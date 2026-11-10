package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenSeedsSingletonsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kookbot.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assertSeed(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertSeed(t, s)
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("migrations = %d", n)
	}
}

func TestSecretFingerprintRoundTrip(t *testing.T) {
	s := openTemp(t)
	fp, err := s.SecretFP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fp != "" {
		t.Fatalf("fp = %q", fp)
	}
	if err := s.SetSecretFP(context.Background(), "abcd1234"); err != nil {
		t.Fatal(err)
	}
	fp, err = s.SecretFP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fp != "abcd1234" {
		t.Fatalf("fp = %q", fp)
	}
}

func TestBotTokenRoundTripLeavesFingerprintOnClear(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	nonce, ct, err := s.BotToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if nonce != nil || ct != nil {
		t.Fatalf("nonce=%v ct=%v", nonce, ct)
	}
	if err := s.SetBotToken(ctx, []byte("nonce-bytes-12"), []byte("cipher"), "abcd1234"); err != nil {
		t.Fatal(err)
	}
	nonce, ct, err = s.BotToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(nonce) != "nonce-bytes-12" || string(ct) != "cipher" {
		t.Fatalf("nonce=%q ct=%q", nonce, ct)
	}
	fp, err := s.SecretFP(ctx)
	if err != nil || fp != "abcd1234" {
		t.Fatalf("fp %q %v", fp, err)
	}
	if err := s.ClearBotToken(ctx); err != nil {
		t.Fatal(err)
	}
	nonce, ct, err = s.BotToken(ctx)
	if err != nil || nonce != nil || ct != nil {
		t.Fatalf("cleared nonce=%v ct=%v err=%v", nonce, ct, err)
	}
	fp, err = s.SecretFP(ctx)
	if err != nil || fp != "abcd1234" {
		t.Fatalf("fp after clear %q %v", fp, err)
	}
}

func TestSaveBindingLeavesTTSKeyNull(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	err := s.SaveBinding(ctx, Binding{
		GuildID:            "1",
		GuildName:          "服务器甲",
		VoiceChannelID:     "2",
		VoiceChannelName:   "大厅",
		VoicePasswordNonce: []byte("nonce-bytes-12"),
		VoicePasswordCT:    []byte("cipher"),
		TextChannelID:      "3",
		TextChannelName:    "综合",
		MirrorEnabled:      true,
		MirrorFormat:       "card",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Binding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.GuildName != "服务器甲" || got.VoiceChannelName != "大厅" || !got.MirrorEnabled || got.MirrorFormat != "card" {
		t.Fatalf("%+v", got)
	}
	if string(got.VoicePasswordCT) != "cipher" {
		t.Fatalf("ct %q", got.VoicePasswordCT)
	}
	var key []byte
	if err := s.db.QueryRowContext(ctx, `SELECT api_key_ct FROM tts_settings WHERE id = 1`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != nil {
		t.Fatalf("api key written: %v", key)
	}
	if err := s.SaveBinding(ctx, Binding{GuildID: "9", MirrorFormat: "kmarkdown"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Binding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.VoicePasswordCT != nil || got.VoicePasswordNonce != nil || got.GuildID != "9" {
		t.Fatalf("cleared password %+v", got)
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "kookbot.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func assertSeed(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"settings", "channel_binding", "tts_settings"} {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE id = 1`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("%s rows = %d", table, n)
		}
	}
	var enabled int
	if err := s.db.QueryRowContext(ctx, `SELECT enabled FROM module_state WHERE module = 'speak'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatalf("speak enabled = %d", enabled)
	}
	var voices int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM voice_presets WHERE provider = 'edge'`).Scan(&voices); err != nil {
		t.Fatal(err)
	}
	if voices != 14 {
		t.Fatalf("voices = %d", voices)
	}
	var mirror int
	var format, voice string
	if err := s.db.QueryRowContext(ctx, `SELECT mirror_enabled, mirror_format FROM channel_binding WHERE id = 1`).Scan(&mirror, &format); err != nil {
		t.Fatal(err)
	}
	if mirror != 0 || format != "kmarkdown" {
		t.Fatalf("mirror %d %s", mirror, format)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT edge_voice FROM tts_settings WHERE id = 1`).Scan(&voice); err != nil {
		t.Fatal(err)
	}
	if voice != "zh-CN-XiaoxiaoNeural" {
		t.Fatalf("voice %s", voice)
	}
}
