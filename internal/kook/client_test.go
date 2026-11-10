package kook

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestEndpointWhitelist(t *testing.T) {
	okBases := []string{
		"https://www.kookapp.cn/api",
		"https://www.kookapp.cn/api/",
		"https://www.kookapp.cn/api/v3",
		"https://www.kookapp.cn/api/v3/",
	}
	for _, base := range okBases {
		c := New(base, nil)
		got, err := c.endpoint("user/me")
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		if got != "https://www.kookapp.cn/api/v3/user/me" {
			t.Fatalf("%s -> %s", base, got)
		}
	}
	bad := []string{
		"https://user:pass@www.kookapp.cn/api/v3/user/me",
		"https://evil.example/api/v3/user/me",
		"https://www.kookapp.cn:443/api",
		"http://www.kookapp.cn/api",
		"https://www.kookapp.cn.evil/api",
		"https://user:pass@www.kookapp.cn/api",
	}
	for _, base := range bad {
		c := New(base, nil)
		if _, err := c.endpoint("user/me"); err == nil || !strings.Contains(err.Error(), "bad endpoint") {
			t.Fatalf("%s: err=%v", base, err)
		}
	}
	c := New("https://www.kookapp.cn/api", nil)
	if c.http.Timeout != requestTimeout {
		t.Fatalf("timeout %s", c.http.Timeout)
	}
}

func TestMeRewritesHostAndDoesNotFollowRedirect(t *testing.T) {
	const token = "super-secret-token"
	var logs bytes.Buffer
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		if r.URL.Path != "/api/v3/user/me" {
			t.Errorf("followed redirect to %s", r.URL.Path)
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Header.Get("Authorization") != "Bot "+token {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept-Language") != "zh-CN" {
			t.Errorf("language %q", r.Header.Get("Accept-Language"))
		}
		w.Header().Set("Location", "/secret")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, &logs)
	_, err := c.Me(context.Background(), token)
	if err == nil {
		t.Fatal("expected redirect failure")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked token: %v", err)
	}
	if len(hits) != 1 || hits[0] != "/api/v3/user/me" {
		t.Fatalf("hits %v", hits)
	}
	if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "Authorization") {
		t.Fatalf("log leaked: %s", logs.String())
	}
}

func TestMeSuccess(t *testing.T) {
	const token = "super-secret-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeKOOK(w, 0, "操作成功", User{ID: "9", Username: "小明", IdentifyNum: "0001", Avatar: "https://img.example/a.png"})
	}))
	defer srv.Close()
	c := newTestClient(t, srv, nil)
	user, err := c.Me(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != "9" || user.Username != "小明" || user.IdentifyNum != "0001" {
		t.Fatalf("%+v", user)
	}
	if _, err := c.Me(context.Background(), "  "); err == nil {
		t.Fatal("empty token made a request")
	}
}

func TestBusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeKOOK(w, 40001, "无效的 token", nil)
	}))
	defer srv.Close()
	c := newTestClient(t, srv, nil)
	_, err := c.Me(context.Background(), "tok")
	api, ok := err.(*APIError)
	if !ok || api.Unavailable || api.Code != 40001 || api.Message != "无效的 token" {
		t.Fatalf("%v", err)
	}
}

func TestGuildPagesAndLogPath(t *testing.T) {
	const token = "guild-token-should-stay-out-of-logs"
	var logs bytes.Buffer
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" && strings.Contains(r.URL.Path, "user") {
			t.Errorf("unexpected %s", r.URL.RequestURI())
		}
		pages = append(pages, r.URL.Query().Get("page"))
		if r.URL.Query().Get("page_size") != "50" {
			t.Errorf("page_size %s", r.URL.Query().Get("page_size"))
		}
		page := r.URL.Query().Get("page")
		switch page {
		case "1":
			writeKOOK(w, 0, "操作成功", map[string]any{
				"items": []Guild{{ID: "1", Name: "甲", Icon: "i"}},
				"meta":  map[string]int{"page": 1, "page_total": 2, "page_size": 50, "total": 2},
			})
		case "2":
			writeKOOK(w, 0, "操作成功", map[string]any{
				"items": []Guild{{ID: "2", Name: "乙"}},
				"meta":  map[string]int{"page": 2, "page_total": 2, "page_size": 50, "total": 2},
			})
		default:
			t.Errorf("extra page %s", page)
			writeKOOK(w, 0, "操作成功", map[string]any{
				"items": []Guild{},
				"meta":  map[string]int{"page": 3, "page_total": 2},
			})
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv, &logs)
	guilds, err := c.Guilds(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if len(guilds) != 2 || guilds[0].Name != "甲" || guilds[1].ID != "2" {
		t.Fatalf("%+v", guilds)
	}
	if strings.Join(pages, ",") != "1,2" {
		t.Fatalf("pages %v", pages)
	}
	if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "page=") {
		t.Fatalf("log %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"/api/v3/guild/list"`) && !strings.Contains(logs.String(), `/api/v3/guild/list`) {
		t.Fatalf("path missing: %s", logs.String())
	}
}

