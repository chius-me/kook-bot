package speak

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"bsdx/internal/secret"
	"bsdx/internal/store"
	"bsdx/internal/tts"
	"bsdx/internal/voice"
)

const (
	queueLimit = 8
	textLimit  = 200
	previewMax = 80
	dedupeFor  = 60 * time.Second
)

// SpeakRequest is one sentence. VoiceChannelID is ignored by v1 HTTP.
type SpeakRequest struct {
	Text            string
	VoiceID         string
	Provider        string
	Rate            float64
	Volume          float64
	VoiceChannelID  string
	Requester       string
	ClientRequestID string
}

// Job is the operator-visible sentence.
type Job struct {
	ID        int64  `json:"id"`
	Text      string `json:"text"`
	Status    string `json:"status"`
	VoiceID   string `json:"voice_id"`
	Provider  string `json:"provider"`
	CreatedAt string `json:"created_at"`
	Error     string `json:"error,omitempty"`
	UIHint    string `json:"ui_hint,omitempty"`
	UIHintMS  int    `json:"ui_hint_ms,omitempty"`
}

// Room is the single voice session.
type Room interface {
	EnsureJoined(ctx context.Context, channelID, password string) error
	Play(ctx context.Context, pcm []byte, onStart func()) error
	Skip()
	Stop()
	Leave(ctx context.Context) error
	State() string
	ChannelID() string
	LastError() string
	Roster(ctx context.Context) ([]voice.Person, error)
}

// Snapshot is the queue plus the room.
type Snapshot struct {
	Current    *Job           `json:"current"`
	Queue      []Job          `json:"queue"`
	History    []Job          `json:"history"`
	PlayingID  int64          `json:"playing_id"`
	State      string         `json:"state"`
	StateLabel string         `json:"state_label"`
	ChannelID  string         `json:"channel_id"`
	LastError  string         `json:"last_error"`
	Roster     []voice.Person `json:"roster"`
}

type liveJob struct {
	Job
	rate      float64
	volume    float64
	channel   string
	password  string
	runes     int
	cacheHit  bool
	coldJoin  bool
	inChannel bool
	started   time.Time
	skip      atomic.Bool
	stop      atomic.Bool
	base      context.Context
	cancel    context.CancelFunc
}

// Service is the single speak worker.
type Service struct {
	store        *store.Store
	box          *secret.Box
	room         Room
	edge         tts.Edge
	allowPrivate bool
	dataDir      string
	log          *slog.Logger
	now          func() time.Time
	decode       func(ctx context.Context, path string, volume float64) ([]byte, error)
	mirror       MirrorFunc

	mu      sync.Mutex
	cond    *sync.Cond
	queue   []*liveJob
	current *liveJob
	recent  map[string]recentJob
	stopped bool
}

type recentJob struct {
	job Job
	at  time.Time
}

func New(st *store.Store, box *secret.Box, room Room, edgeBin, dataDir string, allowPrivate bool, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	s := &Service{
		store:        st,
		box:          box,
		room:         room,
		edge:         tts.Edge{Bin: edgeBin},
		allowPrivate: allowPrivate,
		dataDir:      dataDir,
		log:          log,
		now:          time.Now,
		decode: func(ctx context.Context, path string, volume float64) ([]byte, error) {
			return voice.DecodePCM(ctx, "", path, volume)
		},
		recent: map[string]recentJob{},
	}
	s.cond = sync.NewCond(&s.mu)
	go s.loop()
	return s
}

// SetMirror replaces the nil mirror. The func itself checks the binding switch.
func (s *Service) SetMirror(fn MirrorFunc) {
	s.mu.Lock()
	s.mirror = fn
	s.mu.Unlock()
}

// SetDecode replaces ffmpeg for tests.
func (s *Service) SetDecode(fn func(context.Context, string, float64) ([]byte, error)) {
	if fn != nil {
		s.decode = fn
	}
}

