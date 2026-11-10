package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"bsdx/internal/config"
	"bsdx/internal/kook"
	"bsdx/internal/secret"
	"bsdx/internal/store"
)

const (
	panelSecret = "0123456789abcdef0123456789abcdef"
	goodToken   = "bot-token-should-not-leak-0123456789"
	badToken    = "bad-token-should-not-stick-0123456789"
	otherToken  = "other-token-not-saved-zzzzzzzzzzzz"
	roomPass    = "room-secret-pw"
)

func TestPanelTokenAndBinding(t *testing.T) {
	env := newPanel(t)
	env.fake.users[otherToken] = kook.User{ID: "7", Username: "路人", IdentifyNum: "0002", Avatar: "https://img.example/b.png"}
	var changes int
	env.creds.SetOnChange(func() { changes++ })
	if err := env.st.SetSecretFP(context.Background(), "deadbeef"); err != nil {
		t.Fatal(err)
	}

	rec := call(t, env.web, http.MethodGet, "/settings", "", nil, nil, false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("settings redirect %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec = call(t, env.web, http.MethodGet, "/api/status", "", nil, nil, false)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "unauthenticated") {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if env.fake.hits() != 0 {
		t.Fatalf("kook called before login: %d", env.fake.hits())
	}

	session, csrf := loginPanel(t, env.web)
	rec = call(t, env.web, http.MethodPut, "/api/token", `{"token":"`+goodToken+`"}`, session, csrf, false)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "bad_csrf") {
		t.Fatalf("csrf %d %s", rec.Code, rec.Body.String())
	}
	if env.fake.hits() != 0 {
		t.Fatal("kook called without csrf")
	}

	rec = call(t, env.web, http.MethodPut, "/api/token", `{"token":"`+badToken+`"}`, session, csrf, true)
	assertKookErr(t, rec, 40001, "无效的 token")
	if strings.Contains(rec.Body.String(), badToken) {
		t.Fatalf("response leaked token: %s", rec.Body.String())
	}
	nonce, ct, err := env.st.BotToken(context.Background())
	if err != nil || nonce != nil || ct != nil {
		t.Fatalf("bad token stored nonce=%v ct=%v err=%v", nonce, ct, err)
	}

	rec = call(t, env.web, http.MethodPut, "/api/token", `{"token":"`+goodToken+`"}`, session, csrf, true)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), goodToken) {
		t.Fatalf("save %d %s", rec.Code, rec.Body.String())
	}
	var savedUser kook.User
	mustData(t, rec, &savedUser)
	if savedUser.Username != "小明" || savedUser.IdentifyNum != "0001" || savedUser.ID != "42" {
		t.Fatalf("user %+v", savedUser)
	}
	nonce, ct, err = env.st.BotToken(context.Background())
	if err != nil || len(ct) == 0 || bytes.Equal(ct, []byte(goodToken)) || bytes.Contains(ct, []byte(goodToken)) {
		t.Fatalf("ciphertext stored wrong")
	}
	plain, err := env.box.Open(secret.PurposeBotToken, nonce, ct)
	if err != nil || string(plain) != goodToken {
		t.Fatal("decrypt")
	}
	fp, err := env.st.SecretFP(context.Background())
	if err != nil || fp != secret.Fingerprint([]byte(panelSecret)) {
		t.Fatalf("fp %q", fp)
	}
	if changes != 1 {
		t.Fatalf("onchange %d", changes)
	}
	if strings.Contains(env.logs.String(), goodToken) || strings.Contains(env.logs.String(), badToken) {
		t.Fatalf("logs leaked token: %s", env.logs.String())
	}

	rec = call(t, env.web, http.MethodGet, "/api/status", "", session, csrf, false)
	var status StatusView
	mustData(t, rec, &status)
	if !status.TokenSet || !status.Verified || status.Mismatch || status.Gateway != "down" || status.Voice != "idle" {
		t.Fatalf("status %+v", status)
	}
	if status.Identity == nil || status.Identity.Username != "小明" || strings.Contains(rec.Body.String(), goodToken) {
		t.Fatalf("identity %s", rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(session)
	rec = httptest.NewRecorder()
	env.web.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "HTTP 凭证已验证，网关未连接。") || !strings.Contains(rec.Body.String(), "小明#0001") || !strings.Contains(rec.Body.String(), `href="/settings"`) {
		t.Fatalf("home %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), goodToken) {
		t.Fatal("home leaked token")
	}

	rec = call(t, env.web, http.MethodGet, "/settings", "", session, csrf, false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "测试") || !strings.Contains(rec.Body.String(), "语音频道") {
		t.Fatalf("settings page %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), goodToken) {
		t.Fatal("settings page leaked token")
	}

	sealed := append([]byte(nil), ct...)
	rec = call(t, env.web, http.MethodPost, "/api/token/test", `{"token":"`+otherToken+`"}`, session, csrf, true)
	var other kook.User
	mustData(t, rec, &other)
	if other.Username != "路人" || strings.Contains(rec.Body.String(), otherToken) {
		t.Fatalf("test other %s", rec.Body.String())
	}
	_, ct, err = env.st.BotToken(context.Background())
	if err != nil || !bytes.Equal(ct, sealed) {
		t.Fatal("explicit test replaced the saved token")
	}
	rec = call(t, env.web, http.MethodGet, "/api/status", "", session, csrf, false)
	mustData(t, rec, &status)
	if status.Identity == nil || status.Identity.Username != "小明" {
		t.Fatalf("identity changed %+v", status.Identity)
	}
	if !env.fake.saw("Bot " + otherToken) {
		t.Fatal("test did not send the explicit token")
	}

	rec = call(t, env.web, http.MethodGet, "/api/guilds", "", session, csrf, false)
	var guilds struct {
		Guilds []kook.Guild `json:"guilds"`
	}
	mustData(t, rec, &guilds)
	if len(guilds.Guilds) != 2 || guilds.Guilds[0].Name != "服务器甲" || guilds.Guilds[1].Name != "服务器乙" {
		t.Fatalf("guilds %+v", guilds.Guilds)
	}
	rec = call(t, env.web, http.MethodGet, "/api/guilds/nope/channels", "", session, csrf, false)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_channel_type") {
		t.Fatalf("bad guild %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, env.web, http.MethodGet, "/api/guilds/1/channels", "", session, csrf, false)
	var groups kook.ChannelGroups
	mustData(t, rec, &groups)
	if len(groups.Text) != 1 || groups.Text[0].ParentName != "分组甲" {
		t.Fatalf("text %+v", groups.Text)
	}
	var squad *kook.ListedChannel
	for i := range groups.Voice {
		if groups.Voice[i].ID == "25" {
			squad = &groups.Voice[i]
		}
		if groups.Voice[i].ID == "20" && groups.Voice[i].ParentName != "分组甲" {
			t.Fatalf("voice parent %+v", groups.Voice[i])
		}
	}
	if squad == nil || !squad.HasPassword || squad.ParentName != "分组甲" {
		t.Fatalf("squad %+v", squad)
	}

	hits := env.fake.hits()
	rec = call(t, env.web, http.MethodPut, "/api/binding", `{"guild_id":"1","voice_channel_id":"20","mirror_enabled":true,"mirror_format":"kmarkdown"}`, session, csrf, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "mirror_needs_text_channel") || env.fake.hits() != hits {
		t.Fatalf("mirror %d hits %d->%d %s", rec.Code, hits, env.fake.hits(), rec.Body.String())
	}
	rec = call(t, env.web, http.MethodPut, "/api/binding", `{"guild_id":"1","voice_channel_id":"20","text_channel_id":"21"}`, session, csrf, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "voice_password_required") {
		t.Fatalf("password %d %s", rec.Code, rec.Body.String())
	}

	body := `{"guild_id":"1","voice_channel_id":"20","text_channel_id":"21","voice_password":"` + roomPass + `","mirror_enabled":false,"mirror_format":"card"}`
	rec = call(t, env.web, http.MethodPut, "/api/binding", body, session, csrf, true)
	var view bindingView
	mustData(t, rec, &view)
	if view.GuildName != "服务器甲" || view.VoiceChannelName != "来自视图" || view.TextChannelName != "综合" || !view.VoicePasswordSet || view.MirrorFormat != "card" {
		t.Fatalf("binding %+v", view)
	}
	if strings.Contains(rec.Body.String(), roomPass) {
		t.Fatal("binding response contains password")
	}
	stored, err := env.st.Binding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	opened, err := env.box.Open(secret.PurposeVoicePassword, stored.VoicePasswordNonce, stored.VoicePasswordCT)
	if err != nil || string(opened) != roomPass {
		t.Fatal("password decrypt")
	}
	kept := append([]byte(nil), stored.VoicePasswordCT...)

	rec = call(t, env.web, http.MethodPut, "/api/binding", `{"guild_id":"1","voice_channel_id":"20","text_channel_id":"21","mirror_format":"card"}`, session, csrf, true)
	mustData(t, rec, &view)
	stored, err = env.st.Binding(context.Background())
	if err != nil || !view.VoicePasswordSet || !bytes.Equal(stored.VoicePasswordCT, kept) {
		t.Fatal("omit password did not keep ciphertext")
	}
	rec = call(t, env.web, http.MethodPut, "/api/binding", `{"guild_id":"1","voice_channel_id":"20","text_channel_id":"21","voice_password":"********","mirror_format":"card"}`, session, csrf, true)
	mustData(t, rec, &view)
	stored, err = env.st.Binding(context.Background())
	if err != nil || !bytes.Equal(stored.VoicePasswordCT, kept) {
		t.Fatal("mask replaced password")
	}
	rec = call(t, env.web, http.MethodPut, "/api/binding", `{"guild_id":"1","voice_channel_id":"20","text_channel_id":"21","voice_password":"","mirror_format":"card"}`, session, csrf, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "voice_password_required") {
		t.Fatalf("empty password %d %s", rec.Code, rec.Body.String())
	}

	rec = call(t, env.web, http.MethodPut, "/api/binding", `{"guild_id":"1","voice_channel_id":"23","text_channel_id":"21","mirror_format":"kmarkdown"}`, session, csrf, true)
	mustData(t, rec, &view)
	if view.VoiceChannelName != "闲聊" || view.VoicePasswordSet {
		t.Fatalf("cleared %+v", view)
	}
	rec = call(t, env.web, http.MethodGet, "/api/binding", "", session, csrf, false)
	mustData(t, rec, &view)
	if strings.Contains(rec.Body.String(), roomPass) || view.VoicePasswordSet || view.VoiceChannelID != "23" {
		t.Fatalf("get binding %s", rec.Body.String())
	}
	rec = call(t, env.web, http.MethodPut, "/api/binding", `{"guild_id":"1","voice_channel_id":"20","text_channel_id":"21"}`, session, csrf, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "voice_password_required") {
		t.Fatalf("reenter %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, env.web, http.MethodGet, "/api/binding", "", session, csrf, false)
	mustData(t, rec, &view)
	if view.VoiceChannelID != "23" {
		t.Fatalf("rejected save changed binding %+v", view)
	}
	rec = call(t, env.web, http.MethodPut, "/api/binding", `{"guild_id":"1","voice_channel_id":"22","text_channel_id":"21"}`, session, csrf, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_channel_type") {
		t.Fatalf("bad type %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, env.web, http.MethodPut, "/api/binding", `{"guild_id":"1","voice_channel_id":"24","text_channel_id":"21"}`, session, csrf, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_channel_type") {
		t.Fatalf("wrong guild %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, env.web, http.MethodPut, "/api/binding", `{"guild_id":"999","voice_channel_id":"20","text_channel_id":"21","voice_password":"x"}`, session, csrf, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_channel_type") {
		t.Fatalf("missing guild %d %s", rec.Code, rec.Body.String())
	}

	rec = call(t, env.web, http.MethodPut, "/api/token", `{"token":""}`, session, csrf, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cleared":true`) {
		t.Fatalf("clear %d %s", rec.Code, rec.Body.String())
	}
	nonce, ct, err = env.st.BotToken(context.Background())
	if err != nil || nonce != nil || ct != nil {
		t.Fatal("token not cleared")
	}
	fp, err = env.st.SecretFP(context.Background())
	if err != nil || fp != secret.Fingerprint([]byte(panelSecret)) || changes != 2 {
		t.Fatalf("fp %q changes %d", fp, changes)
	}
	rec = call(t, env.web, http.MethodGet, "/api/status", "", session, csrf, false)
	mustData(t, rec, &status)
	if status.TokenSet || status.Verified || status.Identity != nil {
		t.Fatalf("status after clear %+v", status)
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(session)
	rec = httptest.NewRecorder()
	env.web.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "还没有绑定 KOOK 机器人。") {
		t.Fatalf("home after clear %s", rec.Body.String())
	}
	if strings.Contains(env.logs.String(), goodToken) || strings.Contains(env.logs.String(), otherToken) || strings.Contains(env.logs.String(), roomPass) || strings.Contains(env.logs.String(), badToken) {
		t.Fatalf("logs leaked: %s", env.logs.String())
	}
}

func TestMismatchSkipsKOOKUntilRetoken(t *testing.T) {
	otherKey := []byte("abcdefghijklmnopqrstuvwxyz012345")
	env := newPanelPrep(t, func(st *store.Store, box *secret.Box) {
		other, err := secret.New(otherKey)
		if err != nil {
			t.Fatal(err)
		}
		nonce, ct, err := other.Seal(secret.PurposeBotToken, []byte("sealed-with-old-secret-zzzzzzzz"))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetBotToken(context.Background(), nonce, ct, secret.Fingerprint(otherKey)); err != nil {
			t.Fatal(err)
		}
		_ = box
	})
	before := env.fake.hits()
	env.creds.Refresh(context.Background(), env.client)
	if env.fake.hits() != before {
		t.Fatal("refresh called kook on mismatch")
	}
	session, csrf := loginPanel(t, env.web)
	rec := call(t, env.web, http.MethodGet, "/api/guilds", "", session, csrf, false)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "secret_mismatch") {
		t.Fatalf("guilds %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sealed-with-old-secret") {
		t.Fatal("mismatch response leaked token")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(session)
	rec = httptest.NewRecorder()
	env.web.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "密钥已更换，需要重新填写 Token。") {
		t.Fatalf("home %s", rec.Body.String())
	}
	rec = call(t, env.web, http.MethodPut, "/api/token", `{"token":"`+goodToken+`"}`, session, csrf, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("retoken %d %s", rec.Code, rec.Body.String())
	}
	fp, err := env.st.SecretFP(context.Background())
	if err != nil || fp != secret.Fingerprint([]byte(panelSecret)) {
		t.Fatalf("fp %q", fp)
	}
	nonce, ct := mustBlob(t, env.st)
	plain, err := env.box.Open(secret.PurposeBotToken, nonce, ct)
	if err != nil || string(plain) != goodToken {
		t.Fatal("new token did not open with the current secret")
	}
	rec = call(t, env.web, http.MethodGet, "/", "", session, csrf, false)
	if !strings.Contains(rec.Body.String(), "HTTP 凭证已验证，网关未连接。") {
		t.Fatalf("home after retoken %s", rec.Body.String())
	}
}

func TestRefreshCachesSavedToken(t *testing.T) {
	env := newPanel(t)
	nonce, ct, err := env.box.Seal(secret.PurposeBotToken, []byte(goodToken))
	if err != nil {
		t.Fatal(err)
	}
	if err := env.st.SetBotToken(context.Background(), nonce, ct, secret.Fingerprint([]byte(panelSecret))); err != nil {
		t.Fatal(err)
	}
	creds, err := LoadCredentials(context.Background(), env.st, env.box)
	if err != nil {
		t.Fatal(err)
	}
	if creds.mismatch || creds.token != goodToken {
		t.Fatal("load")
	}
	creds.Refresh(context.Background(), env.client)
	st := creds.Public("down", "idle")
	if !st.Verified || st.Identity == nil || st.Identity.Username != "小明" {
		t.Fatalf("%+v", st)
	}
	if strings.Contains(st.Identity.Avatar, goodToken) {
		t.Fatal("avatar")
	}
}

type panelEnv struct {
	web    *Server
	st     *store.Store
	box    *secret.Box
	client *kook.Client
	creds  *Credentials
	fake   *fakeKOOK
	logs   *bytes.Buffer
}

func newPanel(t *testing.T) *panelEnv {
	t.Helper()
	return newPanelPrep(t, nil)
}

func newPanelPrep(t *testing.T, prep func(*store.Store, *secret.Box)) *panelEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kookbot.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secret.New([]byte(panelSecret))
	if err != nil {
		t.Fatal(err)
	}
	if prep != nil {
		prep(st, box)
	}
	creds, err := LoadCredentials(context.Background(), st, box)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeKOOK{
		badToken: badToken,
		users:    map[string]kook.User{},
	}
	httpSrv := httptest.NewServer(fake)
	t.Cleanup(httpSrv.Close)
	u, err := url.Parse(httpSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	client := kook.New("https://www.kookapp.cn/api", logger)
	client.SetRoundTripper(kook.RewriteTransport(u, httpSrv.Client().Transport))
	web, err := New(Deps{
		Config: config.Config{UIPassword: "pw", Secret: panelSecret, HTTPAddr: "127.0.0.1:8765"},
		Store:  st,
		Log:    logger,
		Status: DownStatus{},
		Box:    box,
		Kook:   client,
		Creds:  creds,
	}, []MountFunc{MountPanel})
	if err != nil {
		t.Fatal(err)
	}
	return &panelEnv{web: web, st: st, box: box, client: client, creds: creds, fake: fake, logs: &logs}
}

func loginPanel(t *testing.T, srv *Server) (*http.Cookie, *http.Cookie) {
	t.Helper()
	rec := postJSON(t, srv, "/api/login", `{"password":"pw"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %d %s", rec.Code, rec.Body.String())
	}
	var session, csrf *http.Cookie
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case sessionCookie:
			session = c
		case csrfCookie:
			csrf = c
		}
	}
	if session == nil || csrf == nil {
		t.Fatal("missing cookies")
	}
	return session, csrf
}

func call(t *testing.T, srv *Server, method, path, body string, session, csrf *http.Cookie, useCSRF bool) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = "192.0.2.20:9"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if session != nil {
		req.AddCookie(session)
	}
	if csrf != nil {
		req.AddCookie(csrf)
	}
	if useCSRF && csrf != nil {
		req.Header.Set("X-CSRF-Token", csrf.Value)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func mustData(t *testing.T, rec *httptest.ResponseRecorder, dest any) {
	t.Helper()
	var env struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("json %s", rec.Body.String())
	}
	if rec.Code != http.StatusOK || !env.OK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(env.Data, dest); err != nil {
		t.Fatalf("data %s", rec.Body.String())
	}
}

func assertKookErr(t *testing.T, rec *httptest.ResponseRecorder, code int, message string) {
	t.Helper()
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Data struct {
			KookCode    int    `json:"kook_code"`
			KookMessage string `json:"kook_message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("json %s", rec.Body.String())
	}
	if rec.Code != http.StatusOK || env.OK || env.Error.Code != "kook_error" || env.Data.KookCode != code || env.Data.KookMessage != message {
		t.Fatalf("%d %+v", rec.Code, env)
	}
}

func mustBlob(t *testing.T, st *store.Store) ([]byte, []byte) {
	t.Helper()
	nonce, ct, err := st.BotToken(context.Background())
	if err != nil || len(nonce) == 0 || len(ct) == 0 {
		t.Fatal("missing blob")
	}
	return nonce, ct
}

type fakeKOOK struct {
	mu       sync.Mutex
	badToken string
	users    map[string]kook.User
	hitN     int
	auths    []string
}

func (f *fakeKOOK) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hitN++
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	switch r.URL.Path {
	case "/api/v3/user/me":
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bot ")
		if tok == f.badToken {
			writeEnv(w, 40001, "无效的 token", nil)
			return
		}
		user := kook.User{ID: "42", Username: "小明", IdentifyNum: "0001", Avatar: "https://img.example/a.png"}
		if u, ok := f.users[tok]; ok {
			user = u
		}
		writeEnv(w, 0, "操作成功", user)
	case "/api/v3/guild/list":
		var items []kook.Guild
		switch r.URL.Query().Get("page") {
		case "1":
			items = []kook.Guild{{ID: "1", Name: "服务器甲", Icon: "i"}}
		case "2":
			items = []kook.Guild{{ID: "2", Name: "服务器乙"}}
		}
		writeEnv(w, 0, "操作成功", map[string]any{
			"items": items,
			"meta":  map[string]int{"page": 1, "page_total": 2, "page_size": 50, "total": 2},
		})
	case "/api/v3/channel/list":
		if r.URL.Query().Get("page") != "1" {
			writeEnv(w, 0, "操作成功", map[string]any{
				"items": []any{},
				"meta":  map[string]int{"page": 2, "page_total": 1, "page_size": 50, "total": 0},
			})
			return
		}
		var items []any
		if r.URL.Query().Get("type") == "1" {
			items = []any{map[string]any{"id": "21", "name": "综合", "type": 1, "guild_id": "1", "parent_id": "9", "has_password": false}}
		} else {
			items = []any{
				map[string]any{"id": "20", "name": "列表名不要用", "type": 2, "guild_id": "1", "parent_id": "9", "has_password": true},
				map[string]any{"id": "23", "name": "闲聊", "type": 2, "guild_id": "1", "parent_id": "9", "has_password": false},
				map[string]any{"id": "25", "name": "小队", "type": 2, "guild_id": "1", "parent_id": "9"},
			}
		}
		writeEnv(w, 0, "操作成功", map[string]any{
			"items": items,
			"meta":  map[string]int{"page": 1, "page_total": 1, "page_size": 50, "total": len(items)},
		})
	case "/api/v3/channel/view":
		switch r.URL.Query().Get("target_id") {
		case "9":
			writeEnv(w, 0, "操作成功", map[string]any{"id": "9", "name": "分组甲", "type": 0, "guild_id": "1", "is_category": true})
		case "20":
			writeEnv(w, 0, "操作成功", map[string]any{"id": "20", "name": "来自视图", "type": 2, "guild_id": "1", "parent_id": "9", "has_password": true})
		case "21":
			writeEnv(w, 0, "操作成功", map[string]any{"id": "21", "name": "综合", "type": 1, "guild_id": "1", "parent_id": "9", "has_password": false})
		case "22":
			writeEnv(w, 0, "操作成功", map[string]any{"id": "22", "name": "文字假扮", "type": 1, "guild_id": "1", "has_password": false})
		case "23":
			writeEnv(w, 0, "操作成功", map[string]any{"id": "23", "name": "闲聊", "type": 2, "guild_id": "1", "parent_id": "9", "has_password": false})
		case "24":
			writeEnv(w, 0, "操作成功", map[string]any{"id": "24", "name": "别人的房间", "type": 2, "guild_id": "2", "has_password": false})
		case "25":
			writeEnv(w, 0, "操作成功", map[string]any{"id": "25", "name": "小队", "type": 2, "guild_id": "1", "parent_id": "9", "has_password": true})
		default:
			writeEnv(w, 40000, "频道不存在", nil)
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeKOOK) hits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hitN
}

func (f *fakeKOOK) saw(auth string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, got := range f.auths {
		if got == auth {
			return true
		}
	}
	return false
}

func writeEnv(w http.ResponseWriter, code int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message, "data": data})
}
