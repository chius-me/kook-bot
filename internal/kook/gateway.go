package kook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"bsdx/internal/module"
)

const (
	actionStop = iota
	actionRefetch
	actionRejected
	actionResumeFail
)

// Timings are the gateway ladder. Zero Hello means DefaultTimings.
type Timings struct {
	Hello          time.Duration
	Pong           time.Duration
	PingBase       time.Duration
	PingJitter     time.Duration
	DialGap        [2]time.Duration
	ExtraHeartbeat [2]time.Duration
	ResumeGap      [2]time.Duration
	FetchBase      time.Duration
	FetchCap       time.Duration
}

// DefaultTimings is the official ladder: 6s hello and pong, 30s±5s ping,
// two extra heartbeats at 2s and 4s, two resumes at 8s and 16s, fetch cap 60s.
func DefaultTimings() Timings {
	return Timings{
		Hello:          6 * time.Second,
		Pong:           6 * time.Second,
		PingBase:       30 * time.Second,
		PingJitter:     5 * time.Second,
		DialGap:        [2]time.Duration{2 * time.Second, 4 * time.Second},
		ExtraHeartbeat: [2]time.Duration{2 * time.Second, 4 * time.Second},
		ResumeGap:      [2]time.Duration{8 * time.Second, 16 * time.Second},
		FetchBase:      time.Second,
		FetchCap:       60 * time.Second,
	}
}

// SessionStore persists the session id and sn for this process only.
// A new process does not read them back into the first dial.
type SessionStore interface {
	SetGateway(ctx context.Context, sessionID string, sn int64) error
	ClearGateway(ctx context.Context) error
}

type wsConn interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(int, []byte) error
	Close() error
}

// Gateway keeps one KOOK websocket. compress=0, text frames only.
type Gateway struct {
	Fetch    func(ctx context.Context, token string) (string, error)
	Dial     func(ctx context.Context, rawURL string) (wsConn, error)
	Token    func() (string, bool)
	Sessions SessionStore
	Host     *module.Host
	Log      *slog.Logger
	Status   func(string)
	Timings  Timings
	Rand     *rand.Rand

	mu      sync.Mutex
	writeMu sync.Mutex
	parent  context.Context
	cancel  context.CancelFunc
	epoch   atomic.Int64
}

func (g *Gateway) timings() Timings {
	if g.Timings.Hello == 0 {
		return DefaultTimings()
	}
	return g.Timings
}

// Start runs until parent is cancelled. Restart drops the socket and begins
// again at gateway/index with sn 0.
func (g *Gateway) Start(parent context.Context) {
	g.mu.Lock()
	g.parent = parent
	g.mu.Unlock()
	g.Restart()
}

