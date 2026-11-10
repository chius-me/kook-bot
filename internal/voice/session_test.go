package voice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

const joinBody = `{"ip":"203.0.113.8","port":"1000","rtcp_port":"10001","rtcp_mux":true,"bitrate":48000,"audio_ssrc":"1111","audio_pt":"111"}`

type fakeProc struct {
	mu     sync.Mutex
	alive  bool
	closed bool
	done   chan struct{}
	once   sync.Once
	writes int
}

func newFakeProc() *fakeProc {
	return &fakeProc{alive: true, done: make(chan struct{})}
}

func (p *fakeProc) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	p.writes++
	return len(b), nil
}

func (p *fakeProc) CloseStdin() error {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.alive = false
		p.mu.Unlock()
		close(p.done)
	})
	return nil
}

func (p *fakeProc) Kill() error { return p.CloseStdin() }

func (p *fakeProc) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.alive
}

func (p *fakeProc) Done() <-chan struct{} { return p.done }
func (p *fakeProc) Err() error            { return nil }

type voiceRig struct {
	mu      sync.Mutex
	keeps   int
	leaves  []string
	joins   int
	keepErr error
}

func (h *voiceRig) session() *Session {
	s := New(Hooks{
		Join: func(context.Context, string, string) (json.RawMessage, error) {
			h.mu.Lock()
			h.joins++
			h.mu.Unlock()
			return json.RawMessage(joinBody), nil
		},
		Leave: func(_ context.Context, id string) error {
			h.mu.Lock()
			h.leaves = append(h.leaves, id)
			h.mu.Unlock()
			return nil
		},
		KeepAlive: func(context.Context, string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.keeps++
			return h.keepErr
		},
		StartRTP: func(context.Context, JoinInfo) (Process, error) {
			return newFakeProc(), nil
		},
	})
	s.SetKeepEvery(20 * time.Millisecond)
	s.SetKeepRetry(5 * time.Millisecond)
	s.SetCooldown(0)
	return s
}

func (h *voiceRig) keepCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.keeps
}

func (h *voiceRig) leaveIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.leaves...)
}

func TestPaceWritesTenChunks(t *testing.T) {
	clock := newFakeClock()
	var mu sync.Mutex
	var writes []int
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = pace(ctx, clock, func(b []byte) error {
			mu.Lock()
			writes = append(writes, len(b))
			mu.Unlock()
			return nil
		}, func() ([]byte, error) { return silenceChunk(), nil })
	}()
	waitN := func(n int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			got := len(writes)
			mu.Unlock()
			if got >= n {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("writes stayed below %d", n)
	}
	waitN(1)
	waitWaiter(t, clock)
	before := writeCount(&mu, &writes)
	clock.Advance(time.Second)
	time.Sleep(30 * time.Millisecond)
	if writeCount(&mu, &writes)-before > 3 {
		t.Fatalf("burst %d", writeCount(&mu, &writes)-before)
	}
	for writeCount(&mu, &writes) < 10 {
		waitWaiter(t, clock)
		clock.Advance(100 * time.Millisecond)
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(writes) < 10 {
		t.Fatalf("writes %d", len(writes))
	}
	for _, n := range writes[:10] {
		if n != chunkBytes {
			t.Fatalf("chunk %d", n)
		}
	}
}

func writeCount(mu *sync.Mutex, writes *[]int) int {
	mu.Lock()
	defer mu.Unlock()
	return len(*writes)
}

func waitWaiter(t *testing.T, clock *fakeClock) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if clock.Waiters() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no sleeper")
}

