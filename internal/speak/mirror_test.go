package speak

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"bsdx/internal/kook"
)

func TestEscapeKMarkdownHidesMention(t *testing.T) {
	got := EscapeKMarkdown("(met)all(met) *hi* [link](http://x)")
	if strings.Contains(got, "(met)") {
		t.Fatalf("raw mention survived: %s", got)
	}
	body := KMarkdown("他说 (met)all(met)")
	if strings.Contains(body, "(met)") || !strings.Contains(body, "操作者代说") {
		t.Fatalf("body %s", body)
	}
}

func TestCardContentIsAnArray(t *testing.T) {
	raw, err := CardContent(`他说"你好"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[0] != '[' {
		t.Fatalf("not an array: %s", raw)
	}
	if !strings.Contains(raw, `\"`) {
		t.Fatalf("quote not escaped: %s", raw)
	}
	var cards []map[string]any
	if err := json.Unmarshal([]byte(raw), &cards); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("cards %d", len(cards))
	}
}

func TestMirrorSendsOperatorText(t *testing.T) {
	var got struct {
		typ     float64
		target  string
		content string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/message/create" {
			t.Errorf("path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		got.typ, _ = body["type"].(float64)
		got.target, _ = body["target_id"].(string)
		got.content, _ = body["content"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":40012,"message":"没有权限","data":null}`)
	}))
	defer srv.Close()
	st := testStore(t)
	binding, err := st.Binding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	binding.MirrorEnabled = true
	binding.TextChannelID = "21"
	binding.TextChannelName = "综合"
	binding.MirrorFormat = "kmarkdown"
	if err := st.SaveBinding(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	client := kookClient(t, srv)
	err = NewMirror(client, func() (string, bool) { return "tok", true }, st.Binding)(context.Background(), Job{Text: "请 (met)all(met)"})
	if err == nil || !strings.Contains(err.Error(), "没有权限") {
		t.Fatalf("err %v", err)
	}
	if got.typ != 9 || got.target != "21" || strings.Contains(got.content, "(met)") {
		t.Fatalf("message %+v", got)
	}

	binding.MirrorFormat = "card"
	if err := st.SaveBinding(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got.typ, _ = body["type"].(float64)
		got.content, _ = body["content"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"message":"操作成功","data":null}`)
	})
	if err := NewMirror(client, func() (string, bool) { return "tok", true }, st.Binding)(context.Background(), Job{Text: `引号"在这"`}); err != nil {
		t.Fatal(err)
	}
	if got.typ != 10 || len(got.content) == 0 || got.content[0] != '[' {
		t.Fatalf("card %+v", got)
	}
}

func kookClient(t *testing.T, srv *httptest.Server) *kook.Client {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := kook.New("https://www.kookapp.cn/api", slog.New(slog.NewJSONHandler(io.Discard, nil)))
	c.SetRoundTripper(kook.RewriteTransport(u, srv.Client().Transport))
	return c
}

func TestUIHintCopy(t *testing.T) {
	now := timeNow()
	cold := &liveJob{coldJoin: true, started: now.Add(-7e9), Job: Job{Status: "synthesizing"}}
	hint, ms := uiHint(cold, now)
	if hint != "正在加入" || ms != 6000 {
		t.Fatalf("cold %q %d", hint, ms)
	}
	hit := &liveJob{cacheHit: true, inChannel: true, started: now.Add(-2e9), Job: Job{Status: "synthesizing"}}
	hint, ms = uiHint(hit, now)
	if hint != "仍在合成" || ms != 1000 {
		t.Fatalf("hit %q %d", hint, ms)
	}
	miss := &liveJob{inChannel: true, runes: 4, started: now.Add(-4e9), Job: Job{Status: "synthesizing"}}
	hint, ms = uiHint(miss, now)
	if hint != "仍在合成" || ms != 3000 {
		t.Fatalf("miss %q %d", hint, ms)
	}
}
