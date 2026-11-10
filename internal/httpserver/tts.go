package httpserver

import (
	"errors"
	"net/http"
	"strings"

	"bsdx/internal/secret"
	"bsdx/internal/speak"
	"bsdx/internal/store"
	"bsdx/internal/tts"
)

const apiKeyMask = "********"

// MountTTS registers synthesis settings and preview. Routes stay out of server.go.
func MountTTS(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("GET /api/tts", getTTS(d))
	mux.HandleFunc("PUT /api/tts", putTTS(d))
	mux.HandleFunc("GET /api/tts/voices", ttsVoices(d))
	mux.HandleFunc("POST /api/tts/preview", ttsPreview(d))
}

func getTTS(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, d, false) || !depsReady(w, d) {
			return
		}
		row, err := d.Store.TTS(r.Context())
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "暂时无法读取设置")
			return
		}
		writeOK(w, http.StatusOK, ttsView(row))
	}
}

func ttsView(row store.TTSSettings) map[string]any {
	if row.Provider == "" {
		row.Provider = "edge"
	}
	if row.EdgeVoice == "" {
		row.EdgeVoice = tts.DefaultEdgeVoice
	}
	return map[string]any{
		"provider":    row.Provider,
		"edge_voice":  row.EdgeVoice,
		"rate":        row.Rate,
		"volume":      row.Volume,
		"api_url":     row.APIURL,
		"api_model":   row.APIModel,
		"api_voice":   row.APIVoice,
		"api_key_set": len(row.APIKeyCT) > 0,
		"presets":     tts.Presets(),
		"voices":      tts.EdgeVoices(),
	}
}

func putTTS(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, d, true) || !depsReady(w, d) {
			return
		}
		var body struct {
			Provider  string  `json:"provider"`
			EdgeVoice string  `json:"edge_voice"`
			Rate      float64 `json:"rate"`
			Volume    float64 `json:"volume"`
			APIURL    string  `json:"api_url"`
			APIModel  string  `json:"api_model"`
			APIVoice  string  `json:"api_voice"`
			APIKey    *string `json:"api_key"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_json", "请求格式错误")
			return
		}
		body.Provider = strings.TrimSpace(body.Provider)
		if body.Provider != "edge" && body.Provider != "openai" {
			writeErr(w, http.StatusBadRequest, "bad_json", "语音引擎无效")
			return
		}
		if body.Rate == 0 {
			body.Rate = 1
		}
		if body.Volume == 0 {
			body.Volume = 0.8
		}
		if body.Rate < 0.5 || body.Rate > 2 {
			writeErr(w, http.StatusBadRequest, "bad_rate", "语速要在 0.5 到 2.0 之间")
			return
		}
		if body.Volume < 0.05 || body.Volume > 2 {
			writeErr(w, http.StatusBadRequest, "bad_volume", "音量要在 0.05 到 2.0 之间")
			return
		}
		body.EdgeVoice = strings.TrimSpace(body.EdgeVoice)
		if body.EdgeVoice == "" {
			body.EdgeVoice = tts.DefaultEdgeVoice
		}
		if !tts.EdgeAllowed(body.EdgeVoice) {
			writeErr(w, http.StatusBadRequest, "bad_voice", "没有这个音色")
			return
		}
		body.APIURL = strings.TrimSpace(body.APIURL)
		body.APIModel = strings.TrimSpace(body.APIModel)
		body.APIVoice = strings.TrimSpace(body.APIVoice)
		if body.APIURL != "" {
			if err := tts.ValidateSpeechURL(body.APIURL, d.Config.TTSAllowPrivate, nil); err != nil {
				writeErr(w, http.StatusBadRequest, "bad_url", "语音接口地址无效")
				return
			}
		}
		if body.Provider == "openai" && body.APIVoice != "" {
			if err := tts.CheckAPIVoice(body.APIVoice); err != nil {
				writeErr(w, http.StatusBadRequest, "bad_api_voice", "接口音色无效")
				return
			}
		}
		prev, err := d.Store.TTS(r.Context())
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "暂时无法读取设置")
			return
		}
		row := store.TTSSettings{
			Provider:    body.Provider,
			EdgeVoice:   body.EdgeVoice,
			Rate:        body.Rate,
			Volume:      body.Volume,
			APIURL:      body.APIURL,
			APIModel:    body.APIModel,
			APIVoice:    body.APIVoice,
			APIKeyNonce: prev.APIKeyNonce,
			APIKeyCT:    prev.APIKeyCT,
		}
		if body.APIKey != nil && *body.APIKey != apiKeyMask {
			if *body.APIKey == "" {
				row.APIKeyNonce, row.APIKeyCT = nil, nil
			} else {
				nonce, ct, err := d.Box.Seal(secret.PurposeTTSAPIKey, []byte(*body.APIKey))
				if err != nil {
					writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "暂时无法保存")
					return
				}
				row.APIKeyNonce, row.APIKeyCT = nonce, ct
			}
		}
		if err := d.Store.SaveTTS(r.Context(), row); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "暂时无法保存")
			return
		}
		if d.Log != nil {
			d.Log.Info("tts saved", "component", "tts", "provider", row.Provider)
		}
		writeOK(w, http.StatusOK, ttsView(row))
	}
}

func ttsVoices(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, d, false) || !depsReady(w, d) {
			return
		}
		provider := r.URL.Query().Get("provider")
		if provider == "" {
			if row, err := d.Store.TTS(r.Context()); err == nil {
				provider = row.Provider
			}
		}
		if provider == "openai" {
			writeOK(w, http.StatusOK, map[string]any{"provider": "openai", "voices": tts.OpenAIVoiceHints()})
			return
		}
		writeOK(w, http.StatusOK, map[string]any{"provider": "edge", "voices": tts.EdgeVoices()})
	}
}

func ttsPreview(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, d, true) || !depsReady(w, d) {
			return
		}
		if d.Speak == nil {
			writeErr(w, http.StatusServiceUnavailable, "tts_failed", "语音合成失败")
			return
		}
		var body struct {
			Text     string  `json:"text"`
			VoiceID  string  `json:"voice_id"`
			Provider string  `json:"provider"`
			Rate     float64 `json:"rate"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_json", "请求格式错误")
			return
		}
		audio, err := d.Speak.Preview(r.Context(), body.Text, body.VoiceID, body.Provider, body.Rate)
		if err != nil {
			var rej *speak.Reject
			if errors.As(err, &rej) {
				writeErr(w, rej.Status, rej.Code, rej.Message)
				return
			}
			writeErr(w, http.StatusServiceUnavailable, "tts_failed", "语音合成失败")
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(audio)
	}
}