// Restart cancels the current session and dials a new one.
func (g *Gateway) Restart() {
	g.mu.Lock()
	parent := g.parent
	if g.cancel != nil {
		g.cancel()
	}
	if parent == nil {
		g.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	g.cancel = cancel
	epoch := g.epoch.Add(1)
	g.mu.Unlock()
	go g.Run(ctx, epoch)
}

func (g *Gateway) setStatus(state string) {
	if g.Status != nil {
		g.Status(state)
	}
}

func (g *Gateway) log() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// Run loops the connection ladder until ctx is done or the token is rejected.
func (g *Gateway) Run(ctx context.Context, epoch int64) {
	t := g.timings()
	var fetchWait time.Duration
	for {
		if ctx.Err() != nil || g.epoch.Load() != epoch {
			return
		}
		token, ok := "", false
		if g.Token != nil {
			token, ok = g.Token()
		}
		if !ok || token == "" {
			g.setStatus("down")
			<-ctx.Done()
			return
		}
		rawURL, err := g.fetch(ctx, token)
		if err != nil {
			g.setStatus("down")
			wait := t.FetchBase
			if fetchWait > 0 {
				wait = fetchWait
			}
			if t.FetchCap > 0 && wait > t.FetchCap {
				wait = t.FetchCap
			}
			if !sleepCtx(ctx, wait) {
				return
			}
			fetchWait = wait * 2
			if t.FetchCap > 0 && fetchWait > t.FetchCap {
				fetchWait = t.FetchCap
			}
			continue
		}
		fetchWait = 0
		g.log().Info("gateway dial", "component", "gateway", "url", gatewayHost(rawURL))
		switch g.connect(ctx, epoch, rawURL, token) {
		case actionStop:
			return
		case actionRejected:
			g.setStatus("token_rejected")
			<-ctx.Done()
			return
		default:
			g.setStatus("down")
		}
	}
}

func (g *Gateway) fetch(ctx context.Context, token string) (string, error) {
	if g.Fetch != nil {
		return g.Fetch(ctx, token)
	}
	return "", errors.New("gateway fetch is not set")
}

func (g *Gateway) dial(ctx context.Context, rawURL string) (wsConn, error) {
	if g.Dial != nil {
		return g.Dial(ctx, rawURL)
	}
	return defaultDial(ctx, rawURL)
}

func defaultDial(ctx context.Context, rawURL string) (wsConn, error) {
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := d.DialContext(ctx, rawURL, nil)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (g *Gateway) connect(ctx context.Context, epoch int64, rawURL, token string) int {
	conn, err := g.dialTwice(ctx, rawURL)
	if err != nil {
		if ctx.Err() != nil {
			return actionStop
		}
		return actionRefetch
	}
	defer conn.Close()
	frames := readFrames(conn)
	hello, act := g.waitHello(ctx, frames)
	if act != 0 {
		return act
	}
	cur := &snCursor{}
	g.persist(ctx, epoch, hello.session, 0)
	g.setStatus("ready")
	return g.pump(ctx, epoch, conn, frames, rawURL, token, hello.session, cur)
}

type helloOK struct {
	session string
}

func (g *Gateway) waitHello(ctx context.Context, frames <-chan wsIn) (helloOK, int) {
	timer := time.NewTimer(g.timings().Hello)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return helloOK{}, actionStop
		case <-timer.C:
			return helloOK{}, actionRefetch
		case in, ok := <-frames:
			if !ok || in.err != nil {
				return helloOK{}, actionRefetch
			}
			if in.kind == websocket.BinaryMessage {
				g.log().Warn("gateway binary frame", "component", "gateway")
				return helloOK{}, actionRefetch
			}
			sig, ok := parseSignal(in.data)
			if !ok {
				continue
			}
			if sig.s == 5 {
				return helloOK{}, actionRefetch
			}
			if sig.s != 1 {
				continue
			}
			switch sig.code {
			case 0:
				if sig.session == "" {
					return helloOK{}, actionRefetch
				}
				return helloOK{session: sig.session}, 0
			case 40101, 40102:
				return helloOK{}, actionRejected
			default:
				// 40100 and 40103 reconnect. 40103 is not token_rejected.
				return helloOK{}, actionRefetch
			}
		}
	}
}

func (g *Gateway) pump(ctx context.Context, epoch int64, conn wsConn, frames <-chan wsIn, rawURL, token, session string, cur *snCursor) int {
	t := g.timings()
	ping := time.NewTimer(g.pingDelay())
	defer ping.Stop()
	var pong *time.Timer
	pongOn := false
	stopPong := func() {
		if pongOn {
			pong.Stop()
			pongOn = false
		}
	}
	defer stopPong()
	armPong := func(d time.Duration) {
		stopPong()
		pong = time.NewTimer(d)
		pongOn = true
	}
	extra := 0
	for {
		var pongC <-chan time.Time
		if pongOn {
			pongC = pong.C
		}
		select {
		case <-ctx.Done():
			return actionStop
		case <-ping.C:
			if !pongOn {
				if err := g.writePing(conn, cur.maxSn); err != nil {
					return g.resume(ctx, epoch, conn, rawURL, token, session, cur)
				}
				armPong(t.Pong)
				extra = 0
			}
			ping.Reset(g.pingDelay())
		case <-pongC:
			if extra >= 2 {
				return g.resume(ctx, epoch, conn, rawURL, token, session, cur)
			}
			gap := t.ExtraHeartbeat[extra]
			extra++
			if err := g.writePing(conn, cur.maxSn); err != nil {
				return g.resume(ctx, epoch, conn, rawURL, token, session, cur)
			}
			armPong(gap)
		case in, ok := <-frames:
			if !ok || in.err != nil {
				return g.resume(ctx, epoch, conn, rawURL, token, session, cur)
			}
			if in.kind == websocket.BinaryMessage {
				g.log().Warn("gateway binary frame", "component", "gateway")
				return actionRefetch
			}
			act := g.onFrame(ctx, epoch, in.data, session, cur, func() {
				stopPong()
				extra = 0
			})
			if act != 0 {
				return act
			}
		}
	}
}