func TestPageTotalZeroAndCap(t *testing.T) {
	t.Run("short page stops", func(t *testing.T) {
		var n int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n++
			writeKOOK(w, 0, "操作成功", map[string]any{
				"items": []Guild{{ID: "1", Name: "甲"}},
				"meta":  map[string]int{"page": 1, "page_total": 0, "page_size": 50, "total": 0},
			})
		}))
		defer srv.Close()
		c := newTestClient(t, srv, nil)
		guilds, err := c.Guilds(context.Background(), "tok")
		if err != nil || len(guilds) != 1 || n != 1 {
			t.Fatalf("n=%d guilds=%d err=%v", n, len(guilds), err)
		}
	})
	t.Run("full pages stop at cap", func(t *testing.T) {
		var n int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n++
			items := make([]Guild, pageSize)
			for i := range items {
				items[i] = Guild{ID: "1", Name: "甲"}
			}
			writeKOOK(w, 0, "操作成功", map[string]any{
				"items": items,
				"meta":  map[string]int{"page": n, "page_total": 100, "page_size": pageSize, "total": 5000},
			})
		}))
		defer srv.Close()
		c := newTestClient(t, srv, nil)
		guilds, err := c.Guilds(context.Background(), "tok")
		if err != nil {
			t.Fatal(err)
		}
		if n != maxListPages || len(guilds) != pageSize*maxListPages {
			t.Fatalf("requests %d guilds %d", n, len(guilds))
		}
	})
}

func TestChannelsParentAndPassword(t *testing.T) {
	views := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/channel/list":
			kind := r.URL.Query().Get("type")
			if r.URL.Query().Get("guild_id") != "7" {
				t.Errorf("guild %s", r.URL.Query().Get("guild_id"))
			}
			if kind == "1" {
				writeKOOK(w, 0, "操作成功", map[string]any{
					"items": []map[string]any{
						{"id": "11", "name": "综合", "type": 1, "guild_id": "7", "parent_id": "9", "has_password": false},
					},
					"meta": map[string]int{"page": 1, "page_total": 1, "page_size": 50, "total": 1},
				})
				return
			}
			writeKOOK(w, 0, "操作成功", map[string]any{
				"items": []map[string]any{
					{"id": "21", "name": "大厅", "type": 2, "guild_id": "7", "parent_id": "9", "has_password": true},
					{"id": "22", "name": "小队", "type": 2, "guild_id": "7", "parent_id": "9"},
				},
				"meta": map[string]int{"page": 1, "page_total": 1, "page_size": 50, "total": 2},
			})
		case "/api/v3/channel/view":
			id := r.URL.Query().Get("target_id")
			views[id]++
			switch id {
			case "9":
				writeKOOK(w, 0, "操作成功", map[string]any{
					"id": "9", "name": "分组甲", "type": 0, "guild_id": "7", "is_category": true,
				})
			case "22":
				hp := true
				writeKOOK(w, 0, "操作成功", map[string]any{
					"id": "22", "name": "小队", "type": 2, "guild_id": "7", "parent_id": "9", "has_password": hp,
				})
			default:
				t.Errorf("unexpected view %s", id)
				writeKOOK(w, 40000, "没有", nil)
			}
		default:
			t.Errorf("path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv, nil)
	groups, err := c.Channels(context.Background(), "tok", "7")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups.Text) != 1 || groups.Text[0].ParentName != "分组甲" || groups.Text[0].HasPassword {
		t.Fatalf("text %+v", groups.Text)
	}
	if len(groups.Voice) != 2 {
		t.Fatalf("voice %+v", groups.Voice)
	}
	if !groups.Voice[0].HasPassword || groups.Voice[0].ParentName != "分组甲" {
		t.Fatalf("voice0 %+v", groups.Voice[0])
	}
	if !groups.Voice[1].HasPassword || groups.Voice[1].Name != "小队" {
		t.Fatalf("voice1 %+v", groups.Voice[1])
	}
	if views["9"] != 1 || views["22"] != 1 || views["21"] != 0 || views["11"] != 0 {
		t.Fatalf("views %+v", views)
	}
}

