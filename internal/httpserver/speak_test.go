package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bsdx/internal/config"
	"bsdx/internal/kook"
	"bsdx/internal/logx"
	"bsdx/internal/secret"
	"bsdx/internal/speak"
	"bsdx/internal/store"
	"bsdx/internal/voice"
)

type recordRoom struct {
	mu     sync.Mutex
	joined []string
	played int
}

func (r *recordRoom) EnsureJoined(_ context.Context, channelID, _ string) error {
	r.mu.Lock()
	r.joined = append(r.joined, channelID)
	r.mu.Unlock()
	return nil
}

func (r *recordRoom) Play(_ context.Context, _ []byte, onStart func()) error {
	if onStart != nil {
		onStart()
	}
	r.mu.Lock()
	r.played++
	r.mu.Unlock()
	return nil
}

func (r *recordRoom) Skip()                       {}
func (r *recordRoom) Stop()                       {}
func (r *recordRoom) Leave(context.Context) error { return nil }
func (r *recordRoom) State() string               { return voice.StateIdle }
func (r *recordRoom) ChannelID() string           { return "" }
func (r *recordRoom) LastError() string           { return "" }
func (r *recordRoom) Roster(context.Context) ([]voice.Person, error) {
	return nil, nil
}

func TestSpeakHTTPIgnoresClientChannel(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kookbot.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	binding, err := st.Binding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	binding.GuildID = "1"
	binding.VoiceChannelID = "20"
	binding.VoiceChannelName = "来自视图"
	if err := st.SaveBinding(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	box, err := secret.New([]byte(panelSecret))
	if err != nil {
		t.Fatal(err)
	}
	creds, err := LoadCredentials(context.Background(), st, box)
	if err != nil {
		t.Fatal(err)
	}
	room := &recordRoom{}
	logger := logx.New(httptest.NewRecorder(), 0)
	svc := speak.New(st, box, room, "edge-tts", dir, false, logger)
	svc.SetEdgeRunner(func(_ context.Context, _ string, args []string) error {
		path := ""
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "--write-media" {
				path = args[i+1]
			}
		}
		return writeFile(path)
	})
	svc.SetDecode(func(context.Context, string, float64) ([]byte, error) {
		return make([]byte, 300), nil
	})
	web, err := New(Deps{
		Config: config.Config{UIPassword: "pw", Secret: panelSecret, HTTPAddr: "127.0.0.1:8765"},
		Store:  st,
		Log:    logger,
		Box:    box,
		Kook:   kook.New("https://www.kookapp.cn/api", logger),
		Creds:  creds,
		Speak:  svc,
	}, []MountFunc{MountSpeak, MountTTS})
	if err != nil {
		t.Fatal(err)
	}
	session, csrf := loginPanel(t, web)
	rec := call(t, web, http.MethodPost, "/api/speak", `{"text":"你好","voice_channel_id":"999"}`, session, csrf, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("speak %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		room.mu.Lock()
		joined := append([]string(nil), room.joined...)
		room.mu.Unlock()
		if len(joined) > 0 {
			if len(joined) != 1 || joined[0] != "20" {
				t.Fatalf("joined %v", joined)
			}
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("did not join the pinned channel")
		}
		time.Sleep(2 * time.Millisecond)
	}

	before, err := st.ListHistory(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	rec = call(t, web, http.MethodPost, "/api/tts/preview", `{"text":"试听一句"}`, session, csrf, true)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "audio/mpeg" || rec.Body.Len() < 256 {
		t.Fatalf("preview %d %s %d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	after, err := st.ListHistory(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("preview wrote history %d -> %d", len(before), len(after))
	}
	room.mu.Lock()
	played := room.played
	joined := len(room.joined)
	room.mu.Unlock()
	if played != 1 || joined != 1 {
		t.Fatalf("preview changed the room played %d joined %d", played, joined)
	}
}

func writeFile(path string) error {
	if path == "" {
		return os.ErrNotExist
	}
	return os.WriteFile(path, make([]byte, 300), 0o600)
}