// SetEdgeRunner replaces the CLI for tests.
func (s *Service) SetEdgeRunner(run tts.Runner) { s.edge.Run = run }

// SetNow replaces the clock for dedupe and UI hints.
func (s *Service) SetNow(fn func() time.Time) {
	if fn != nil {
		s.now = fn
	}
}

// Busy is true while a sentence is queued or in progress.
func (s *Service) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current != nil || len(s.queue) > 0
}

func (s *Service) Submit(_ context.Context, req SpeakRequest) (Job, error) {
	text, rej := Clean(req.Text, textLimit)
	if rej != nil {
		return Job{}, rej
	}
	settings, binding, err := s.load()
	if err != nil {
		return Job{}, reject(http.StatusServiceUnavailable, "kook_unavailable", "暂时无法读取设置")
	}
	if binding.VoiceChannelID == "" {
		return Job{}, reject(http.StatusConflict, "voice_channel_unbound", "还没有钉住语音频道")
	}
	provider := req.Provider
	if provider == "" {
		provider = settings.Provider
	}
	if provider != "edge" && provider != "openai" {
		return Job{}, reject(http.StatusBadRequest, "bad_json", "语音引擎无效")
	}
	rate := req.Rate
	if rate == 0 {
		rate = settings.Rate
	}
	if rate == 0 {
		rate = 1
	}
	volume := req.Volume
	if volume == 0 {
		volume = settings.Volume
	}
	if volume == 0 {
		volume = 0.8
	}
	if rej = checkRate(rate); rej != nil {
		return Job{}, rej
	}
	if rej = checkVolume(volume); rej != nil {
		return Job{}, rej
	}
	voiceID := req.VoiceID
	if voiceID == "" {
		if provider == "edge" {
			voiceID = settings.EdgeVoice
		} else {
			voiceID = settings.APIVoice
		}
	}
	if voiceID == "" && provider == "edge" {
		voiceID = tts.DefaultEdgeVoice
	}
	if provider == "edge" && !tts.EdgeAllowed(voiceID) {
		return Job{}, reject(http.StatusBadRequest, "bad_voice", "没有这个音色")
	}
	if provider == "openai" {
		if err := tts.CheckAPIVoice(voiceID); err != nil {
			return Job{}, reject(http.StatusBadRequest, "bad_api_voice", "接口音色无效")
		}
	}
	password, err := s.password(binding)
	if err != nil {
		return Job{}, reject(http.StatusServiceUnavailable, "secret_mismatch", "密钥已更换，请重新填写 Token")
	}
	now := s.now()
	s.mu.Lock()
	s.sweepRecent(now)
	if req.ClientRequestID != "" {
		if prev, ok := s.recent[req.ClientRequestID]; ok {
			job := prev.job
			s.mu.Unlock()
			return job, nil
		}
	}
	if s.depth() >= queueLimit {
		s.mu.Unlock()
		return Job{}, reject(http.StatusTooManyRequests, "queue_full", "队列已满")
	}
	s.mu.Unlock()

	row := store.HistoryRow{
		CreatedAt:      now.UTC().Format(time.RFC3339),
		Text:           text,
		Provider:       provider,
		VoiceID:        voiceID,
		Rate:           rate,
		Volume:         volume,
		VoiceChannelID: binding.VoiceChannelID,
		Status:         "queued",
	}
	var id int64
	if s.store != nil {
		id, err = s.store.InsertHistory(context.Background(), row)
		if err != nil {
			return Job{}, reject(http.StatusServiceUnavailable, "kook_unavailable", "暂时无法保存")
		}
	}
	job := &liveJob{
		Job: Job{
			ID:        id,
			Text:      text,
			Status:    "queued",
			VoiceID:   voiceID,
			Provider:  provider,
			CreatedAt: row.CreatedAt,
		},
		rate:     rate,
		volume:   volume,
		channel:  binding.VoiceChannelID,
		password: password,
		runes:    utf8.RuneCountInString(text),
	}
	base, cancel := context.WithCancel(context.Background())
	job.base = base
	job.cancel = cancel
	s.mu.Lock()
	if s.depth() >= queueLimit {
		s.mu.Unlock()
		cancel()
		s.finish(job, "skipped", "")
		return Job{}, reject(http.StatusTooManyRequests, "queue_full", "队列已满")
	}
	s.queue = append(s.queue, job)
	if req.ClientRequestID != "" {
		s.recent[req.ClientRequestID] = recentJob{job: job.Job, at: now}
	}
	s.cond.Signal()
	out := job.Job
	s.mu.Unlock()
	return out, nil
}

