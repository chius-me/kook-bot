package httpserver

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bsdx/internal/config"
)

func testServer(t *testing.T, password string, secure bool) (*Server, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	srv, err := New(Deps{
		Config: config.Config{UIPassword: password, CookieSecure: secure, HTTPAddr: "127.0.0.1:8765"},
		Log:    slog.New(slog.NewJSONHandler(&logs, nil)),
		Status: DownStatus{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return srv, &logs
}

func TestHealthz(t *testing.T) {
	srv, _ := testServer(t, "pw", false)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		OK   bool `json:"ok"`
		Data struct {
			Status  string `json:"status"`
			Gateway string `json:"gateway"`
			Voice   string `json:"voice"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Data.Status != "up" || body.Data.Gateway != "down" || body.Data.Voice != "idle" {
		t.Fatalf("%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "token") || strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("healthz leaked: %s", rec.Body.String())
	}
}

func TestLoginLogoutAndCSRF(t *testing.T) {
	srv, logs := testServer(t, "correct-password", false)
	rec := postJSON(t, srv, "/api/login", `{"password":"wrong"}`, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "bad_password") {
		t.Fatalf("wrong password: %d %s", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, srv, "/api/login", `{"password":"correct-password"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	var session, csrf *http.Cookie
	for _, c := range cookies {
		switch c.Name {
		case sessionCookie:
			session = c
		case csrfCookie:
			csrf = c
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode || session.Path != "/" || session.MaxAge != 12*3600 || session.Secure {
		t.Fatalf("session cookie %#v", session)
	}
	if csrf == nil || csrf.HttpOnly || csrf.Value == "" || csrf.Value == session.Value {
		t.Fatalf("csrf cookie %#v", csrf)
	}
	if strings.Contains(logs.String(), "correct-password") {
		t.Fatalf("password logged: %s", logs.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.AddCookie(session)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"authenticated":true`) {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.AddCookie(session)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "bad_csrf") {
		t.Fatalf("logout without csrf: %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.AddCookie(session)
	req.AddCookie(csrf)
	req.Header.Set("X-CSRF-Token", csrf.Value)
	req.RemoteAddr = "192.0.2.8:9"
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.AddCookie(session)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("session after logout: %d", rec.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	srv, _ := testServer(t, "correct-password", false)
	for i := 0; i < loginMaxFails; i++ {
		rec := postJSON(t, srv, "/api/login", `{"password":"no"}`, map[string]string{"addr": "192.0.2.9:1"})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	rec := postJSON(t, srv, "/api/login", `{"password":"correct-password"}`, map[string]string{"addr": "192.0.2.9:1"})
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "login_limited") {
		t.Fatalf("limited: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPagesAndMount(t *testing.T) {
	var logs bytes.Buffer
	srv, err := New(Deps{
		Config: config.Config{UIPassword: "pw"},
		Log:    slog.New(slog.NewJSONHandler(&logs, nil)),
	}, []MountFunc{
		func(mux *http.ServeMux, d Deps) {
			mux.HandleFunc("GET /api/modules/example", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("home redirect %d %s", rec.Code, rec.Header().Get("Location"))
	}
	req = httptest.NewRequest(http.MethodGet, "/login", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "登录") {
		t.Fatalf("login page %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "font-family") {
		t.Fatalf("css %d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/modules/example", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("mount %d", rec.Code)
	}
}

func TestSecureCookie(t *testing.T) {
	srv, _ := testServer(t, "pw", true)
	rec := postJSON(t, srv, "/api/login", `{"password":"pw"}`, nil)
	for _, c := range rec.Result().Cookies() {
		if !c.Secure {
			t.Fatalf("%s not secure", c.Name)
		}
	}
}

func TestLoopbackAddrIgnoresBindHost(t *testing.T) {
	got, err := LoopbackAddr("0.0.0.0:8765")
	if err != nil || got != "127.0.0.1:8765" {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := LoopbackAddr("8765"); err == nil {
		t.Fatal("expected error")
	}
}

func TestHealthcheckAgainstServer(t *testing.T) {
	srv, _ := testServer(t, "pw", false)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	port := strings.TrimPrefix(ts.URL, "http://127.0.0.1:")
	if err := RunHealthcheck("0.0.0.0:" + port); err != nil {
		t.Fatal(err)
	}
}

func postJSON(t *testing.T, srv *Server, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.1:1000"
	if hdr != nil && hdr["addr"] != "" {
		req.RemoteAddr = hdr["addr"]
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestUnauthenticatedSession(t *testing.T) {
	srv, _ := testServer(t, "pw", false)
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "unauthenticated") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
