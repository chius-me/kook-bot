package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultHTTPAddr = "127.0.0.1:8765"
	DefaultDataDir  = "/data"
	DefaultBaseURL  = "https://www.kookapp.cn/api"
	DefaultFFmpeg   = "ffmpeg"
	DefaultEdgeTTS  = "/opt/edge-tts/bin/edge-tts"
)

// Config is process bootstrap. The bot token is not here.
type Config struct {
	Secret          string
	UIPassword      string
	HTTPAddr        string
	DataDir         string
	BaseURL         string
	FFmpeg          string
	EdgeTTS         string
	TTSAllowPrivate bool
	CookieSecure    bool
	LogLevel        slog.Level
}

func (c Config) DBPath() string {
	return filepath.Join(c.DataDir, "kookbot.sqlite")
}

// Load reads the environment. It does not load dotenv files and it does not log.
func Load() (Config, error) {
	secret := os.Getenv("KOOKBOT_SECRET")
	if secret == "" {
		return Config{}, errorsNew("KOOKBOT_SECRET is required")
	}
	if len(secret) < 32 {
		return Config{}, errorsNew("KOOKBOT_SECRET must be at least 32 bytes")
	}
	password := os.Getenv("KOOKBOT_UI_PASSWORD")
	if password == "" {
		return Config{}, errorsNew("KOOKBOT_UI_PASSWORD is required")
	}
	addr := getenv("KOOKBOT_HTTP_ADDR", DefaultHTTPAddr)
	if err := checkAddr(addr); err != nil {
		return Config{}, err
	}
	level, err := parseLevel(getenv("KOOKBOT_LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}
	dataDir := getenv("KOOKBOT_DATA_DIR", DefaultDataDir)
	if strings.TrimSpace(dataDir) == "" {
		return Config{}, errorsNew("KOOKBOT_DATA_DIR is empty")
	}
	return Config{
		Secret:          secret,
		UIPassword:      password,
		HTTPAddr:        addr,
		DataDir:         dataDir,
		BaseURL:         getenv("KOOKBOT_BASE_URL", DefaultBaseURL),
		FFmpeg:          getenv("KOOKBOT_FFMPEG", DefaultFFmpeg),
		EdgeTTS:         getenv("KOOKBOT_EDGE_TTS", DefaultEdgeTTS),
		TTSAllowPrivate: os.Getenv("KOOKBOT_TTS_ALLOW_PRIVATE") == "1",
		CookieSecure:    os.Getenv("KOOKBOT_COOKIE_SECURE") == "1",
		LogLevel:        level,
	}, nil
}

func checkAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		return errorsNew("KOOKBOT_HTTP_ADDR must be host:port")
	}
	return nil
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, errorsNew("KOOKBOT_LOG_LEVEL is invalid")
	}
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

type plainError string

func (e plainError) Error() string { return string(e) }

func errorsNew(msg string) error { return plainError(msg) }

// Format is kept so a mistaken fmt of the config does not grow a Stringer
// that prints the secret. Callers log fields, not the struct.
func (c Config) GoString() string {
	return fmt.Sprintf("config.Config{HTTPAddr:%q, DataDir:%q}", c.HTTPAddr, c.DataDir)
}