func (s *Service) depth() int {
	n := len(s.queue)
	if s.current != nil {
		n++
	}
	return n
}

func (s *Service) sweepRecent(now time.Time) {
	for k, v := range s.recent {
		if now.Sub(v.at) > dedupeFor {
			delete(s.recent, k)
		}
	}
}

func (s *Service) Skip() error {
	s.mu.Lock()
	cur := s.current
	s.mu.Unlock()
	if cur != nil {
		cur.skip.Store(true)
		if cur.cancel != nil {
			cur.cancel()
		}
	}
	if s.room != nil {
		s.room.Skip()
	}
	return nil
}

func (s *Service) Stop() error {
	s.mu.Lock()
	pending := s.queue
	s.queue = nil
	cur := s.current
	s.mu.Unlock()
	for _, job := range pending {
		job.stop.Store(true)
		s.finish(job, "skipped", "")
	}
	if cur != nil {
		cur.stop.Store(true)
		if cur.cancel != nil {
			cur.cancel()
		}
	}
	if s.room != nil {
		s.room.Stop()
	}
	return nil
}

func (s *Service) Leave(ctx context.Context) error {
	if s.room == nil {
		return nil
	}
	return s.room.Leave(ctx)
}

func (s *Service) Snapshot(ctx context.Context) Snapshot {
	s.mu.Lock()
	var current *Job
	var playing int64
	if s.current != nil {
		job := s.public(s.current)
		current = &job
		if s.current.Status == "playing" {
			playing = s.current.ID
		}
	}
	queue := make([]Job, 0, len(s.queue))
	for _, job := range s.queue {
		queue = append(queue, job.Job)
	}
	s.mu.Unlock()
	snap := Snapshot{Current: current, Queue: queue, History: s.history(ctx), PlayingID: playing}
	if s.room != nil {
		snap.State = s.room.State()
		snap.ChannelID = s.room.ChannelID()
		snap.LastError = s.room.LastError()
		roster, _ := s.room.Roster(ctx)
		snap.Roster = roster
	}
	if snap.State == "" {
		snap.State = voice.StateIdle
	}
	if snap.Roster == nil {
		snap.Roster = []voice.Person{}
	}
	snap.StateLabel = voice.Label(snap.State)
	return snap
}

func (s *Service) history(ctx context.Context) []Job {
	if s.store == nil {
		return []Job{}
	}
	rows, err := s.store.ListHistory(ctx, 20)
	if err != nil {
		return []Job{}
	}
	out := make([]Job, 0, len(rows))
	for _, row := range rows {
		out = append(out, Job{
			ID:        row.ID,
			Text:      row.Text,
			Status:    row.Status,
			VoiceID:   row.VoiceID,
			Provider:  row.Provider,
			CreatedAt: row.CreatedAt,
			Error:     row.Error,
		})
	}
	return out
}

func (s *Service) loop() {
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.stopped {
			s.cond.Wait()
		}
		if s.stopped {
			s.mu.Unlock()
			return
		}
		job := s.queue[0]
		s.queue = s.queue[1:]
		s.current = job
		s.mu.Unlock()
		s.run(job)
		s.mu.Lock()
		if s.current == job {
			s.current = nil
		}
		s.mu.Unlock()
	}
}

