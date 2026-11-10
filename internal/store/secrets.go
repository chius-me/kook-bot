package store

import (
	"context"
	"time"
)

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// BotToken returns the sealed bot token. Both slices are nil when no token is stored.
func (s *Store) BotToken(ctx context.Context) (nonce, ct []byte, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT bot_token_nonce, bot_token_ct FROM settings WHERE id = 1`).Scan(&nonce, &ct)
	return nonce, ct, err
}

// SetBotToken stores the sealed token and the secret fingerprint in one update.
func (s *Store) SetBotToken(ctx context.Context, nonce, ct []byte, fp string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE settings
		SET bot_token_nonce = ?, bot_token_ct = ?, secret_fp = ?, updated_at = ?
		WHERE id = 1`, nonce, ct, fp, nowUTC())
	return err
}

// ClearBotToken removes the sealed token. It does not change secret_fp.
func (s *Store) ClearBotToken(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE settings
		SET bot_token_nonce = NULL, bot_token_ct = NULL, updated_at = ?
		WHERE id = 1`, nowUTC())
	return err
}

// Binding is the single channel pin. Password blobs are nil when unset.
type Binding struct {
	GuildID            string
	GuildName          string
	VoiceChannelID     string
	VoiceChannelName   string
	VoicePasswordNonce []byte
	VoicePasswordCT    []byte
	TextChannelID      string
	TextChannelName    string
	MirrorEnabled      bool
	MirrorFormat       string
}

func (s *Store) Binding(ctx context.Context) (Binding, error) {
	var b Binding
	var mirror int
	err := s.db.QueryRowContext(ctx, `
		SELECT guild_id, guild_name, voice_channel_id, voice_channel_name,
			voice_password_nonce, voice_password_ct,
			text_channel_id, text_channel_name, mirror_enabled, mirror_format
		FROM channel_binding WHERE id = 1`).Scan(
		&b.GuildID, &b.GuildName, &b.VoiceChannelID, &b.VoiceChannelName,
		&b.VoicePasswordNonce, &b.VoicePasswordCT,
		&b.TextChannelID, &b.TextChannelName, &mirror, &b.MirrorFormat,
	)
	b.MirrorEnabled = mirror != 0
	return b, err
}

// SaveBinding writes the channel pin. It does not read or write tts_settings.
func (s *Store) SaveBinding(ctx context.Context, b Binding) error {
	mirror := 0
	if b.MirrorEnabled {
		mirror = 1
	}
	if b.MirrorFormat == "" {
		b.MirrorFormat = "kmarkdown"
	}
	var nonce, ct any
	if len(b.VoicePasswordNonce) == 0 && len(b.VoicePasswordCT) == 0 {
		nonce, ct = nil, nil
	} else {
		nonce = b.VoicePasswordNonce
		ct = b.VoicePasswordCT
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE channel_binding SET
			guild_id = ?, guild_name = ?,
			voice_channel_id = ?, voice_channel_name = ?,
			voice_password_nonce = ?, voice_password_ct = ?,
			text_channel_id = ?, text_channel_name = ?,
			mirror_enabled = ?, mirror_format = ?, updated_at = ?
		WHERE id = 1`,
		b.GuildID, b.GuildName,
		b.VoiceChannelID, b.VoiceChannelName,
		nonce, ct,
		b.TextChannelID, b.TextChannelName,
		mirror, b.MirrorFormat, nowUTC(),
	)
	return err
}
