package voice

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

const (
	StateIdle     = "idle"
	StateJoining  = "joining"
	StateReady    = "ready"
	StatePlaying  = "playing"
	StateLeaving  = "leaving"
	StateError    = "error"
	idleLimit     = 600 * time.Second
	rosterFresh   = 10 * time.Second
	procWaitLimit = 2 * time.Second
)

// ErrSkipped means Skip or Stop interrupted this sentence. The RTP process stays up.
var ErrSkipped = errors.New("skipped")

// ErrBusy means a join for a different channel is already in progress.
var ErrBusy = errors.New("voice_busy")

// Person is one voice-channel member.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Process is the long-lived ffmpeg the feeder writes to.
type Process interface {
	Write([]byte) (int, error)
	CloseStdin() error
	Kill() error
	Alive() bool
	Done() <-chan struct{}
	Err() error
}

// Hooks are the KOOK and ffmpeg calls. HTTP and sleep stay outside the session lock.
type Hooks struct {
	Join      func(ctx context.Context, channelID, password string) (json.RawMessage, error)
	Leave     func(ctx context.Context, channelID string) error
	KeepAlive func(ctx context.Context, channelID string) error
	UserList  func(ctx context.Context, channelID string) ([]Person, error)
	List      func(ctx context.Context) ([]string, error)
	StartRTP  func(ctx context.Context, info JoinInfo) (Process, error)
}

type pcmItem struct {
	data []byte
	id   uint64
}

// Session is the process-wide voice room.
type Session struct {
	hooks     Hooks
	clock     sleeper
	cooldown  time.Duration
	keepEvery time.Duration
	keepRetry time.Duration
	idleEvery time.Duration

	mu         sync.Mutex
	state      string
	channelID  string
	gen        uint64
	leaving    bool
	leaveDone  chan struct{}
	joinTarget string
	joinDone   chan struct{}
	proc       Process
	feedCtx    context.Context
	feedCancel context.CancelFunc
	keepCancel context.CancelFunc
	feedDone   chan struct{}
	keepDone   chan struct{}
	preamble   chan struct{}
	lastError  string
	leftAt     time.Time
	why        string

	q         []pcmItem
	cur       uint64
	nextID    uint64
	need      map[uint64]int
	played    map[uint64]int
	skipped   map[uint64]bool
	done      map[uint64]chan struct{}
	onStart   map[uint64]func()
	afterStop func()

	people   []Person
	peopleAt time.Time
	aloneFor time.Duration
	selfID   func() string
	busy     func() bool

	booted bool
	bootCh chan struct{}
}

func New(h Hooks) *Session {
	return &Session{
		hooks:     h,
		clock:     realClock{},
		state:     StateIdle,
		cooldown:  3 * time.Second,
		keepEvery: 30 * time.Second,
		keepRetry: 5 * time.Second,
		idleEvery: 30 * time.Second,
		need:      map[uint64]int{},
		played:    map[uint64]int{},
		skipped:   map[uint64]bool{},
		done:      map[uint64]chan struct{}{},
		onStart:   map[uint64]func(){},
		booted:    true,
	}
}

func (s *Session) SetClock(c sleeper) {
	if c != nil {
		s.clock = c
	}
}

func (s *Session) SetCooldown(d time.Duration)  { s.cooldown = d }
func (s *Session) SetKeepEvery(d time.Duration) { s.keepEvery = d }
func (s *Session) SetKeepRetry(d time.Duration) { s.keepRetry = d }

func (s *Session) SetSelfID(fn func() string) { s.selfID = fn }
func (s *Session) SetBusy(fn func() bool)     { s.busy = fn }

// SetBooting makes EnsureJoined wait until Boot finishes.
func (s *Session) SetBooting() {
	s.mu.Lock()
	s.booted = false
	s.bootCh = make(chan struct{})
	s.mu.Unlock()
}

// Boot leaves leftover KOOK rooms. It does not join the pinned channel.
func (s *Session) Boot(ctx context.Context) {
	defer s.markBooted()
	if s.hooks.List == nil {
		return
	}
	ids, err := s.hooks.List(ctx)
	if err != nil || len(ids) == 0 {
		return
	}
	for _, id := range ids {
		if id == "" || s.hooks.Leave == nil {
			continue
		}
		_ = s.hooks.Leave(ctx, id)
	}
	_ = s.clock.Sleep(ctx, s.cooldown)
}