func (s *Service) run(job *liveJob) {
	if job.base == nil {
		job.base, job.cancel = context.WithCancel(context.Background())
	}
	if job.stop.Load() || job.skip.Load() {
		s.finish(job, "skipped", "")
		return
	}
	job.started = s.now()
	if s.room != nil {
		st := s.room.State()
		job.inChannel = st == voice.StateReady || st == voice.StatePlaying
		job.coldJoin = !job.inChannel
	} else {
		job.coldJoin = true
	}
	s.finish(job, "synthesizing", "")
	ctx := job.base
	synthCtx, synthCancel := context.WithTimeout(ctx, 20*time.Second)
	path, hit, err := s.synthesize(synthCtx, job)
	synthCancel()
	if job.stop.Load() || job.skip.Load() || errors.Is(ctx.Err(), context.Canceled) {
		s.finish(job, "skipped", "")
		s.removeTemp(path, hit)
		return
	}
	if err != nil {
		s.finish(job, "error", "语音合成失败")
		s.removeTemp(path, hit)
		return
	}
	job.cacheHit = hit
	if s.room == nil {
		s.finish(job, "error", "语音未就绪")
		s.removeTemp(path, hit)
		return
	}
	joinCtx, joinCancel := context.WithTimeout(ctx, 20*time.Second)
	err = s.room.EnsureJoined(joinCtx, job.channel, job.password)
	joinCancel()
	if job.stop.Load() || job.skip.Load() {
		s.finish(job, "skipped", "")
		s.removeTemp(path, hit)
		return
	}
	if err != nil {
		msg := "加入语音频道失败"
		if s.room.LastError() != "" {
			msg = s.room.LastError()
		}
		s.finish(job, "error", msg)
		s.removeTemp(path, hit)
		return
	}
	pcm, err := s.decode(ctx, path, job.volume)
	if err != nil {
		s.finish(job, "error", "语音解码失败")
		s.removeTemp(path, hit)
		return
	}
	dur := time.Duration(len(pcm)) * time.Second / 192000
	playCtx, playCancel := context.WithTimeout(ctx, dur+5*time.Second)
	defer playCancel()
	started := make(chan struct{})
	abort := make(chan struct{})
	var once sync.Once
	go s.mirrorAfter(started, abort, job)
	err = s.room.Play(playCtx, pcm, func() { once.Do(func() { close(started) }) })
	select {
	case <-started:
	default:
		close(abort)
	}
	s.removeTemp(path, hit)
	if job.stop.Load() || job.skip.Load() || errors.Is(err, voice.ErrSkipped) || errors.Is(ctx.Err(), context.Canceled) {
		s.finish(job, "skipped", "")
		return
	}
	if err != nil {
		s.finish(job, "error", "播放失败")
		return
	}
	s.finish(job, "done", "")
}

func (s *Service) mirrorAfter(started <-chan struct{}, abort <-chan struct{}, job *liveJob) {
	select {
	case <-started:
	case <-abort:
		return
	}
	s.mu.Lock()
	fn := s.mirror
	copied := job.Job
	s.mu.Unlock()
	if fn == nil {
		return
	}
	if err := fn(context.Background(), copied); err != nil {
		s.noteError(job, err.Error())
	}
}

