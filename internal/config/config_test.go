package config

import (
	"strings"
	"testing"
)

func TestLoadRequiresSecretAndPassword(t *testing.T) {
	t.Setenv("KOOKBOT_SECRET", "")
	t.Setenv("KOOKBOT_UI_PASSWORD", "")
	t.Setenv("BOT_TOKEN", "should-be-ignored")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "KOOKBOT_SECRET") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "should-be-ignored") {
		t.Fatal("error includes BOT_TOKEN")
	}
}

func TestLoadRejectsShortSecret(t *testing.T) {
	t.Setenv("KOOKBOT_SECRET", "short-secret-value-not-enough!!")
	t.Setenv("KOOKBOT_UI_PASSWORD", "pw")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "32 bytes") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "short-secret") {
		t.Fatal("error includes the secret")
	}
}

func TestLoadDefaults(t *testing.T) {
	secret := strings.Repeat("k", 32)
	t.Setenv("KOOKBOT_SECRET", secret)
	t.Setenv("KOOKBOT_UI_PASSWORD", "pw")
	t.Setenv("KOOKBOT_HTTP_ADDR", "")
	t.Setenv("KOOKBOT_DATA_DIR", "")
	t.Setenv("KOOKBOT_BASE_URL", "")
	t.Setenv("KOOKBOT_LOG_LEVEL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != DefaultHTTPAddr || cfg.DataDir != DefaultDataDir || cfg.BaseURL != DefaultBaseURL {
		t.Fatalf("%+v", cfg.GoString())
	}
	if strings.Contains(cfg.GoString(), secret) || strings.Contains(cfg.GoString(), "pw") {
		t.Fatal("GoString leaked a credential")
	}
}

func TestLoadRejectsBarePort(t *testing.T) {
	t.Setenv("KOOKBOT_SECRET", strings.Repeat("k", 32))
	t.Setenv("KOOKBOT_UI_PASSWORD", "pw")
	t.Setenv("KOOKBOT_HTTP_ADDR", ":8765")
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
}