func (s *Session) markBooted() {
	s.mu.Lock()
	s.booted = true
	ch := s.bootCh
	s.bootCh = nil
	s.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

func (s *Session) waitBoot(ctx context.Context) error {
	s.mu.Lock()
	if s.booted {
		s.mu.Unlock()
		return nil
	}
	ch := s.bootCh
	s.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// View is the panel snapshot. State labels live with the UI.
type View struct {
	State     string   `json:"state"`
	ChannelID string   `json:"channel_id"`
	LastError string   `json:"last_error"`
	Roster    []Person `json:"roster"`
}

func (s *Session) State() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == "" {
		return StateIdle
	}
	return s.state
}

func (s *Session) ChannelID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.channelID
}

func (s *Session) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastError
}

func Label(state string) string {
	switch state {
	case StateJoining:
		return "正在加入"
	case StateReady:
		return "已在频道"
	case StatePlaying:
		return "正在播放"
	case StateLeaving:
		return "正在离开"
	case StateError:
		return "错误"
	default:
		return "空闲"
	}
}

// EnsureJoined joins channelID unless this process is already there on a live ffmpeg.
func (s *Session) EnsureJoined(ctx context.Context, channelID, password string) error {
	if err := s.waitBoot(ctx); err != nil {
		return err
	}
	for {
		s.mu.Lock()
		if s.usable(channelID) {
			s.mu.Unlock()
			return nil
		}
		if s.leaving {
			done := s.leaveDone
			s.mu.Unlock()
			if err := waitCh(ctx, done); err != nil {
				return err
			}
			if err := s.pad(ctx); err != nil {
				return err
			}
			continue
		}
		if s.state == StateJoining {
			if s.joinTarget != "" && s.joinTarget != channelID {
				s.mu.Unlock()
				return ErrBusy
			}
			done := s.joinDone
			s.mu.Unlock()
			if err := waitCh(ctx, done); err != nil {
				return err
			}
			continue
		}
		if s.occupiesOther(channelID) {
			s.mu.Unlock()
			if err := s.Leave(ctx, "operator"); err != nil {
				return err
			}
			if err := s.pad(ctx); err != nil {
				return err
			}
			continue
		}
		done := make(chan struct{})
		s.state = StateJoining
		s.joinTarget = channelID
		s.joinDone = done
		startGen := s.gen
		s.mu.Unlock()

		err := s.join(ctx, channelID, password, startGen)
		close(done)
		return err
	}
}

func (s *Session) usable(channelID string) bool {
	if s.leaving || channelID == "" {
		return false
	}
	if s.state != StateReady && s.state != StatePlaying {
		return false
	}
	if s.channelID != channelID {
		return false
	}
	if s.feedCtx != nil && s.feedCtx.Err() != nil {
		return false
	}
	if s.proc == nil || !s.proc.Alive() {
		return false
	}
	return true
}

func (s *Session) occupiesOther(channelID string) bool {
	if s.channelID == "" || s.channelID == channelID {
		return false
	}
	switch s.state {
	case StateReady, StatePlaying, StateJoining, StateLeaving, StateError:
		return true
	default:
		return false
	}
}