func (s *Service) synthesize(ctx context.Context, job *liveJob) (string, bool, error) {
	root := filepath.Join(s.dataDir, "audio")
	cacheDir := filepath.Join(root, "cache")
	tmpDir := filepath.Join(root, "tmp")
	_ = os.MkdirAll(cacheDir, 0o700)
	_ = os.MkdirAll(tmpDir, 0o700)
	key := tts.CacheKey(job.Provider, job.VoiceID, job.rate, job.Text)
	if tts.Cacheable(job.Text) {
		cached := tts.CachePath(cacheDir, key)
		if st, err := os.Stat(cached); err == nil && st.Size() >= 256 && audioInside(root, cached) {
			return cached, true, nil
		}
	}
	settings, _, err := s.load()
	if err != nil {
		return "", false, err
	}
	out := filepath.Join(tmpDir, randomName()+".mp3")
	req := tts.Request{Text: job.Text, VoiceID: job.VoiceID, Rate: job.rate, Volume: job.volume}
	var synth tts.Provider
	if job.Provider == "openai" {
		apiKey, err := s.apiKey(settings)
		if err != nil {
			return "", false, err
		}
		synth = tts.OpenAI{
			URL:          settings.APIURL,
			APIKey:       apiKey,
			Model:        settings.APIModel,
			AllowPrivate: s.allowPrivate,
		}
	} else {
		synth = s.edge
	}
	if err := synth.Synthesize(ctx, req, out); err != nil {
		_ = os.Remove(out)
		return "", false, err
	}
	if tts.Cacheable(job.Text) {
		dest := tts.CachePath(cacheDir, key)
		if err := os.Rename(out, dest); err == nil {
			_ = tts.PruneCache(cacheDir, 200)
			return dest, false, nil
		}
	}
	return out, false, nil
}

func (s *Service) removeTemp(path string, cacheHit bool) {
	if path == "" || cacheHit {
		return
	}
	if filepath.Base(filepath.Dir(path)) == "tmp" {
		_ = os.Remove(path)
	}
}

// Preview synthesizes a short clip and does not join or write history.
func (s *Service) Preview(ctx context.Context, text, voiceID, provider string, rate float64) ([]byte, error) {
	clean, rej := Clean(text, previewMax)
	if rej != nil {
		return nil, rej
	}
	settings, _, err := s.load()
	if err != nil {
		return nil, reject(http.StatusServiceUnavailable, "kook_unavailable", "暂时无法读取设置")
	}
	if provider == "" {
		provider = settings.Provider
	}
	if provider != "edge" && provider != "openai" {
		return nil, reject(http.StatusBadRequest, "bad_json", "语音引擎无效")
	}
	if rate == 0 {
		rate = settings.Rate
	}
	if rate == 0 {
		rate = 1
	}
	if rej = checkRate(rate); rej != nil {
		return nil, rej
	}
	if voiceID == "" {
		if provider == "edge" {
			voiceID = settings.EdgeVoice
		} else {
			voiceID = settings.APIVoice
		}
	}
	if provider == "edge" && !tts.EdgeAllowed(voiceID) {
		return nil, reject(http.StatusBadRequest, "bad_voice", "没有这个音色")
	}
	if provider == "openai" {
		if err := tts.CheckAPIVoice(voiceID); err != nil {
			return nil, reject(http.StatusBadRequest, "bad_api_voice", "接口音色无效")
		}
	}
	job := &liveJob{Job: Job{Text: clean, VoiceID: voiceID, Provider: provider}, rate: rate, volume: settings.Volume}
	path, hit, err := s.synthesize(ctx, job)
	if err != nil {
		if errors.Is(err, tts.ErrBadVoice) {
			return nil, reject(http.StatusBadRequest, "bad_voice", "没有这个音色")
		}
		if errors.Is(err, tts.ErrBadURL) {
			return nil, reject(http.StatusBadRequest, "bad_url", "语音接口地址无效")
		}
		if errors.Is(err, tts.ErrBadAPIVoice) {
			return nil, reject(http.StatusBadRequest, "bad_api_voice", "接口音色无效")
		}
		return nil, reject(http.StatusServiceUnavailable, "tts_failed", "语音合成失败")
	}
	defer s.removeTemp(path, hit)
	body, err := os.ReadFile(path)
	if err != nil || len(body) < 256 {
		return nil, reject(http.StatusServiceUnavailable, "tts_failed", "语音合成失败")
	}
	return body, nil
}

