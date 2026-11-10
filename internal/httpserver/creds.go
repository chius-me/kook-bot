package httpserver

import (
	"context"
	"errors"
	"sync"

	"bsdx/internal/kook"
	"bsdx/internal/secret"
	"bsdx/internal/store"
)

// Credentials is the in-memory bot token. PR3 reads it and sets OnChange
// to reconnect the gateway. This PR does not open a websocket.
type Credentials struct {
	mu       sync.Mutex
	gen      int
	token    string
	user     kook.User
	verified bool
	mismatch bool
	lastErr  string
	onChange func()
}

// LoadCredentials opens the sealed token. A decrypt failure is mismatch,
// not a process error, and the plaintext is discarded.
func LoadCredentials(ctx context.Context, st *store.Store, box *secret.Box) (*Credentials, error) {
	c := &Credentials{}
	if st == nil || box == nil {
		return c, nil
	}
	nonce, ct, err := st.BotToken(ctx)
	if err != nil {
		return nil, err
	}
	if len(nonce) == 0 || len(ct) == 0 {
		return c, nil
	}
	plain, err := box.Open(secret.PurposeBotToken, nonce, ct)
	if err != nil {
		c.mismatch = true
		return c, nil
	}
	c.token = string(plain)
	return c, nil
}

// SetOnChange registers the hook PR3 will use to reconnect. nil clears it.
func (c *Credentials) SetOnChange(fn func()) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.onChange = fn
	c.mu.Unlock()
}

func (c *Credentials) fire() {
	c.mu.Lock()
	fn := c.onChange
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (c *Credentials) snapshot() (token string, gen int, mismatch bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token, c.gen, c.mismatch
}

// Token is the decrypted bot token. The bool is false when it is missing or the secret changed.
func (c *Credentials) Token() (string, bool) {
	token, code := c.usable()
	return token, code == ""
}

// BotID is the user/me id kept in memory. It is empty until verification succeeds.
func (c *Credentials) BotID() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.verified || c.mismatch {
		return ""
	}
	return c.user.ID
}

func (c *Credentials) usable() (string, string) {
	if c == nil {
		return "", "token_missing"
	}
	token, _, mismatch := c.snapshot()
	if mismatch {
		return "", "secret_mismatch"
	}
	if token == "" {
		return "", "token_missing"
	}
	return token, ""
}

// Commit stores a token that user/me already accepted.
func (c *Credentials) Commit(token string, user kook.User) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.gen++
	c.token = token
	c.user = user
	c.verified = true
	c.mismatch = false
	c.lastErr = ""
	c.mu.Unlock()
	c.fire()
}

// Clear drops the in-memory token. The caller clears the database first.
func (c *Credentials) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.gen++
	c.token = ""
	c.user = kook.User{}
	c.verified = false
	c.mismatch = false
	c.lastErr = ""
	c.mu.Unlock()
	c.fire()
}

func (c *Credentials) observe(gen int, token string, user kook.User, err error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen || c.token != token || c.mismatch {
		return
	}
	if err != nil {
		c.verified = false
		c.user = kook.User{}
		var api *kook.APIError
		if errors.As(err, &api) && !api.Unavailable && api.Code != 0 {
			c.lastErr = api.Message
		} else {
			c.lastErr = ""
		}
		return
	}
	c.user = user
	c.verified = true
	c.lastErr = ""
}

// Refresh calls user/me when a token is loaded. It does nothing without one,
// and it does not block process startup.
func (c *Credentials) Refresh(ctx context.Context, client *kook.Client) {
	if c == nil || client == nil {
		return
	}
	token, gen, mismatch := c.snapshot()
	if token == "" || mismatch {
		return
	}
	user, err := client.Me(ctx, token)
	c.observe(gen, token, user, err)
}

// StatusView is GET /api/status. It never includes the token.
type StatusView struct {
	TokenSet      bool       `json:"token_set"`
	Verified      bool       `json:"verified"`
	Mismatch      bool       `json:"mismatch"`
	Gateway       string     `json:"gateway"`
	Voice         string     `json:"voice"`
	LastKookError string     `json:"last_kook_error"`
	Identity      *kook.User `json:"identity"`
}

func (c *Credentials) Public(gateway, voice string) StatusView {
	if c == nil {
		return StatusView{Gateway: gateway, Voice: voice}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := StatusView{
		TokenSet:      c.token != "" || c.mismatch,
		Verified:      c.verified && !c.mismatch,
		Mismatch:      c.mismatch,
		Gateway:       gateway,
		Voice:         voice,
		LastKookError: c.lastErr,
	}
	if c.mismatch {
		st.Verified = false
		st.LastKookError = ""
	}
	if st.Verified {
		user := c.user
		st.Identity = &user
	}
	return st
}

// HomeView is the homepage copy for this PR. The gateway is not connected.
type HomeView struct {
	Line     string
	Identity string
	Err      string
}

func (c *Credentials) Home() HomeView {
	if c == nil {
		return HomeView{Line: "还没有绑定 KOOK 机器人。"}
	}
	st := c.Public("down", "idle")
	switch {
	case st.Mismatch:
		return HomeView{Line: "密钥已更换，需要重新填写 Token。"}
	case !st.TokenSet:
		return HomeView{Line: "还没有绑定 KOOK 机器人。"}
	case st.Verified && st.Identity != nil:
		return HomeView{
			Line:     "HTTP 凭证已验证，网关未连接。",
			Identity: st.Identity.Username + "#" + st.Identity.IdentifyNum,
		}
	case st.LastKookError != "":
		return HomeView{Line: "HTTP 凭证未能通过校验。", Err: st.LastKookError}
	default:
		return HomeView{Line: "HTTP 凭证已保存，网关未连接。"}
	}
}
