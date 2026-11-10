package kook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"bsdx/internal/module"
)

type fakeMsg struct {
	kind int
	data []byte
	err  error
}

type fakeConn struct {
	mu     sync.Mutex
	cond   *sync.Cond
	inbox  []fakeMsg
	writes [][]byte
	closed bool
}

func newFakeConn() *fakeConn {
	c := &fakeConn{}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *fakeConn) push(kind int, data string) {
	c.mu.Lock()
	c.inbox = append(c.inbox, fakeMsg{kind: kind, data: []byte(data)})
	c.cond.Broadcast()
	c.mu.Unlock()
}

func (c *fakeConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.inbox) == 0 && !c.closed {
		c.cond.Wait()
	}
	if len(c.inbox) == 0 {
		return 0, nil, io.EOF
	}
	msg := c.inbox[0]
	c.inbox = c.inbox[1:]
	if msg.err != nil {
		return 0, nil, msg.err
	}
	return msg.kind, msg.data, nil
}

func (c *fakeConn) WriteMessage(kind int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), data...))
	return nil
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.cond.Broadcast()
	c.mu.Unlock()
	return nil
}

func (c *fakeConn) wrote(substr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, w := range c.writes {
		if strings.Contains(string(w), substr) {
			return true
		}
	}
	return false
}

type memSessions struct {
	mu   sync.Mutex
	sets []struct {
		id string
		sn int64
	}
	clears int
}

func (m *memSessions) SetGateway(_ context.Context, id string, sn int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sets = append(m.sets, struct {
		id string
		sn int64
	}{id, sn})
	return nil
}

func (m *memSessions) ClearGateway(ctx context.Context) error {
	m.mu.Lock()
	m.clears++
	m.mu.Unlock()
	return m.SetGateway(ctx, "", 0)
}

func (m *memSessions) last() (string, int64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sets) == 0 {
		return "", 0, false
	}
	last := m.sets[len(m.sets)-1]
	return last.id, last.sn, true
}

type recMod struct {
	mu sync.Mutex
	ev []module.Event
}

func (m *recMod) ID() string { return "rec" }
func (m *recMod) Subscriptions() []module.Subscription {
	return []module.Subscription{
		{Type: 255, ExtraType: "joined_channel"},
		{Type: 255, ExtraType: "exited_channel"},
	}
}
func (m *recMod) Handle(_ context.Context, ev module.Event) error {
	m.mu.Lock()
	m.ev = append(m.ev, ev)
	m.mu.Unlock()
	return nil
}

func (m *recMod) events() []module.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]module.Event(nil), m.ev...)
}

type gwRig struct {
	mu       sync.Mutex
	dials    []string
	conns    []*fakeConn
	fetches  int
	statuses []string
	logs     bytes.Buffer
	sessions memSessions
	gw       *Gateway
	hello    string
	mod      *recMod
	prepare  func(raw string, c *fakeConn)
}

func (r *gwRig) start(t *testing.T) {
	t.Helper()
	host := module.New()
	if r.mod == nil {
		r.mod = &recMod{}
	}
	if err := host.Register(r.mod); err != nil {
		t.Fatal(err)
	}
	r.gw = &Gateway{
		Fetch: func(context.Context, string) (string, error) {
			r.mu.Lock()
			r.fetches++
			r.mu.Unlock()
			return "wss://gw.example/socket?compress=0&token=super-secret", nil
		},
		Dial: func(_ context.Context, raw string) (wsConn, error) {
			c := newFakeConn()
			if r.prepare != nil {
				r.prepare(raw, c)
			} else if !strings.Contains(raw, "resume=1") && r.hello != "" {
				c.push(websocket.TextMessage, r.hello)
			}
			r.mu.Lock()
			r.dials = append(r.dials, raw)
			r.conns = append(r.conns, c)
			r.mu.Unlock()
			return c, nil
		},
		Token:    func() (string, bool) { return "bot-token", true },
		Sessions: &r.sessions,
		Host:     host,
		Log:      slog.New(slog.NewJSONHandler(&r.logs, nil)),
		Status: func(state string) {
			r.mu.Lock()
			r.statuses = append(r.statuses, state)
			r.mu.Unlock()
		},
		Timings: fastTimings(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.gw.epoch.Store(1)
	done := make(chan struct{})
	go func() {
		r.gw.Run(ctx, 1)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("gateway did not stop")
		}
	})
}

func (r *gwRig) dialsCopy() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.dials...)
}

func (r *gwRig) statusHas(state string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.statuses {
		if s == state {
			return true
		}
	}
	return false
}

func (r *gwRig) fetchCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fetches
}