func TestLeaveStopsKeepAlive(t *testing.T) {
	h := &voiceRig{}
	s := h.session()
	ctx := context.Background()
	if err := s.EnsureJoined(ctx, "20", ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for h.keepCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if h.keepCount() < 1 {
		t.Fatal("no keep-alive")
	}
	if err := s.Leave(ctx, "operator"); err != nil {
		t.Fatal(err)
	}
	n := h.keepCount()
	time.Sleep(60 * time.Millisecond)
	if h.keepCount() != n {
		t.Fatalf("keep-alive after leave %d -> %d", n, h.keepCount())
	}
	if s.State() != StateIdle {
		t.Fatalf("state %s", s.State())
	}
}

func TestKeepAliveFailureDoesNotDeadlock(t *testing.T) {
	h := &voiceRig{keepErr: errors.New("down")}
	s := h.session()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx := context.Background()
		if err := s.EnsureJoined(ctx, "20", ""); err != nil {
			t.Errorf("join %v", err)
			return
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if s.State() == StateError && len(h.leaveIDs()) >= 1 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Errorf("state %s leaves %v", s.State(), h.leaveIDs())
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("keep-alive leave deadlocked")
	}
	if got := h.leaveIDs(); len(got) != 1 || got[0] != "20" {
		t.Fatalf("leaves %v", got)
	}
}

func TestEnsureJoinedWaitsOutLeave(t *testing.T) {
	h := &voiceRig{}
	s := h.session()
	ctx := context.Background()
	if err := s.EnsureJoined(ctx, "20", ""); err != nil {
		t.Fatal(err)
	}
	held := make(chan struct{})
	release := make(chan struct{})
	s.afterStop = func() {
		close(held)
		<-release
	}
	leaveDone := make(chan error, 1)
	go func() { leaveDone <- s.Leave(ctx, "operator") }()
	select {
	case <-held:
	case <-time.After(2 * time.Second):
		t.Fatal("leave did not reach afterStop")
	}
	joined := make(chan error, 1)
	go func() { joined <- s.EnsureJoined(ctx, "20", "") }()
	time.Sleep(40 * time.Millisecond)
	select {
	case err := <-joined:
		t.Fatalf("EnsureJoined returned while leaving: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-leaveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("leave hung")
	}
	select {
	case err := <-joined:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rejoin hung")
	}
	if s.State() != StateReady && s.State() != StatePlaying {
		t.Fatalf("state %s", s.State())
	}
	if s.ChannelID() != "20" {
		t.Fatalf("channel %s", s.ChannelID())
	}
	if got := h.leaveIDs(); len(got) != 1 || got[0] != "20" {
		t.Fatalf("leaves %v", got)
	}
}

func TestLeaveSkipsHTTPWhenGenerationMoves(t *testing.T) {
	h := &voiceRig{}
	s := h.session()
	ctx := context.Background()
	if err := s.EnsureJoined(ctx, "20", ""); err != nil {
		t.Fatal(err)
	}
	s.afterStop = func() {
		s.mu.Lock()
		s.gen++
		s.channelID = "77"
		s.state = StateReady
		s.mu.Unlock()
	}
	if err := s.Leave(ctx, "operator"); err != nil {
		t.Fatal(err)
	}
	if len(h.leaveIDs()) != 0 {
		t.Fatalf("leave http %v", h.leaveIDs())
	}
	if s.ChannelID() != "77" {
		t.Fatalf("channel %s", s.ChannelID())
	}
}

func TestBootLeavesListedRoomsOnly(t *testing.T) {
	var left []string
	var joined int
	s := New(Hooks{
		List: func(context.Context) ([]string, error) { return []string{"20", "23"}, nil },
		Leave: func(_ context.Context, id string) error {
			left = append(left, id)
			return nil
		},
		Join: func(context.Context, string, string) (json.RawMessage, error) {
			joined++
			return nil, errors.New("should not join")
		},
	})
	s.SetCooldown(0)
	s.Boot(context.Background())
	if len(left) != 2 || left[0] != "20" || left[1] != "23" || joined != 0 {
		t.Fatalf("left %v joined %d", left, joined)
	}
	if s.State() != StateIdle {
		t.Fatalf("state %s", s.State())
	}

	s = New(Hooks{
		List:  func(context.Context) ([]string, error) { return nil, errors.New("down") },
		Leave: func(context.Context, string) error { t.Fatal("left on list error"); return nil },
	})
	s.SetCooldown(0)
	s.Boot(context.Background())
}

func TestIdleStep(t *testing.T) {
	if next, leave := idleStep(false, true, 0, 0, 30*time.Second, idleLimit); next != 0 || leave {
		t.Fatalf("list fail %s %v", next, leave)
	}
	if next, leave := idleStep(true, false, 0, time.Minute, 30*time.Second, idleLimit); next != 0 || leave {
		t.Fatalf("unknown self %s %v", next, leave)
	}
	if next, leave := idleStep(true, true, 1, time.Minute, 30*time.Second, idleLimit); next != 0 || leave {
		t.Fatalf("others %s %v", next, leave)
	}
	next, leave := idleStep(true, true, 0, 570*time.Second, 30*time.Second, idleLimit)
	if next != 600*time.Second || !leave {
		t.Fatalf("limit %s %v", next, leave)
	}
	if n := countOthers([]Person{{ID: "42"}, {ID: "7"}}, "42"); n != 1 {
		t.Fatalf("others %d", n)
	}
	if n := countOthers([]Person{{ID: "42"}}, "42"); n != 0 {
		t.Fatalf("self only %d", n)
	}
}