func (s *Session) join(ctx context.Context, channelID, password string, startGen uint64) error {
	fail := func(err error) error {
		s.mu.Lock()
		if s.gen == startGen && s.state == StateJoining {
			s.state = StateError
			s.lastError = err.Error()
			s.joinTarget = ""
		}
		s.mu.Unlock()
		return err
	}
	if s.hooks.Join == nil {
		return fail(errors.New("join is not configured"))
	}
	raw, err := s.hooks.Join(ctx, channelID, password)
	if err != nil {
		return fail(err)
	}
	info, err := ParseJoin(raw)
	if err != nil {
		if s.hooks.Leave != nil {
			_ = s.hooks.Leave(ctx, channelID)
		}
		return fail(err)
	}
	if s.hooks.StartRTP == nil {
		return fail(errors.New("rtp is not configured"))
	}
	proc, err := s.hooks.StartRTP(ctx, info)
	if err != nil {
		if s.hooks.Leave != nil {
			_ = s.hooks.Leave(ctx, channelID)
		}
		return fail(err)
	}
	s.mu.Lock()
	if s.gen != startGen || s.state != StateJoining {
		s.mu.Unlock()
		_ = proc.CloseStdin()
		proc.Kill()
		if s.hooks.Leave != nil {
			_ = s.hooks.Leave(context.Background(), channelID)
		}
		return errors.New("join aborted")
	}
	s.gen++
	s.channelID = channelID
	s.state = StateReady
	s.lastError = ""
	s.proc = proc
	s.joinTarget = ""
	feedCtx, feedCancel := context.WithCancel(context.Background())
	keepCtx, keepCancel := context.WithCancel(context.Background())
	s.feedCtx = feedCtx
	s.feedCancel = feedCancel
	s.keepCancel = keepCancel
	s.feedDone = make(chan struct{})
	s.keepDone = make(chan struct{})
	s.preamble = make(chan struct{})
	s.q = nil
	s.cur = 0
	feedDone := s.feedDone
	keepDone := s.keepDone
	s.mu.Unlock()
	go s.feeder(feedCtx, proc, feedDone, s.preamble)
	go s.keepLoop(keepCtx, channelID, keepDone)
	return nil
}

func (s *Session) feeder(ctx context.Context, proc Process, done chan struct{}, preamble chan struct{}) {
	defer close(done)
	preambleLeft := 4
	var once sync.Once
	mark := func() { once.Do(func() { close(preamble) }) }
	err := pace(ctx, s.clock, func(b []byte) error {
		if proc == nil {
			return errors.New("no process")
		}
		errCh := make(chan error, 1)
		go func() {
			_, werr := proc.Write(b)
			errCh <- werr
		}()
		select {
		case werr := <-errCh:
			return werr
		case <-ctx.Done():
			_ = proc.CloseStdin()
			return ctx.Err()
		}
	}, func() ([]byte, error) {
		if preambleLeft > 0 {
			preambleLeft--
			if preambleLeft == 0 {
				mark()
			}
			return silenceChunk(), nil
		}
		return s.takeChunk(), nil
	})
	mark()
	if err != nil && ctx.Err() == nil {
		s.askLeave("ffmpeg")
	}
}

func (s *Session) takeChunk() []byte {
	s.mu.Lock()
	if len(s.q) == 0 {
		if s.state == StatePlaying {
			s.state = StateReady
		}
		s.mu.Unlock()
		return silenceChunk()
	}
	it := s.q[0]
	s.q = s.q[1:]
	s.state = StatePlaying
	s.cur = it.id
	s.played[it.id]++
	n := s.played[it.id]
	need := s.need[it.id]
	cb := s.onStart[it.id]
	if n != 1 {
		cb = nil
	}
	s.mu.Unlock()
	if cb != nil {
		cb()
	}
	if need > 0 && n >= need {
		s.finish(it.id, false)
	}
	return it.data
}

// Play queues PCM for the single feeder. onStart runs when the first chunk is taken.
func (s *Session) Play(ctx context.Context, pcm []byte, onStart func()) error {
	s.mu.Lock()
	pre := s.preamble
	feedDone := s.feedDone
	s.mu.Unlock()
	if pre == nil {
		return errors.New("not in channel")
	}
	select {
	case <-pre:
	case <-ctx.Done():
		return ctx.Err()
	case <-feedDone:
		return errors.New("feeder stopped")
	}
	chunks := packetize(pcm)
	sil := silenceChunk()
	for i := 0; i < 3; i++ {
		chunks = append(chunks, sil)
	}
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan struct{})
	s.done[id] = ch
	s.need[id] = len(chunks)
	if onStart != nil {
		s.onStart[id] = onStart
	}
	for _, c := range chunks {
		s.q = append(s.q, pcmItem{data: c, id: id})
	}
	s.mu.Unlock()
	select {
	case <-ch:
		s.mu.Lock()
		skipped := s.skipped[id]
		s.mu.Unlock()
		s.dropSentence(id)
		if skipped {
			return ErrSkipped
		}
		return nil
	case <-ctx.Done():
		s.dropSentence(id)
		s.finish(id, true)
		return ctx.Err()
	case <-feedDone:
		return errors.New("feeder stopped")
	}
}