func TestRateLimitRetry(t *testing.T) {
	t.Run("one second then success", func(t *testing.T) {
		var n int
		var slept []time.Duration
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n++
			if n == 1 {
				w.Header().Set("X-Rate-Limit-Reset", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"code":429,"message":"慢一点"}`))
				return
			}
			writeKOOK(w, 0, "操作成功", User{ID: "1", Username: "小明", IdentifyNum: "1"})
		}))
		defer srv.Close()
		c := newTestClient(t, srv, nil)
		c.sleep = func(ctx context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		}
		user, err := c.Me(context.Background(), "tok")
		if err != nil || user.ID != "1" || n != 2 || len(slept) != 1 || slept[0] != time.Second {
			t.Fatalf("n=%d slept=%v err=%v user=%+v", n, slept, err, user)
		}
	})
	t.Run("thirty seconds is not slept", func(t *testing.T) {
		var n int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n++
			w.Header().Set("X-Rate-Limit-Reset", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":429,"message":"慢一点"}`))
		}))
		defer srv.Close()
		c := newTestClient(t, srv, nil)
		c.sleep = func(ctx context.Context, d time.Duration) error {
			t.Errorf("slept %s", d)
			return nil
		}
		_, err := c.Me(context.Background(), "tok")
		api, ok := err.(*APIError)
		if n != 1 || !ok || api.Code != 429 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	})
	t.Run("missing header does not retry", func(t *testing.T) {
		var n int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n++
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":429,"message":"慢一点"}`))
		}))
		defer srv.Close()
		c := newTestClient(t, srv, nil)
		c.sleep = func(ctx context.Context, d time.Duration) error {
			t.Errorf("slept %s", d)
			return nil
		}
		if _, err := c.Me(context.Background(), "tok"); err == nil || n != 1 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	})
	t.Run("unix reset within three seconds", func(t *testing.T) {
		now := time.Unix(1_700_000_100, 0)
		var n int
		var slept []time.Duration
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n++
			if n == 1 {
				w.Header().Set("X-Rate-Limit-Reset", "1700000102")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"code":429,"message":"慢一点"}`))
				return
			}
			writeKOOK(w, 0, "操作成功", User{ID: "1", Username: "小明", IdentifyNum: "1"})
		}))
		defer srv.Close()
		c := newTestClient(t, srv, nil)
		c.now = func() time.Time { return now }
		c.sleep = func(ctx context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		}
		if _, err := c.Me(context.Background(), "tok"); err != nil {
			t.Fatal(err)
		}
		if n != 2 || len(slept) != 1 || slept[0] != 2*time.Second {
			t.Fatalf("n=%d slept=%v", n, slept)
		}
	})
}

func TestRateLimitBoundary(t *testing.T) {
	for _, tc := range []struct {
		header string
		retry  bool
		wait   time.Duration
	}{
		{header: "3", retry: true, wait: 3 * time.Second},
		{header: "4", retry: false},
		{header: "0", retry: false},
	} {
		t.Run(tc.header, func(t *testing.T) {
			var n int
			var slept []time.Duration
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n++
				if n == 1 {
					w.Header().Set("X-Rate-Limit-Reset", tc.header)
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"code":429,"message":"慢一点"}`))
					return
				}
				writeKOOK(w, 0, "操作成功", User{ID: "1", Username: "小明", IdentifyNum: "1"})
			}))
			defer srv.Close()
			c := newTestClient(t, srv, nil)
			c.sleep = func(ctx context.Context, d time.Duration) error {
				slept = append(slept, d)
				return nil
			}
			_, err := c.Me(context.Background(), "tok")
			if tc.retry {
				if err != nil || n != 2 || len(slept) != 1 || slept[0] != tc.wait {
					t.Fatalf("n=%d slept=%v err=%v", n, slept, err)
				}
				return
			}
			if err == nil || n != 1 || len(slept) != 0 {
				t.Fatalf("n=%d slept=%v err=%v", n, slept, err)
			}
		})
	}
}

func newTestClient(t *testing.T, srv *httptest.Server, logs io.Writer) *Client {
	t.Helper()
	if logs == nil {
		logs = io.Discard
	}
	c := New("https://www.kookapp.cn/api", slog.New(slog.NewJSONHandler(logs, nil)))
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.SetRoundTripper(RewriteTransport(u, srv.Client().Transport))
	return c
}

func writeKOOK(w http.ResponseWriter, code int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message, "data": data})
}
