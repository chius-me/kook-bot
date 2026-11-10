package logx

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
)

var (
	reWSS   = regexp.MustCompile(`wss://[^\s"'<>]+`)
	reAuth  = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)(?:"[^"]*"|'[^']*'|(?:(?:bot|bearer)\s+)?\S+)`)
	reField = regexp.MustCompile(`(?i)(\b(?:token|api_key|password|encrypt|cookie)\b\s*[:=]\s*)(?:"[^"]*"|'[^']*'|\S+)`)
	reJSON  = regexp.MustCompile(`(?i)("(?:authorization|token|api_key|password|encrypt|cookie)"\s*:\s*")[^"]*(")`)
)

// Redact removes credential-shaped substrings from a log line.
// wss query strings are dropped. Authorization and named secret fields are replaced.
func Redact(s string) string {
	s = reJSON.ReplaceAllString(s, `${1}[redacted]${2}`)
	s = reAuth.ReplaceAllString(s, `${1}[redacted]`)
	s = reField.ReplaceAllString(s, `${1}[redacted]`)
	s = reWSS.ReplaceAllStringFunc(s, dropWSSQuery)
	return s
}

func dropWSSQuery(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "wss://redacted"
	}
	if u.RawQuery == "" && u.Fragment == "" {
		return raw
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// New returns a JSON logger that redacts messages and string attributes.
func New(w io.Writer, level slog.Level) *slog.Logger {
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(redactHandler{next: base})
}

type redactHandler struct {
	next slog.Handler
}

func (h redactHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h redactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = redactAttr(a)
	}
	return redactHandler{next: h.next.WithAttrs(clean)}
}

func (h redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{next: h.next.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	switch a.Value.Kind() {
	case slog.KindString:
		if sensitiveKey(a.Key) {
			return slog.String(a.Key, "[redacted]")
		}
		return slog.String(a.Key, Redact(a.Value.String()))
	case slog.KindGroup:
		group := a.Value.Group()
		clean := make([]slog.Attr, len(group))
		for i, g := range group {
			clean[i] = redactAttr(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok && err != nil {
			return slog.String(a.Key, Redact(err.Error()))
		}
	}
	if sensitiveKey(a.Key) {
		return slog.String(a.Key, "[redacted]")
	}
	return a
}

func sensitiveKey(key string) bool {
	switch strings.ToLower(key) {
	case "authorization", "token", "api_key", "password", "encrypt", "cookie":
		return true
	default:
		return false
	}
}