func (s *Session) dropSentence(id uint64) {
	s.mu.Lock()
	kept := s.q[:0]
	for _, it := range s.q {
		if it.id != id {
			kept = append(kept, it)
		}
	}
	s.q = kept
	s.mu.Unlock()
}

func (s *Session) finish(id uint64, skipped bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if skipped {
		s.skipped[id] = true
	}
	ch := s.done[id]
	if ch == nil {
		return
	}
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// Skip drops the rest of the current sentence. The person stays in the room.
func (s *Session) Skip() {
	s.mu.Lock()
	id := s.cur
	if id == 0 && len(s.q) > 0 {
		id = s.q[0].id
	}
	s.mu.Unlock()
	if id == 0 {
		return
	}
	s.dropSentence(id)
	s.finish(id, true)
}

// Stop drops every queued sentence and the one that is playing.
func (s *Session) Stop() {
	s.mu.Lock()
	ids := map[uint64]struct{}{}
	if s.cur != 0 {
		ids[s.cur] = struct{}{}
	}
	for _, it := range s.q {
		ids[it.id] = struct{}{}
	}
	s.q = nil
	s.mu.Unlock()
	for id := range ids {
		s.finish(id, true)
	}
}

func (s *Session) askLeave(why string) {
	go func() { _ = s.Leave(context.Background(), why) }()
}

// Leave tears the room down. why "operator" ends in idle; anything else ends in error.
// A caller that finds a leave already running waits for that leave instead of sending another.
func (s *Session) Leave(ctx context.Context, why string) error {
	s.mu.Lock()
	if (s.state == StateIdle || s.state == "") && !s.leaving {
		s.mu.Unlock()
		return nil
	}
	if s.leaving {
		done := s.leaveDone
		s.mu.Unlock()
		return waitCh(ctx, done)
	}
	s.gen++
	myGen := s.gen
	s.state = StateLeaving
	s.leaving = true
	s.why = why
	done := make(chan struct{})
	s.leaveDone = done
	channel := s.channelID
	if s.feedCancel != nil {
		s.feedCancel()
	}
	if s.keepCancel != nil {
		s.keepCancel()
	}
	feedDone := s.feedDone
	keepDone := s.keepDone
	proc := s.proc
	s.mu.Unlock()

	if keepDone != nil {
		<-keepDone
	}
	if feedDone != nil {
		<-feedDone
	}
	if s.afterStop != nil {
		s.afterStop()
	}

	s.mu.Lock()
	if s.gen != myGen {
		if s.leaveDone == done {
			s.leaving = false
		}
		s.mu.Unlock()
		close(done)
		return nil
	}
	s.mu.Unlock()

	if proc != nil {
		_ = proc.CloseStdin()
		select {
		case <-proc.Done():
		case <-time.After(procWaitLimit):
			proc.Kill()
			<-proc.Done()
		}
	}

	s.mu.Lock()
	still := s.gen == myGen
	s.mu.Unlock()
	if still && channel != "" && s.hooks.Leave != nil {
		_ = s.hooks.Leave(ctx, channel)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leaveDone == done {
		s.leaving = false
	}
	if s.gen != myGen {
		close(done)
		return nil
	}
	s.channelID = ""
	s.proc = nil
	s.feedCancel = nil
	s.keepCancel = nil
	s.feedCtx = nil
	s.q = nil
	s.cur = 0
	s.people = nil
	s.peopleAt = time.Time{}
	s.aloneFor = 0
	s.leftAt = s.clock.Now()
	if why == "operator" {
		s.state = StateIdle
		s.lastError = ""
	} else {
		s.state = StateError
		if proc != nil && proc.Err() != nil {
			s.lastError = trimErr(proc.Err().Error())
		} else if why != "" {
			s.lastError = why
		}
	}
	close(done)
	return nil
}

func trimErr(s string) string {
	if len(s) > 2048 {
		return s[len(s)-2048:]
	}
	return s
}

func (s *Session) pad(ctx context.Context) error {
	s.mu.Lock()
	left := s.leftAt
	cool := s.cooldown
	s.mu.Unlock()
	if left.IsZero() || cool <= 0 {
		return nil
	}
	remain := cool - s.clock.Now().Sub(left)
	if remain <= 0 {
		return nil
	}
	return s.clock.Sleep(ctx, remain)
}

func (s *Session) keepLoop(ctx context.Context, channel string, done chan struct{}) {
	defer close(done)
	if s.keepFail(ctx, channel) {
		s.askLeave("keepalive")
		return
	}
	for {
		if err := s.clock.Sleep(ctx, s.keepEvery); err != nil {
			return
		}
		if s.keepFail(ctx, channel) {
			s.askLeave("keepalive")
			return
		}
	}
}

func (s *Session) keepFail(ctx context.Context, channel string) bool {
	if ctx.Err() != nil || s.hooks.KeepAlive == nil {
		return false
	}
	if err := s.hooks.KeepAlive(ctx, channel); err == nil {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	if err := s.clock.Sleep(ctx, s.keepRetry); err != nil {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	if err := s.hooks.KeepAlive(ctx, channel); err == nil {
		return false
	}
	return ctx.Err() == nil
}

func waitCh(ctx context.Context, ch <-chan struct{}) error {
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Note applies joined_channel / exited_channel for the pinned channel only.
func (s *Session) Note(channel, user string, joined bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if channel == "" || user == "" || channel != s.channelID {
		return
	}
	if joined {
		for _, p := range s.people {
			if p.ID == user {
				return
			}
		}
		s.people = append(s.people, Person{ID: user})
		return
	}
	kept := s.people[:0]
	for _, p := range s.people {
		if p.ID != user {
			kept = append(kept, p)
		}
	}
	s.people = kept
}

// Roster returns a cached user list. The cache is 10 seconds.
func (s *Session) Roster(ctx context.Context) ([]Person, error) {
	s.mu.Lock()
	channel := s.channelID
	fresh := s.peopleAt
	cached := append([]Person(nil), s.people...)
	inRoom := s.state == StateReady || s.state == StatePlaying
	s.mu.Unlock()
	if channel == "" || !inRoom {
		return []Person{}, nil
	}
	if !fresh.IsZero() && s.clock.Now().Sub(fresh) < rosterFresh {
		return cached, nil
	}
	if s.hooks.UserList == nil {
		return cached, nil
	}
	people, err := s.hooks.UserList(ctx, channel)
	if err != nil {
		return cached, err
	}
	s.mu.Lock()
	if s.channelID == channel {
		s.people = append([]Person(nil), people...)
		s.peopleAt = s.clock.Now()
	}
	out := append([]Person(nil), s.people...)
	s.mu.Unlock()
	return out, nil
}

// WatchIdle leaves after 600 seconds alone. List failures do not accumulate.
func (s *Session) WatchIdle(ctx context.Context) {
	for {
		if err := s.clock.Sleep(ctx, s.idleEvery); err != nil {
			return
		}
		s.mu.Lock()
		inRoom := (s.state == StateReady || s.state == StatePlaying) && !s.leaving
		queued := len(s.q) > 0 || s.state == StatePlaying
		channel := s.channelID
		acc := s.aloneFor
		s.mu.Unlock()
		busy := false
		if s.busy != nil && s.busy() {
			busy = true
		}
		if !inRoom || queued || busy || channel == "" {
			s.mu.Lock()
			s.aloneFor = 0
			s.mu.Unlock()
			continue
		}
		people, err := s.Roster(ctx)
		self := ""
		if s.selfID != nil {
			self = s.selfID()
		}
		next, leave := idleStep(err == nil, self != "", countOthers(people, self), acc, s.idleEvery, idleLimit)
		s.mu.Lock()
		if s.channelID == channel && !s.leaving {
			s.aloneFor = next
		}
		s.mu.Unlock()
		if leave {
			s.askLeave("idle")
		}
	}
}

func countOthers(people []Person, self string) int {
	n := 0
	for _, p := range people {
		if p.ID != self {
			n++
		}
	}
	return n
}

// idleStep is the 600 second rule. A failed list or an unknown self id does not accumulate.
func idleStep(listOK, selfKnown bool, others int, accumulated, step, limit time.Duration) (time.Duration, bool) {
	if !listOK || !selfKnown || others > 0 {
		return 0, false
	}
	next := accumulated + step
	return next, next >= limit
}