func fastTimings() Timings {
	return Timings{
		Hello:          400 * time.Millisecond,
		Pong:           25 * time.Millisecond,
		PingBase:       20 * time.Millisecond,
		PingJitter:     0,
		DialGap:        [2]time.Duration{time.Millisecond, time.Millisecond},
		ExtraHeartbeat: [2]time.Duration{10 * time.Millisecond, 10 * time.Millisecond},
		ResumeGap:      [2]time.Duration{15 * time.Millisecond, 15 * time.Millisecond},
		FetchBase:      5 * time.Millisecond,
		FetchCap:       40 * time.Millisecond,
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

const helloFrame = `{"s":1,"d":{"code":0,"session_id":"sess-1"}}`

func TestGatewayFirstPingUsesZero(t *testing.T) {
	r := &gwRig{hello: helloFrame}
	r.sessions.sets = append(r.sessions.sets, struct {
		id string
		sn int64
	}{"old", 99})
	r.start(t)
	waitFor(t, "first ping", func() bool {
		dials := r.dialsCopy()
		if len(dials) == 0 || len(r.connsOf()) == 0 {
			return false
		}
		return r.connsOf()[0].wrote(`"sn":0`)
	})
	dials := r.dialsCopy()
	if strings.Contains(dials[0], "resume=") || strings.Contains(dials[0], "session_id=") {
		t.Fatalf("first dial is not a fresh session: %s", dials[0])
	}
	if r.connsOf()[0].wrote(`"sn":99`) {
		t.Fatal("first ping reused the stored sn")
	}
	_, sn, ok := r.sessions.last()
	if !ok || sn != 0 {
		t.Fatalf("stored sn %d ok %v", sn, ok)
	}
	text := r.logs.String()
	if strings.Contains(text, "super-secret") || strings.Contains(text, "resume=1") || strings.Contains(text, "session_id=") {
		t.Fatalf("log leaked gateway url: %s", text)
	}
	if !strings.Contains(text, "wss://gw.example") {
		t.Fatalf("log missing host: %s", text)
	}
}

func (r *gwRig) connsOf() []*fakeConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*fakeConn(nil), r.conns...)
}

func TestGatewayReorderAndDuplicate(t *testing.T) {
	mod := &recMod{}
	r := &gwRig{hello: helloFrame, mod: mod}
	r.prepare = func(raw string, c *fakeConn) {
		if strings.Contains(raw, "resume=1") {
			return
		}
		c.push(websocket.TextMessage, helloFrame)
		c.push(websocket.TextMessage, frame(2, 255, `"exited_channel"`))
		c.push(websocket.TextMessage, frame(1, 255, `"joined_channel"`))
		c.push(websocket.TextMessage, frame(1, 255, `"joined_channel"`))
	}
	r.start(t)
	waitFor(t, "two events", func() bool { return len(mod.events()) >= 2 })
	ev := mod.events()
	if ev[0].SN != 1 || ev[1].SN != 2 {
		t.Fatalf("order %+v", ev)
	}
	if ev[0].ExtraType != "joined_channel" {
		t.Fatalf("first %+v", ev[0])
	}
	time.Sleep(40 * time.Millisecond)
	if len(mod.events()) != 2 {
		t.Fatalf("duplicate delivered: %+v", mod.events())
	}
	r.sessions.mu.Lock()
	defer r.sessions.mu.Unlock()
	saw2 := false
	for _, set := range r.sessions.sets {
		if set.sn == 2 {
			saw2 = true
		}
		if saw2 && set.sn == 1 {
			t.Fatalf("duplicate moved the cursor backwards: %+v", r.sessions.sets)
		}
	}
	if !saw2 {
		t.Fatalf("sn never reached 2: %+v", r.sessions.sets)
	}
}

func TestGatewayUnsubscribedNumericExtra(t *testing.T) {
	r := &gwRig{}
	r.prepare = func(raw string, c *fakeConn) {
		if strings.Contains(raw, "resume=1") {
			return
		}
		c.push(websocket.TextMessage, helloFrame)
		c.push(websocket.TextMessage, frame(1, 1, `1`))
		c.push(websocket.TextMessage, frame(2, 9, `1`))
	}
	r.start(t)
	waitFor(t, "both numeric frames", func() bool {
		_, sn, ok := r.sessions.last()
		return ok && sn >= 2
	})
	if r.gw.Host.Unsubscribed() != 2 {
		t.Fatalf("unsubscribed %d", r.gw.Host.Unsubscribed())
	}
	if strings.Contains(r.logs.String(), "gateway frame skipped") {
		t.Fatalf("numeric extra was treated as poison: %s", r.logs.String())
	}
}

