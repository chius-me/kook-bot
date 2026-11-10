package httpserver

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"bsdx/internal/kook"
	"bsdx/internal/secret"
	"bsdx/internal/store"
)

const passwordMask = "********"

var idPattern = regexp.MustCompile(`^[0-9]{1,32}$`)

func idOK(s string) bool { return idPattern.MatchString(s) }

// MountPanel registers token, guild, and binding routes.
// Those paths stay out of server.go's route list.
func MountPanel(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("GET /settings", settingsPage(d))
	mux.HandleFunc("GET /api/status", statusAPI(d))
	mux.HandleFunc("PUT /api/token", putToken(d))
	mux.HandleFunc("POST /api/token/test", testToken(d))
	mux.HandleFunc("GET /api/guilds", guildsAPI(d))
	mux.HandleFunc("GET /api/guilds/{guild_id}/channels", channelsAPI(d))
	mux.HandleFunc("GET /api/binding", getBinding(d))
	mux.HandleFunc("PUT /api/binding", putBinding(d))
}

func settingsPage(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Sessions == nil || !d.Sessions.Valid(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		renderNamed(w, d.Log, d.Templates, "settings", pageData{Title: "设置"})
	}
}

func renderNamed(w http.ResponseWriter, log *slog.Logger, tmpl *template.Template, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if tmpl == nil {
		http.Error(w, "模板不可用", http.StatusInternalServerError)
		return
	}
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil && log != nil {
		log.Error("render", "component", "http", "err", err.Error())
	}
}

func statusAPI(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, d, false) {
			return
		}
		gw, voice := "down", "idle"
		if d.Status != nil {
			gw = d.Status.Gateway()
			voice = d.Status.Voice()
		}
		writeOK(w, http.StatusOK, d.Creds.Public(gw, voice))
	}
}

func putToken(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, d, true) || !depsReady(w, d) {
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_json", "请求格式错误")
			return
		}
		token := strings.TrimSpace(body.Token)
		if token == "" {
			if err := d.Store.ClearBotToken(r.Context()); err != nil {
				writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "暂时无法保存")
				return
			}
			d.Creds.Clear()
			if d.Log != nil {
				d.Log.Info("token cleared", "component", "kook")
			}
			writeOK(w, http.StatusOK, map[string]bool{"cleared": true})
			return
		}
		user, err := d.Kook.Me(r.Context(), token)
		if err != nil {
			failKook(w, err)
			return
		}
		nonce, ct, err := d.Box.Seal(secret.PurposeBotToken, []byte(token))
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "暂时无法保存")
			return
		}
		fp := secret.Fingerprint([]byte(d.Config.Secret))
		if err := d.Store.SetBotToken(r.Context(), nonce, ct, fp); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "暂时无法保存")
			return
		}
		d.Creds.Commit(token, user)
		if d.Log != nil {
			d.Log.Info("token saved", "component", "kook")
		}
		writeOK(w, http.StatusOK, user)
	}
}

