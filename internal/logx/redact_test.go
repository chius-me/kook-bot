package logx

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactAuthorizationAndWSSQuery(t *testing.T) {
	in := `dial Authorization: Bot super-secret-token url=wss://www.kookapp.cn/gateway?token=super-secret-token&compress=0`
	out := Redact(in)
	if strings.Contains(out, "super-secret-token") {
		t.Fatalf("secret survived: %s", out)
	}
	if !strings.Contains(out, "Authorization:") {
		t.Fatalf("label removed: %s", out)
	}
	if !strings.Contains(out, "wss://www.kookapp.cn/gateway") {
		t.Fatalf("gateway location removed: %s", out)
	}
	if strings.Contains(out, "compress=0") || strings.Contains(out, "?") {
		t.Fatalf("query survived: %s", out)
	}
}

func TestRedactNamedFields(t *testing.T) {
	in := `password=hunter2 api_key: "sk-live" cookie='session' encrypt=blob`
	out := Redact(in)
	for _, secret := range []string{"hunter2", "sk-live", "session", "blob"} {
		if strings.Contains(out, secret) {
			t.Fatalf("%s survived in %s", secret, out)
		}
	}
}

func TestLoggerRedactsAttributes(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)
	logger.Info("connect", "authorization", "Bot super-secret-token", "url", "wss://example/ws?token=super-secret-token")
	out := buf.String()
	if strings.Contains(out, "super-secret-token") {
		t.Fatalf("secret survived: %s", out)
	}
}
