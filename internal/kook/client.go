package kook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	requestTimeout = 10 * time.Second
	pageSize       = 50
	maxListPages   = 20
	maxRateWait    = 3 * time.Second
	maxBody        = 1 << 20
)

// Client calls the KOOK HTTP API. The token is an argument, not stored here.
type Client struct {
	baseURL string
	http    *http.Client
	log     *slog.Logger
	sleep   func(context.Context, time.Duration) error
	now     func() time.Time
}

func New(baseURL string, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	return &Client{
		baseURL: baseURL,
		log:     log,
		http: &http.Client{
			Timeout: requestTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		sleep: defaultSleep,
		now:   time.Now,
	}
}

// SetRoundTripper replaces the transport. Tests point it at httptest after
// endpoint has already required www.kookapp.cn. It does not widen the whitelist.
func (c *Client) SetRoundTripper(rt http.RoundTripper) {
	c.http.Transport = rt
}

// RewriteTransport sends an already-validated request to base.
// Production code does not call it. The request URL is still built for www.kookapp.cn.
func RewriteTransport(base *url.URL, next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		cloned.URL.Scheme = base.Scheme
		cloned.URL.Host = base.Host
		cloned.Host = base.Host
		return next.RoundTrip(cloned)
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func defaultSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) endpoint(route string) (string, error) {
	base := strings.TrimRight(c.baseURL, "/")
	base = strings.TrimSuffix(base, "/v3")
	route = strings.TrimLeft(route, "/")
	u, err := url.Parse(base + "/v3/" + route)
	if err != nil {
		return "", fmt.Errorf("kook: bad endpoint")
	}
	// Host 含端口时也不等于 www.kookapp.cn。User 非空表示 URL 里带了 userinfo。
	if u.Scheme != "https" || u.User != nil || u.Host != "www.kookapp.cn" {
		return "", fmt.Errorf("kook: bad endpoint")
	}
	return u.String(), nil
}

// Me calls GET user/me.
func (c *Client) Me(ctx context.Context, token string) (User, error) {
	var user User
	if err := c.do(ctx, token, http.MethodGet, "user/me", nil, nil, &user); err != nil {
		return User{}, err
	}
	return user, nil
}

// Guilds merges guild/list pages.
func (c *Client) Guilds(ctx context.Context, token string) ([]Guild, error) {
	return getPage[Guild](c, ctx, token, "guild/list", nil)
}

// Channels lists text (type 1) and voice (type 2) channels.
// A missing parent name is filled from channel/view. has_password on the list is kept;
// when the list omits it, channel/view supplies it.
func (c *Client) Channels(ctx context.Context, token, guildID string) (ChannelGroups, error) {
	text, err := getPage[Channel](c, ctx, token, "channel/list", url.Values{
		"guild_id": {guildID},
		"type":     {"1"},
	})
	if err != nil {
		return ChannelGroups{}, err
	}
	voice, err := getPage[Channel](c, ctx, token, "channel/list", url.Values{
		"guild_id": {guildID},
		"type":     {"2"},
	})
	if err != nil {
		return ChannelGroups{}, err
	}
	views, err := c.viewsFor(ctx, token, append(append([]Channel{}, text...), voice...))
	if err != nil {
		return ChannelGroups{}, err
	}
	return ChannelGroups{
		Text:  listChannels(text, ChannelText, views),
		Voice: listChannels(voice, ChannelVoice, views),
	}, nil
}

// Channel calls GET channel/view.
func (c *Client) Channel(ctx context.Context, token, targetID string) (Channel, error) {
	var ch Channel
	q := url.Values{"target_id": {targetID}}
	if err := c.do(ctx, token, http.MethodGet, "channel/view", q, nil, &ch); err != nil {
		return Channel{}, err
	}
	return ch, nil
}

func (c *Client) viewsFor(ctx context.Context, token string, items []Channel) (map[string]Channel, error) {
	need := map[string]struct{}{}
	for _, ch := range items {
		if ch.HasPassword == nil && ch.ID != "" {
			need[ch.ID] = struct{}{}
		}
		if parent := parentID(ch.ParentID); parent != "" {
			need[parent] = struct{}{}
		}
	}
	views := make(map[string]Channel, len(need))
	for id := range need {
		ch, err := c.Channel(ctx, token, id)
		if err != nil {
			return nil, err
		}
		views[id] = ch
	}
	return views, nil
}

func listChannels(items []Channel, kind int, views map[string]Channel) []ListedChannel {
	out := make([]ListedChannel, 0, len(items))
	for _, ch := range items {
		if ch.ID == "" || ch.Type != kind {
			continue
		}
		hp := false
		if ch.HasPassword != nil {
			hp = *ch.HasPassword
		} else if v, ok := views[ch.ID]; ok && v.HasPassword != nil {
			hp = *v.HasPassword
		}
		parent := parentID(ch.ParentID)
		out = append(out, ListedChannel{
			ID:          ch.ID,
			Name:        ch.Name,
			Type:        ch.Type,
			GuildID:     ch.GuildID,
			ParentID:    parent,
			ParentName:  views[parent].Name,
			HasPassword: hp,
		})
	}
	return out
}

func parentID(id string) string {
	if id == "" || id == "0" {
		return ""
	}
	return id
}

func getPage[T any](c *Client, ctx context.Context, token, route string, base url.Values) ([]T, error) {
	var all []T
	for page := 1; page <= maxListPages; page++ {
		q := cloneValues(base)
		q.Set("page", strconv.Itoa(page))
		q.Set("page_size", strconv.Itoa(pageSize))
		var data struct {
			Items []T      `json:"items"`
			Meta  pageMeta `json:"meta"`
		}
		if err := c.do(ctx, token, http.MethodGet, route, q, nil, &data); err != nil {
			return nil, err
		}
		all = append(all, data.Items...)
		n := len(data.Items)
		if data.Meta.PageTotal > 0 && page >= data.Meta.PageTotal {
			break
		}
		if n == 0 || (data.Meta.PageTotal == 0 && n < pageSize) {
			break
		}
	}
	if all == nil {
		all = []T{}
	}
	return all, nil
}

func cloneValues(src url.Values) url.Values {
	dst := make(url.Values, len(src))
	for k, vs := range src {
		dst[k] = append([]string(nil), vs...)
	}
	return dst
}

// do performs one KOOK call. body is reserved for later POST routes; PR2 passes nil.
// A 429 is retried once when X-Rate-Limit-Reset is a wait of at most 3 seconds.
func (c *Client) do(ctx context.Context, token, method, route string, query url.Values, body []byte, dest any) error {
	if strings.TrimSpace(token) == "" {
		return &APIError{Unavailable: true}
	}
	rawURL, err := c.endpoint(route)
	if err != nil {
		return err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("kook: bad endpoint")
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	for attempt := 0; attempt < 2; attempt++ {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
		if err != nil {
			return &APIError{Unavailable: true}
		}
		req.Header.Set("Authorization", "Bot "+token)
		req.Header.Set("Accept-Language", "zh-CN")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		start := time.Now()
		status, header, payload, err := c.round(req)
		c.logCall(method, u.Path, status, codeOf(payload), time.Since(start))
		if err != nil {
			return &APIError{Unavailable: true}
		}
		if status == http.StatusTooManyRequests && attempt == 0 {
			if wait, ok := c.retryDelay(header.Get("X-Rate-Limit-Reset")); ok {
				if err := c.sleep(ctx, wait); err != nil {
					return &APIError{Unavailable: true}
				}
				continue
			}
		}
		env, ok := parseEnvelope(payload)
		if !ok {
			return &APIError{HTTPStatus: status, Unavailable: true}
		}
		if status != http.StatusOK || env.Code != 0 {
			ae := &APIError{HTTPStatus: status, Code: env.Code, Message: env.Message}
			if env.Code == 0 {
				ae.Unavailable = true
			}
			return ae
		}
		if dest == nil {
			return nil
		}
		if len(env.Data) == 0 || bytes.Equal(bytes.TrimSpace(env.Data), []byte("null")) {
			return &APIError{Unavailable: true}
		}
		if err := json.Unmarshal(env.Data, dest); err != nil {
			return &APIError{Unavailable: true}
		}
		return nil
	}
	return &APIError{Unavailable: true}
}

func (c *Client) round(req *http.Request) (int, http.Header, []byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, resp.Header.Clone(), nil, err
	}
	return resp.StatusCode, resp.Header.Clone(), payload, nil
}

func (c *Client) logCall(method, path string, status, code int, latency time.Duration) {
	if c.log == nil {
		return
	}
	c.log.Info("kook",
		"component", "kook",
		"method", method,
		"path", path,
		"status", status,
		"kook_code", code,
		"latency_ms", latency.Milliseconds(),
	)
}

func codeOf(payload []byte) int {
	env, ok := parseEnvelope(payload)
	if !ok {
		return 0
	}
	return env.Code
}

func parseEnvelope(payload []byte) (envelope, bool) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return envelope{}, false
	}
	var raw struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return envelope{}, false
	}
	return envelope{Code: raw.Code, Message: raw.Message, Data: []byte(raw.Data)}, true
}

// retryDelay accepts a small second count, or a unix timestamp whose delay is at most 3 seconds.
func (c *Client) retryDelay(header string) (time.Duration, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(header, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0, false
	}
	var wait time.Duration
	if v > 1_000_000_000 {
		sec, frac := math.Modf(v)
		wait = time.Unix(int64(sec), int64(frac*float64(time.Second))).Sub(c.now())
	} else {
		wait = time.Duration(v * float64(time.Second))
	}
	if wait <= 0 || wait > maxRateWait {
		return 0, false
	}
	return wait, true
}