func (g *Gateway) onFrame(ctx context.Context, epoch int64, data []byte, session string, cur *snCursor, gotPong func()) int {
	sig, ok := parseSignal(data)
	if !ok {
		return 0
	}
	switch sig.s {
	case 3:
		gotPong()
		return 0
	case 5:
		cur.reset()
		return actionRefetch
	case 6:
		return 0
	case 1:
		switch sig.code {
		case 40101, 40102:
			return actionRejected
		case 40100, 40103:
			return actionRefetch
		case 40106, 40107, 40108:
			g.clearSession(ctx, epoch)
			return actionRefetch
		default:
			return 0
		}
	case 0:
		before := cur.maxSn
		pushed := cur.Push(sig.sn, sig.d)
		if pushed.Poison {
			g.log().Warn("gateway frame skipped", "component", "gateway", "sn", sig.sn)
		}
		if cur.maxSn != before {
			g.persist(ctx, epoch, session, cur.maxSn)
		}
		for _, ev := range pushed.Events {
			if g.Host != nil {
				_ = g.Host.Dispatch(ctx, ev)
			}
		}
		if pushed.Fatal {
			cur.reset()
			g.clearSession(ctx, epoch)
			return actionRefetch
		}
		return 0
	default:
		return 0
	}
}

func (g *Gateway) resume(ctx context.Context, epoch int64, conn wsConn, rawURL, token, session string, cur *snCursor) int {
	if conn != nil {
		_ = conn.Close()
	}
	t := g.timings()
	for i := 0; i < 2; i++ {
		gap := t.ResumeGap[i]
		if !sleepCtx(ctx, gap) {
			return actionStop
		}
		if g.epoch.Load() != epoch {
			return actionStop
		}
		resumed, err := resumeURL(rawURL, session, cur.maxSn)
		if err != nil {
			break
		}
		conn, err := g.dial(ctx, resumed)
		if err != nil {
			if ctx.Err() != nil {
				return actionStop
			}
			continue
		}
		act := g.resumeOnce(ctx, epoch, conn, rawURL, token, session, cur, gap)
		conn.Close()
		if act != actionResumeFail {
			return act
		}
	}
	g.clearSession(ctx, epoch)
	cur.reset()
	return actionRefetch
}

func (g *Gateway) resumeOnce(ctx context.Context, epoch int64, conn wsConn, rawURL, token, session string, cur *snCursor, ackWait time.Duration) int {
	frames := readFrames(conn)
	timer := time.NewTimer(ackWait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return actionStop
		case <-timer.C:
			return actionResumeFail
		case in, ok := <-frames:
			if !ok || in.err != nil {
				return actionResumeFail
			}
			if in.kind == websocket.BinaryMessage {
				return actionRefetch
			}
			sig, ok := parseSignal(in.data)
			if !ok {
				continue
			}
			switch sig.s {
			case 6:
				g.setStatus("ready")
				return g.pump(ctx, epoch, conn, frames, rawURL, token, session, cur)
			case 5:
				cur.reset()
				return actionRefetch
			case 1:
				switch sig.code {
				case 40101, 40102:
					return actionRejected
				case 40106, 40107, 40108:
					g.clearSession(ctx, epoch)
					cur.reset()
					return actionRefetch
				case 40100, 40103:
					return actionRefetch
				default:
					if sig.code != 0 {
						return actionResumeFail
					}
				}
			case 0:
				before := cur.maxSn
				pushed := cur.Push(sig.sn, sig.d)
				if pushed.Poison {
					g.log().Warn("gateway frame skipped", "component", "gateway", "sn", sig.sn)
				}
				if cur.maxSn != before {
					g.persist(ctx, epoch, session, cur.maxSn)
				}
				for _, ev := range pushed.Events {
					if g.Host != nil {
						_ = g.Host.Dispatch(ctx, ev)
					}
				}
				if pushed.Fatal {
					g.clearSession(ctx, epoch)
					cur.reset()
					return actionRefetch
				}
			}
		}
	}
}