func TestGatewayEightPoisonsRefetch(t *testing.T) {
	r := &gwRig{}
	r.prepare = func(raw string, c *fakeConn) {
		if strings.Contains(raw, "resume=1") || r.fetchCount() > 1 {
			return
		}
		c.push(websocket.TextMessage, helloFrame)
		for sn := 1; sn <= 8; sn++ {
			c.push(websocket.TextMessage, fmt.Sprintf(`{"s":0,"sn":%d,"d":{"type":1}}`, sn))
		}
	}
	r.start(t)
	waitFor(t, "refetch after poison streak", func() bool { return r.fetchCount() >= 2 })
	r.sessions.mu.Lock()
	clears := r.sessions.clears
	r.sessions.mu.Unlock()
	if clears < 1 {
		t.Fatal("session was not cleared")
	}
}

func TestGatewayHelloCodes(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		rejected bool
		refetch  bool
	}{
		{name: "40101", code: 40101, rejected: true},
		{name: "40102", code: 40102, rejected: true},
		{name: "40100", code: 40100, refetch: true},
		{name: "40103", code: 40103, refetch: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &gwRig{}
			frame := fmt.Sprintf(`{"s":1,"d":{"code":%d}}`, tc.code)
			r.prepare = func(raw string, c *fakeConn) {
				if len(r.dialsCopy()) == 0 {
					c.push(websocket.TextMessage, frame)
				}
			}
			r.start(t)
			if tc.rejected {
				waitFor(t, "token_rejected", func() bool { return r.statusHas("token_rejected") })
				time.Sleep(40 * time.Millisecond)
				if r.fetchCount() != 1 || len(r.dialsCopy()) != 1 {
					t.Fatalf("kept retrying fetches %d dials %d", r.fetchCount(), len(r.dialsCopy()))
				}
				return
			}
			waitFor(t, "refetch", func() bool { return r.fetchCount() >= 2 })
			if r.statusHas("token_rejected") {
				t.Fatal("40103/40100 marked token_rejected")
			}
		})
	}
}

func TestGatewaySignalRefetch(t *testing.T) {
	t.Run("binary", func(t *testing.T) {
		r := &gwRig{}
		r.prepare = func(raw string, c *fakeConn) {
			if len(r.dialsCopy()) == 0 {
				c.push(websocket.BinaryMessage, "compressed")
			}
		}
		r.start(t)
		waitFor(t, "refetch", func() bool { return r.fetchCount() >= 2 })
		if r.statusHas("token_rejected") {
			t.Fatal("binary marked token_rejected")
		}
	})
	t.Run("reconnect", func(t *testing.T) {
		r := &gwRig{hello: helloFrame}
		r.start(t)
		waitFor(t, "ready", func() bool { return r.statusHas("ready") && len(r.connsOf()) > 0 })
		r.connsOf()[0].push(websocket.TextMessage, `{"s":5}`)
		waitFor(t, "second dial", func() bool { return len(r.dialsCopy()) >= 2 })
		if strings.Contains(r.dialsCopy()[len(r.dialsCopy())-1], "resume=") {
			t.Fatalf("s=5 resumed: %v", r.dialsCopy())
		}
	})
}

func TestGatewayResumeLadder(t *testing.T) {
	r := &gwRig{hello: helloFrame}
	r.start(t)
	waitFor(t, "two resumes", func() bool {
		n := 0
		for _, d := range r.dialsCopy() {
			if strings.Contains(d, "resume=1") {
				n++
			}
		}
		return n >= 2
	})
	var resumes []string
	for _, d := range r.dialsCopy() {
		if strings.Contains(d, "resume=1") {
			resumes = append(resumes, d)
		}
	}
	for _, d := range resumes {
		if !strings.Contains(d, "session_id=sess-1") || !strings.Contains(d, "sn=0") {
			t.Fatalf("resume url %s", d)
		}
	}
	if strings.Contains(r.logs.String(), "session_id=") || strings.Contains(r.logs.String(), "super-secret") {
		t.Fatalf("log leaked resume url: %s", r.logs.String())
	}
	waitFor(t, "fresh dial after resume", func() bool {
		dials := r.dialsCopy()
		if len(dials) < 4 {
			return false
		}
		return !strings.Contains(dials[len(dials)-1], "resume=")
	})
	r.sessions.mu.Lock()
	clears := r.sessions.clears
	r.sessions.mu.Unlock()
	if clears < 1 {
		t.Fatal("failed resume did not clear the session")
	}
}

func frame(sn, typ int, extra string) string {
	return fmt.Sprintf(`{"s":0,"sn":%d,"d":{"type":%d,"target_id":"20","extra":{"type":%s,"body":{"user_id":"9","channel_id":"20"}}}}`, sn, typ, extra)
}