func testToken(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, d, true) || !depsReady(w, d) {
			return
		}
		var body struct {
			Token *string `json:"token"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_json", "请求格式错误")
			return
		}
		explicit := body.Token != nil && strings.TrimSpace(*body.Token) != ""
		var token string
		var gen int
		if explicit {
			token = strings.TrimSpace(*body.Token)
		} else {
			saved, savedGen, mismatch := d.Creds.snapshot()
			if mismatch {
				writeCred(w, "secret_mismatch")
				return
			}
			if saved == "" {
				writeCred(w, "token_missing")
				return
			}
			token = saved
			gen = savedGen
		}
		user, err := d.Kook.Me(r.Context(), token)
		if err != nil {
			if !explicit {
				d.Creds.observe(gen, token, kook.User{}, err)
			}
			failKook(w, err)
			return
		}
		if !explicit {
			d.Creds.observe(gen, token, user, nil)
		}
		writeOK(w, http.StatusOK, user)
	}
}

func guildsAPI(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, d, false) || !depsReady(w, d) {
			return
		}
		token, code := d.Creds.usable()
		if code != "" {
			writeCred(w, code)
			return
		}
		guilds, err := d.Kook.Guilds(r.Context(), token)
		if err != nil {
			failKook(w, err)
			return
		}
		writeOK(w, http.StatusOK, map[string]any{"guilds": guilds})
	}
}

func channelsAPI(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, d, false) || !depsReady(w, d) {
			return
		}
		guildID := r.PathValue("guild_id")
		if !idOK(guildID) {
			writeErr(w, http.StatusBadRequest, "bad_channel_type", "服务器 ID 无效")
			return
		}
		token, code := d.Creds.usable()
		if code != "" {
			writeCred(w, code)
			return
		}
		groups, err := d.Kook.Channels(r.Context(), token, guildID)
		if err != nil {
			failKook(w, err)
			return
		}
		writeOK(w, http.StatusOK, groups)
	}
}

type bindingBody struct {
	GuildID        string  `json:"guild_id"`
	VoiceChannelID string  `json:"voice_channel_id"`
	TextChannelID  string  `json:"text_channel_id"`
	VoicePassword  *string `json:"voice_password"`
	MirrorEnabled  bool    `json:"mirror_enabled"`
	MirrorFormat   string  `json:"mirror_format"`
}

type bindingView struct {
	GuildID          string `json:"guild_id"`
	GuildName        string `json:"guild_name"`
	VoiceChannelID   string `json:"voice_channel_id"`
	VoiceChannelName string `json:"voice_channel_name"`
	TextChannelID    string `json:"text_channel_id"`
	TextChannelName  string `json:"text_channel_name"`
	VoicePasswordSet bool   `json:"voice_password_set"`
	MirrorEnabled    bool   `json:"mirror_enabled"`
	MirrorFormat     string `json:"mirror_format"`
}

func viewOf(b store.Binding) bindingView {
	format := b.MirrorFormat
	if format == "" {
		format = "kmarkdown"
	}
	return bindingView{
		GuildID:          b.GuildID,
		GuildName:        b.GuildName,
		VoiceChannelID:   b.VoiceChannelID,
		VoiceChannelName: b.VoiceChannelName,
		TextChannelID:    b.TextChannelID,
		TextChannelName:  b.TextChannelName,
		VoicePasswordSet: len(b.VoicePasswordCT) > 0,
		MirrorEnabled:    b.MirrorEnabled,
		MirrorFormat:     format,
	}
}

func getBinding(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, d, false) || !depsReady(w, d) {
			return
		}
		b, err := d.Store.Binding(r.Context())
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "暂时无法读取")
			return
		}
		writeOK(w, http.StatusOK, viewOf(b))
	}
}

func putBinding(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, d, true) || !depsReady(w, d) {
			return
		}
		var body bindingBody
		if err := decodeJSON(w, r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_json", "请求格式错误")
			return
		}
		body.GuildID = strings.TrimSpace(body.GuildID)
		body.VoiceChannelID = strings.TrimSpace(body.VoiceChannelID)
		body.TextChannelID = strings.TrimSpace(body.TextChannelID)
		if body.MirrorFormat == "" {
			body.MirrorFormat = "kmarkdown"
		}
		if body.MirrorFormat != "kmarkdown" && body.MirrorFormat != "card" {
			writeErr(w, http.StatusBadRequest, "bad_json", "镜像格式无效")
			return
		}
		if body.MirrorEnabled && body.TextChannelID == "" {
			writeErr(w, http.StatusBadRequest, "mirror_needs_text_channel", "打开镜像需要先选择文字频道")
			return
		}
		if !idOK(body.GuildID) || !idOK(body.VoiceChannelID) || (body.TextChannelID != "" && !idOK(body.TextChannelID)) {
			writeErr(w, http.StatusBadRequest, "bad_channel_type", "频道或服务器无效")
			return
		}
		token, code := d.Creds.usable()
		if code != "" {
			writeCred(w, code)
			return
		}
		guilds, err := d.Kook.Guilds(r.Context(), token)
		if err != nil {
			failKook(w, err)
			return
		}
		guildName, found := "", false
		for _, g := range guilds {
			if g.ID == body.GuildID {
				guildName = g.Name
				found = true
				break
			}
		}
		if !found {
			writeErr(w, http.StatusBadRequest, "bad_channel_type", "机器人不在这个服务器")
			return
		}
		voice, err := d.Kook.Channel(r.Context(), token, body.VoiceChannelID)
		if err != nil {
			failKook(w, err)
			return
		}
		if voice.Type != kook.ChannelVoice || voice.GuildID != body.GuildID {
			writeErr(w, http.StatusBadRequest, "bad_channel_type", "这不是所选服务器的语音频道")
			return
		}
		textID, textName := "", ""
		if body.TextChannelID != "" {
			text, err := d.Kook.Channel(r.Context(), token, body.TextChannelID)
			if err != nil {
				failKook(w, err)
				return
			}
			if text.Type != kook.ChannelText || text.GuildID != body.GuildID {
				writeErr(w, http.StatusBadRequest, "bad_channel_type", "这不是所选服务器的文字频道")
				return
			}
			textID = text.ID
			if textID == "" {
				textID = body.TextChannelID
			}
			textName = text.Name
		}
		current, err := d.Store.Binding(r.Context())
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "暂时无法保存")
			return
		}
		nonce, ct, ok := passwordBlobs(w, d, voice, body.VoicePassword, current)
		if !ok {
			return
		}
		voiceID := voice.ID
		if voiceID == "" {
			voiceID = body.VoiceChannelID
		}
		saved := store.Binding{
			GuildID:            body.GuildID,
			GuildName:          guildName,
			VoiceChannelID:     voiceID,
			VoiceChannelName:   voice.Name,
			VoicePasswordNonce: nonce,
			VoicePasswordCT:    ct,
			TextChannelID:      textID,
			TextChannelName:    textName,
			MirrorEnabled:      body.MirrorEnabled,
			MirrorFormat:       body.MirrorFormat,
		}
		if err := d.Store.SaveBinding(r.Context(), saved); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "暂时无法保存")
			return
		}
		if d.Log != nil {
			d.Log.Info("binding saved", "component", "kook", "guild_id", saved.GuildID, "voice_channel_id", saved.VoiceChannelID)
		}
		writeOK(w, http.StatusOK, viewOf(saved))
	}
}

// passwordBlobs returns false after it has written the HTTP error.
// A room without a password clears any stored password.
// Omitted or the mask keeps the saved ciphertext. An empty string clears it.
func passwordBlobs(w http.ResponseWriter, d Deps, voice kook.Channel, password *string, current store.Binding) ([]byte, []byte, bool) {
	needs := voice.HasPassword != nil && *voice.HasPassword
	if !needs {
		return nil, nil, true
	}
	keep := password == nil || *password == passwordMask
	if keep {
		if len(current.VoicePasswordCT) == 0 {
			writeErr(w, http.StatusBadRequest, "voice_password_required", "这个语音频道需要房间密码")
			return nil, nil, false
		}
		return append([]byte(nil), current.VoicePasswordNonce...), append([]byte(nil), current.VoicePasswordCT...), true
	}
	if *password == "" {
		writeErr(w, http.StatusBadRequest, "voice_password_required", "这个语音频道需要房间密码")
		return nil, nil, false
	}
	nonce, ct, err := d.Box.Seal(secret.PurposeVoicePassword, []byte(*password))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "暂时无法保存")
		return nil, nil, false
	}
	return nonce, ct, true
}

func authorized(w http.ResponseWriter, r *http.Request, d Deps, mutate bool) bool {
	if d.Sessions == nil || !d.Sessions.Valid(r) {
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "未登录")
		return false
	}
	if mutate && !csrfOK(r) {
		writeErr(w, http.StatusForbidden, "bad_csrf", "CSRF 校验失败")
		return false
	}
	return true
}

func depsReady(w http.ResponseWriter, d Deps) bool {
	if d.Creds == nil || d.Kook == nil || d.Store == nil || d.Box == nil {
		writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "KOOK 暂时无法连接")
		return false
	}
	return true
}

func writeCred(w http.ResponseWriter, code string) {
	switch code {
	case "secret_mismatch":
		writeErr(w, http.StatusServiceUnavailable, "secret_mismatch", "密钥已更换，请重新填写 Token")
	default:
		writeErr(w, http.StatusServiceUnavailable, "token_missing", "还没有 Token")
	}
}

func failKook(w http.ResponseWriter, err error) {
	var api *kook.APIError
	if errors.As(err, &api) && api.Code != 0 && !api.Unavailable {
		msg := api.Message
		if msg == "" {
			msg = "KOOK 返回错误"
		}
		writeKookErr(w, api.Code, msg)
		return
	}
	writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "KOOK 暂时无法连接")
}
