package speak

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bsdx/internal/module"
	"bsdx/internal/store"
	"bsdx/internal/voice"
)

func TestCleanLimits(t *testing.T) {
	if _, err := Clean("  \n", textLimit); err == nil || err.Code != "empty_text" {
		t.Fatalf("%v", err)
	}
	if _, err := Clean(strings.Repeat("字", 201), textLimit); err == nil || err.Code != "text_too_long" || err.Status != 422 {
		t.Fatalf("%v", err)
	}
	if _, err := Clean(strings.Repeat("字", 81), previewMax); err == nil || err.Message != "试听最多 80 个字" {
		t.Fatalf("%v", err)
	}
	got, err := Clean("第一行\n第二行", textLimit)
	if err != nil || got != "第一行 第二行" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestSubmitQueueAndChannel(t *testing.T) {
	st := testStore(t)
	pinVoice(t, st, "20")
	room := &stubRoom{}
	svc := New(st, nil, room, "edge-tts", t.TempDir(), false, nil)
	var once sync.Once
	started := make(chan struct{})
	svc.SetEdgeRunner(func(ctx context.Context, _ string, _ []string) error {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return ctx.Err()
	})
	if _, err := svc.Submit(context.Background(), SpeakRequest{Text: strings.Repeat("字", 201)}); err == nil {
		t.Fatal("accepted a long sentence")
	}
	first, err := svc.Submit(context.Background(), SpeakRequest{Text: "第一句", VoiceChannelID: "999"})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "worker", func() bool {
		snap := svc.Snapshot(context.Background())
		return snap.Current != nil && snap.Current.ID == first.ID
	})
	for i := 0; i < 7; i++ {
		if _, err := svc.Submit(context.Background(), SpeakRequest{Text: "排队"}); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	_, err = svc.Submit(context.Background(), SpeakRequest{Text: "溢出"})
	var rej *Reject
	if !errors.As(err, &rej) || rej.Code != "queue_full" || rej.Status != 429 {
		t.Fatalf("%v", err)
	}
	if err := svc.Stop(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "stopped", func() bool {
		rows, err := st.ListHistory(context.Background(), 20)
		if err != nil || len(rows) < 8 {
			return false
		}
		for _, row := range rows {
			if row.Status != "skipped" {
				return false
			}
		}
		return true
	})
}

func TestSpeakIgnoresClientChannelAndMirrorFailureStaysDone(t *testing.T) {
	st := testStore(t)
	pinVoice(t, st, "20")
	room := &stubRoom{}
	dir := t.TempDir()
	svc := New(st, nil, room, "edge-tts", dir, false, nil)
	svc.SetEdgeRunner(writeClip)
	svc.SetDecode(func(context.Context, string, float64) ([]byte, error) {
		return make([]byte, 19200), nil
	})
	svc.SetMirror(func(context.Context, Job) error { return errors.New("没有权限") })
	job, err := svc.Submit(context.Background(), SpeakRequest{Text: "你好", VoiceChannelID: "999"})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "done", func() bool {
		rows, err := st.ListHistory(context.Background(), 5)
		if err != nil {
			return false
		}
		for _, row := range rows {
			if row.ID == job.ID && row.Status == "done" && strings.Contains(row.Error, "没有权限") {
				return true
			}
		}
		return false
	})
	room.mu.Lock()
	joined := append([]string(nil), room.joined...)
	room.mu.Unlock()
	if len(joined) != 1 || joined[0] != "20" {
		t.Fatalf("joined %v", joined)
	}
}

func TestPreviewDoesNotJoinOrRecord(t *testing.T) {
	st := testStore(t)
	room := &stubRoom{}
	svc := New(st, nil, room, "edge-tts", t.TempDir(), false, nil)
	svc.SetEdgeRunner(writeClip)
	audio, err := svc.Preview(context.Background(), "试听", "", "edge", 1)
	if err != nil || len(audio) < 256 {
		t.Fatalf("audio %d err %v", len(audio), err)
	}
	rows, err := st.ListHistory(context.Background(), 20)
	if err != nil || len(rows) != 0 {
		t.Fatalf("history %v %v", rows, err)
	}
	room.mu.Lock()
	defer room.mu.Unlock()
	if len(room.joined) != 0 || room.played != 0 {
		t.Fatalf("preview touched the room %+v", room)
	}
}