func (g *Gateway) writePing(conn wsConn, sn int64) error {
	payload := []byte(fmt.Sprintf(`{"s":2,"sn":%d}`, sn))
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func (g *Gateway) pingDelay() time.Duration {
	t := g.timings()
	if t.PingJitter <= 0 {
		if t.PingBase <= 0 {
			return time.Millisecond
		}
		return t.PingBase
	}
	r := g.Rand
	if r == nil {
		r = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	span := int64(t.PingJitter) * 2
	delta := time.Duration(r.Int63n(span+1)) - t.PingJitter
	d := t.PingBase + delta
	if d < time.Millisecond {
		return time.Millisecond
	}
	return d
}

func (g *Gateway) dialTwice(ctx context.Context, rawURL string) (wsConn, error) {
	conn, err := g.dial(ctx, rawURL)
	if err == nil {
		return conn, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	t := g.timings()
	if !sleepCtx(ctx, t.DialGap[0]) {
		return nil, ctx.Err()
	}
	conn, err = g.dial(ctx, rawURL)
	if err == nil {
		return conn, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !sleepCtx(ctx, t.DialGap[1]) {
		return nil, ctx.Err()
	}
	return nil, err
}

func (g *Gateway) persist(ctx context.Context, epoch int64, session string, sn int64) {
	if g.Sessions == nil || g.epoch.Load() != epoch {
		return
	}
	if err := g.Sessions.SetGateway(ctx, session, sn); err != nil {
		g.log().Warn("gateway sn", "component", "gateway", "err", err.Error())
	}
}

func (g *Gateway) clearSession(ctx context.Context, epoch int64) {
	if g.Sessions == nil || g.epoch.Load() != epoch {
		return
	}
	if err := g.Sessions.ClearGateway(ctx); err != nil {
		g.log().Warn("gateway clear", "component", "gateway", "err", err.Error())
	}
}

type signal struct {
	s       int
	sn      int64
	d       json.RawMessage
	code    int
	session string
}

func parseSignal(raw []byte) (signal, bool) {
	var frame struct {
		S  int             `json:"s"`
		SN int64           `json:"sn"`
		D  json.RawMessage `json:"d"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return signal{}, false
	}
	sig := signal{s: frame.S, sn: frame.SN, d: frame.D}
	if len(bytesTrim(frame.D)) > 0 && !bytesEqualNull(frame.D) {
		var d struct {
			Code      int    `json:"code"`
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(frame.D, &d) == nil {
			sig.code = d.Code
			sig.session = d.SessionID
		}
	}
	return sig, true
}

func bytesTrim(b []byte) []byte {
	return bytesTrimSpace(b)
}

func bytesEqualNull(b []byte) bool {
	return string(bytesTrimSpace(b)) == "null"
}

func bytesTrimSpace(b []byte) []byte {
	i := 0
	j := len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\t' || b[i] == '\r') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\t' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}

func resumeURL(raw, sessionID string, sn int64) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("bad gateway url")
	}
	q := u.Query()
	q.Set("resume", "1")
	q.Set("sn", strconv.FormatInt(sn, 10))
	q.Set("session_id", sessionID)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func gatewayHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "invalid"
	}
	if u.Scheme == "" {
		return u.Host
	}
	return u.Scheme + "://" + u.Host
}

type wsIn struct {
	kind int
	data []byte
	err  error
}

func readFrames(conn wsConn) <-chan wsIn {
	out := make(chan wsIn, 32)
	go func() {
		defer close(out)
		for {
			kind, data, err := conn.ReadMessage()
			out <- wsIn{kind: kind, data: data, err: err}
			if err != nil {
				return
			}
		}
	}()
	return out
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// HTTPClientFetch adapts Client.GatewayURL. The websocket URL is not logged here.
func HTTPClientFetch(client *Client) func(context.Context, string) (string, error) {
	return func(ctx context.Context, token string) (string, error) {
		if client == nil {
			return "", errors.New("no kook client")
		}
		return client.GatewayURL(ctx, token)
	}
}
