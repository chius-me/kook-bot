package httpserver

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"bsdx/internal/secret"
)

const (
	sessionCookie = "kookbot_session"
	csrfCookie    = "kookbot_csrf"
	sessionTTL    = 12 * time.Hour
	loginWindow   = 15 * time.Minute
	loginMaxFails = 5
)

// Sessions keeps SHA-256 digests of session cookies in memory.
// A process restart drops every session.
type Sessions struct {
	mu    sync.Mutex
	items map[string]time.Time
	now   func() time.Time
}

func NewSessions() *Sessions {
	return &Sessions{items: make(map[string]time.Time), now: time.Now}
}

func (s *Sessions) Put(cookie string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[hashToken(cookie)] = s.now().Add(sessionTTL)
}

func (s *Sessions) Valid(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.items[hashToken(c.Value)]
	if !ok || !s.now().Before(exp) {
		delete(s.items, hashToken(c.Value))
		return false
	}
	return true
}

func (s *Sessions) Delete(cookie string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, hashToken(cookie))
}

func hashToken(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

type loginLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
	now  func() time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{hits: make(map[string][]time.Time), now: time.Now}
}

func (l *loginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(ip)) >= loginMaxFails
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hits[ip] = append(l.prune(ip), l.now())
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, ip)
}

func (l *loginLimiter) prune(ip string) []time.Time {
	cut := l.now().Add(-loginWindow)
	kept := l.hits[ip][:0]
	for _, ts := range l.hits[ip] {
		if ts.After(cut) {
			kept = append(kept, ts)
		}
	}
	if len(kept) == 0 {
		delete(l.hits, ip)
		return nil
	}
	l.hits[ip] = kept
	return kept
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.limiter.blocked(ip) {
		writeErr(w, http.StatusTooManyRequests, "login_limited", "登录过于频繁，请稍后再试")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "请求格式错误")
		return
	}
	if !secret.PasswordMatch(body.Password, s.deps.Config.UIPassword) {
		s.limiter.fail(ip)
		writeErr(w, http.StatusForbidden, "bad_password", "密码错误")
		return
	}
	s.limiter.reset(ip)
	sid, err := newToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "kook_unavailable", "暂时无法登录")
		return
	}
	csrf, err := newToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "kook_unavailable", "暂时无法登录")
		return
	}
	s.deps.Sessions.Put(sid)
	s.setSessionCookies(w, sid, csrf)
	writeOK(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.deps.Sessions.Valid(r) {
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "未登录")
		return
	}
	if !csrfOK(r) {
		writeErr(w, http.StatusForbidden, "bad_csrf", "CSRF 校验失败")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.deps.Sessions.Delete(c.Value)
	}
	s.clearCookie(w, sessionCookie)
	s.clearCookie(w, csrfCookie)
	writeOK(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	if !s.deps.Sessions.Valid(r) {
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "未登录")
		return
	}
	writeOK(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) setSessionCookies(w http.ResponseWriter, sid, csrf string) {
	s.writeCookie(w, sessionCookie, sid, true)
	s.writeCookie(w, csrfCookie, csrf, false)
}

func (s *Server) writeCookie(w http.ResponseWriter, name, value string, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: httpOnly,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.deps.Config.CookieSecure,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: name == sessionCookie,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.deps.Config.CookieSecure,
	})
}

func csrfOK(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	got := r.Header.Get("X-CSRF-Token")
	if len(got) != len(c.Value) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(c.Value)) == 1
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dest any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := dec.Decode(dest); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing json")
	}
	return nil
}

// clientIP uses the TCP peer only. A forwarded header would let a caller pick a fresh limit bucket.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		if r.RemoteAddr == "" {
			return "unknown"
		}
		return r.RemoteAddr
	}
	return host
}