func TestSkippedQueueIsNotMirrored(t *testing.T) {
	st := testStore(t)
	pinVoice(t, st, "20")
	var mu sync.Mutex
	var mirrored []string
	svc := New(st, nil, &stubRoom{}, "edge-tts", t.TempDir(), false, nil)
	block := make(chan struct{})
	svc.SetEdgeRunner(func(ctx context.Context, bin string, args []string) error {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
		return writeClip(ctx, bin, args)
	})
	svc.SetMirror(func(_ context.Context, job Job) error {
		mu.Lock()
		mirrored = append(mirrored, job.Text)
		mu.Unlock()
		return nil
	})
	if _, err := svc.Submit(context.Background(), SpeakRequest{Text: "先说"}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "current", func() bool {
		return svc.Snapshot(context.Background()).Current != nil
	})
	if _, err := svc.Submit(context.Background(), SpeakRequest{Text: "后说"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Stop(); err != nil {
		t.Fatal(err)
	}
	close(block)
	waitUntil(t, "both skipped", func() bool {
		rows, err := st.ListHistory(context.Background(), 10)
		if err != nil || len(rows) != 2 {
			return false
		}
		for _, row := range rows {
			if row.Status != "skipped" {
				return false
			}
		}
		return true
	})
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(mirrored) != 0 {
		t.Fatalf("mirrored %v", mirrored)
	}
}

func TestModulePresence(t *testing.T) {
	var got []string
	m := Module{Note: func(channel, user string, joined bool) {
		got = append(got, channel+"/"+user)
		if !joined {
			t.Errorf("joined %v", joined)
		}
	}}
	if m.ID() != "speak" || len(m.Subscriptions()) != 2 {
		t.Fatalf("subs %+v", m.Subscriptions())
	}
	err := m.Handle(context.Background(), module.Event{
		Type: 255, ExtraType: "joined_channel", ChannelID: "20", AuthorID: "9",
	})
	if err != nil || len(got) != 1 || got[0] != "20/9" {
		t.Fatalf("err %v got %v", err, got)
	}
}

type stubRoom struct {
	mu     sync.Mutex
	joined []string
	played int
}

func (s *stubRoom) EnsureJoined(_ context.Context, channelID, _ string) error {
	s.mu.Lock()
	s.joined = append(s.joined, channelID)
	s.mu.Unlock()
	return nil
}

func (s *stubRoom) Play(_ context.Context, _ []byte, onStart func()) error {
	if onStart != nil {
		onStart()
	}
	s.mu.Lock()
	s.played++
	s.mu.Unlock()
	return nil
}

func (s *stubRoom) Skip()                       {}
func (s *stubRoom) Stop()                       {}
func (s *stubRoom) Leave(context.Context) error { return nil }
func (s *stubRoom) State() string               { return voice.StateIdle }
func (s *stubRoom) ChannelID() string           { return "" }
func (s *stubRoom) LastError() string           { return "" }
func (s *stubRoom) Roster(context.Context) ([]voice.Person, error) {
	return nil, nil
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kookbot.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func pinVoice(t *testing.T, st *store.Store, id string) {
	t.Helper()
	b, err := st.Binding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b.GuildID = "1"
	b.GuildName = "服务器甲"
	b.VoiceChannelID = id
	b.VoiceChannelName = "来自视图"
	if err := st.SaveBinding(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}

func writeClip(_ context.Context, _ string, args []string) error {
	var path string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--write-media" {
			path = args[i+1]
		}
	}
	if path == "" {
		return errors.New("no media path")
	}
	return os.WriteFile(path, make([]byte, 300), 0o600)
}

func waitUntil(t *testing.T, what string, cond func() bool) {
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

func timeNow() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
