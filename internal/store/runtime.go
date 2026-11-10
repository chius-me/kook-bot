package store

import (
	"context"
	"database/sql"
)

// SetGateway stores the live session id and the cursor for this process.
// A failed write is safe: the next ping asks KOOK to replay.
func (s *Store) SetGateway(ctx context.Context, sessionID string, sn int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE settings
		SET gateway_session_id = ?, gateway_sn = ?, updated_at = ?
		WHERE id = 1`, sessionID, sn, nowUTC())
	return err
}

// ClearGateway forgets the session. The next connection starts at sn 0.
func (s *Store) ClearGateway(ctx context.Context) error {
	return s.SetGateway(ctx, "", 0)
}

// TTSSettings is the single synthesis row. Key blobs are nil when unset.
type TTSSettings struct {
	Provider    string
	EdgeVoice   string
	Rate        float64
	Volume      float64
	APIURL      string
	APIModel    string
	APIVoice    string
	APIKeyNonce []byte
	APIKeyCT    []byte
}

func (s *Store) TTS(ctx context.Context) (TTSSettings, error) {
	var t TTSSettings
	err := s.db.QueryRowContext(ctx, `
		SELECT provider, edge_voice, rate, volume, api_url, api_model, api_voice,
			api_key_nonce, api_key_ct
		FROM tts_settings WHERE id = 1`).Scan(
		&t.Provider, &t.EdgeVoice, &t.Rate, &t.Volume, &t.APIURL, &t.APIModel, &t.APIVoice,
		&t.APIKeyNonce, &t.APIKeyCT,
	)
	return t, err
}

// SaveTTS writes synthesis settings, including the API key blobs.
// Binding saves do not call this.
func (s *Store) SaveTTS(ctx context.Context, t TTSSettings) error {
	var nonce, ct any
	if len(t.APIKeyNonce) == 0 && len(t.APIKeyCT) == 0 {
		nonce, ct = nil, nil
	} else {
		nonce = t.APIKeyNonce
		ct = t.APIKeyCT
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE tts_settings SET
			provider = ?, edge_voice = ?, rate = ?, volume = ?,
			api_url = ?, api_model = ?, api_voice = ?,
			api_key_nonce = ?, api_key_ct = ?, updated_at = ?
		WHERE id = 1`,
		t.Provider, t.EdgeVoice, t.Rate, t.Volume,
		t.APIURL, t.APIModel, t.APIVoice,
		nonce, ct, nowUTC(),
	)
	return err
}

// HistoryRow is one speak_history record.
type HistoryRow struct {
	ID             int64
	CreatedAt      string
	Text           string
	Provider       string
	VoiceID        string
	Rate           float64
	Volume         float64
	VoiceChannelID string
	Status         string
	Error          string
	SynthMS        sql.NullInt64
	PlayMS         sql.NullInt64
}

func (s *Store) InsertHistory(ctx context.Context, row HistoryRow) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO speak_history (
			created_at, text, provider, voice_id, rate, volume,
			voice_channel_id, status, error, synth_ms, play_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.CreatedAt, row.Text, row.Provider, row.VoiceID, row.Rate, row.Volume,
		row.VoiceChannelID, row.Status, row.Error, nullInt(row.SynthMS), nullInt(row.PlayMS),
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := s.TrimHistory(ctx, 200); err != nil {
		return id, err
	}
	return id, nil
}

func nullInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

func (s *Store) UpdateHistory(ctx context.Context, row HistoryRow) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE speak_history SET
			status = ?, error = ?, synth_ms = ?, play_ms = ?, voice_channel_id = ?
		WHERE id = ?`,
		row.Status, row.Error, nullInt(row.SynthMS), nullInt(row.PlayMS), row.VoiceChannelID, row.ID,
	)
	return err
}

func (s *Store) ListHistory(ctx context.Context, limit int) ([]HistoryRow, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, created_at, text, provider, voice_id, rate, volume,
			voice_channel_id, status, error, synth_ms, play_ms
		FROM speak_history ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryRow
	for rows.Next() {
		var row HistoryRow
		if err := rows.Scan(
			&row.ID, &row.CreatedAt, &row.Text, &row.Provider, &row.VoiceID, &row.Rate, &row.Volume,
			&row.VoiceChannelID, &row.Status, &row.Error, &row.SynthMS, &row.PlayMS,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if out == nil {
		out = []HistoryRow{}
	}
	return out, rows.Err()
}

func (s *Store) TrimHistory(ctx context.Context, keep int) error {
	if keep < 1 {
		keep = 200
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM speak_history
		WHERE id NOT IN (SELECT id FROM speak_history ORDER BY id DESC LIMIT ?)`, keep)
	return err
}
