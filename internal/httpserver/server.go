package httpserver

import (
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"bsdx/internal/config"
	"bsdx/internal/kook"
	"bsdx/internal/module"
	"bsdx/internal/secret"
	"bsdx/internal/speak"
	"bsdx/internal/store"
	"bsdx/internal/voice"
	"bsdx/web"
)

// Deps are the process pieces HTTP handlers are allowed to see.
type Deps struct {
	Config    config.Config
	Store     *store.Store
	Log       *slog.Logger
	Status    RuntimeStatus
	Host      *module.Host
	Sessions  *Sessions
	Box       *secret.Box
	Kook      *kook.Client
	Creds     *Credentials
	Speak     *speak.Service
	Voice     *voice.Session
	Templates *template.Template
}

// MountFunc adds routes from another file. server.go does not grow a second route table for those.
type MountFunc func(mux *http.ServeMux, d Deps)

// Server serves the panel and /healthz.
type Server struct {
	deps    Deps
	extra   []MountFunc
	tmpl    *template.Template
	limiter *loginLimiter
	handler http.Handler
}

func New(d Deps, extra []MountFunc) (*Server, error) {
	if d.Sessions == nil {
		d.Sessions = NewSessions()
	}
	if d.Status == nil {
		d.Status = DownStatus{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	tmpl, err := template.ParseFS(web.FS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	d.Templates = tmpl
	s := &Server{deps: d, extra: extra, tmpl: tmpl, limiter: newLoginLimiter()}
	s.handler = s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

// routes registers login, logout, session, and /healthz.
// Later route files mount themselves through MountFunc instead of editing this list.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("GET /{$}", s.homePage)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/session", s.session)
	if sub, err := fs.Sub(web.FS, "static"); err == nil {
		mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	}
	for _, mount := range s.extra {
		mount(mux, s.deps)
	}
	return s.logRequests(secureHeaders(mux))
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeOK(w, http.StatusOK, map[string]string{
		"status":  "up",
		"gateway": s.deps.Status.Gateway(),
		"voice":   s.deps.Status.Voice(),
	})
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.deps.Sessions.Valid(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, "login", pageData{Title: "登录"})
}

func (s *Server) homePage(w http.ResponseWriter, r *http.Request) {
	if !s.deps.Sessions.Valid(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	gw, voiceState := "down", "idle"
	if s.deps.Status != nil {
		gw = s.deps.Status.Gateway()
		voiceState = s.deps.Status.Voice()
	}
	data := pageData{
		Title:        "kookbot",
		Line:         "还没有绑定 KOOK 机器人。",
		GatewayLabel: gatewayLabel(gw),
		VoiceLabel:   voice.Label(voiceState),
	}
	if s.deps.Creds != nil {
		view := s.deps.Creds.Home()
		data.Line = liveLine(view.Line, gw)
		data.Identity = view.Identity
		data.Err = view.Err
		pub := s.deps.Creds.Public(gw, voiceState)
		if pub.Identity != nil {
			data.Avatar = pub.Identity.Avatar
		}
	}
	s.render(w, "home", data)
}

func liveLine(line, gateway string) string {
	switch gateway {
	case "ready":
		return strings.ReplaceAll(line, "网关未连接", "网关已连接")
	case "token_rejected":
		if strings.Contains(line, "网关") || strings.Contains(line, "凭证") {
			return "Token 已被拒绝，需要重新填写。"
		}
	}
	return line
}

func gatewayLabel(state string) string {
	switch state {
	case "ready":
		return "已连接"
	case "token_rejected":
		return "Token 已被拒绝"
	default:
		return "未连接"
	}
}

type pageData struct {
	Title        string
	Line         string
	Identity     string
	Err          string
	Avatar       string
	GatewayLabel string
	VoiceLabel   string
}

func (s *Server) render(w http.ResponseWriter, name string, data pageData) {
	renderNamed(w, s.deps.Log, s.tmpl, name, data)
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.deps.Log.Info("request",
			"component", "http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"latency_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