func (s *Service) load() (store.TTSSettings, store.Binding, error) {
	if s.store == nil {
		return store.TTSSettings{Provider: "edge", EdgeVoice: tts.DefaultEdgeVoice, Rate: 1, Volume: 0.8}, store.Binding{}, nil
	}
	settings, err := s.store.TTS(context.Background())
	if err != nil {
		return store.TTSSettings{}, store.Binding{}, err
	}
	binding, err := s.store.Binding(context.Background())
	if err != nil {
		return store.TTSSettings{}, store.Binding{}, err
	}
	if settings.Provider == "" {
		settings.Provider = "edge"
	}
	if settings.EdgeVoice == "" {
		settings.EdgeVoice = tts.DefaultEdgeVoice
	}
	return settings, binding, nil
}

func (s *Service) password(b store.Binding) (string, error) {
	if s.box == nil || len(b.VoicePasswordNonce) == 0 || len(b.VoicePasswordCT) == 0 {
		return "", nil
	}
	plain, err := s.box.Open(secret.PurposeVoicePassword, b.VoicePasswordNonce, b.VoicePasswordCT)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Service) apiKey(t store.TTSSettings) (string, error) {
	if s.box == nil || len(t.APIKeyNonce) == 0 || len(t.APIKeyCT) == 0 {
		return "", nil
	}
	plain, err := s.box.Open(secret.PurposeTTSAPIKey, t.APIKeyNonce, t.APIKeyCT)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Service) public(job *liveJob) Job {
	out := job.Job
	hint, ms := uiHint(job, s.now())
	out.UIHint = hint
	out.UIHintMS = ms
	return out
}

func uiHint(job *liveJob, now time.Time) (string, int) {
	if job.started.IsZero() {
		return "", 0
	}
	elapsed := now.Sub(job.started)
	if job.coldJoin && (job.Status == "synthesizing" || job.Status == "playing" || job.Status == "queued") {
		if elapsed >= 6*time.Second {
			return "正在加入", 6000
		}
		return "", 6000
	}
	if job.cacheHit && job.inChannel {
		if elapsed >= time.Second {
			return "仍在合成", 1000
		}
		return "", 1000
	}
	if job.inChannel && !job.cacheHit && job.runes <= 20 {
		if elapsed >= 3*time.Second {
			return "仍在合成", 3000
		}
		return "", 3000
	}
	return "", 0
}

func (s *Service) finish(job *liveJob, status, errText string) {
	s.mu.Lock()
	job.Status = status
	if errText != "" {
		job.Error = errText
	}
	row := job
	s.mu.Unlock()
	s.persist(row)
}

func (s *Service) noteError(job *liveJob, errText string) {
	s.mu.Lock()
	if job.Error == "" {
		job.Error = errText
	}
	s.mu.Unlock()
	s.persist(job)
}

func (s *Service) persist(job *liveJob) {
	if s.store == nil || job.ID == 0 {
		return
	}
	s.mu.Lock()
	row := store.HistoryRow{
		ID:             job.ID,
		Status:         job.Status,
		Error:          job.Error,
		VoiceChannelID: job.channel,
	}
	s.mu.Unlock()
	if err := s.store.UpdateHistory(context.Background(), row); err != nil && s.log != nil {
		s.log.Warn("speak history", "component", "speak", "job_id", row.ID, "err", err.Error())
	}
}

func audioInside(root, abs string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(abs))
	if err != nil {
		return false
	}
	return rel != ".." && !filepath.IsAbs(rel) && (len(rel) < 3 || rel[:3] != ".."+string(filepath.Separator))
}

func randomName() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "clip"
	}
	return hex.EncodeToString(b[:])
}

// VoiceRoom adapts Session.Leave to the speaker interface.
type VoiceRoom struct{ *voice.Session }

func (v VoiceRoom) Leave(ctx context.Context) error {
	if v.Session == nil {
		return nil
	}
	return v.Session.Leave(ctx, "operator")
}
