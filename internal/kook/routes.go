package kook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// GatewayURL calls GET gateway/index?compress=0 and returns data.url.
// Callers must not log the returned URL.
func (c *Client) GatewayURL(ctx context.Context, token string) (string, error) {
	var data struct {
		URL string `json:"url"`
	}
	q := url.Values{"compress": {"0"}}
	if err := c.do(ctx, token, http.MethodGet, "gateway/index", q, nil, &data); err != nil {
		return "", err
	}
	if data.URL == "" {
		return "", &APIError{Unavailable: true}
	}
	return data.URL, nil
}

// VoiceJoin posts voice/join. The raw data object is returned so ports can be
// decoded from either strings or numbers. Password is omitted when empty.
func (c *Client) VoiceJoin(ctx context.Context, token, channelID, password string) (json.RawMessage, error) {
	body := map[string]any{
		"channel_id": channelID,
		"audio_ssrc": "1111",
		"audio_pt":   "111",
		"rtcp_mux":   true,
	}
	if password != "" {
		body["password"] = password
	}
	return c.postRaw(ctx, token, "voice/join", body)
}

// VoiceLeave posts voice/leave.
func (c *Client) VoiceLeave(ctx context.Context, token, channelID string) error {
	return c.post(ctx, token, "voice/leave", map[string]string{"channel_id": channelID})
}

// VoiceKeepAlive posts voice/keep-alive.
func (c *Client) VoiceKeepAlive(ctx context.Context, token, channelID string) error {
	return c.post(ctx, token, "voice/keep-alive", map[string]string{"channel_id": channelID})
}

// VoiceList returns data.items[].id. A missing or non-array items field is an
// empty list, not an error and not a guess at some other field.
func (c *Client) VoiceList(ctx context.Context, token string) ([]string, error) {
	var data struct {
		Items json.RawMessage `json:"items"`
	}
	if err := c.do(ctx, token, http.MethodGet, "voice/list", nil, nil, &data); err != nil {
		return nil, err
	}
	if len(data.Items) == 0 || string(data.Items) == "null" {
		return []string{}, nil
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data.Items, &rows); err != nil {
		return []string{}, nil
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ID != "" {
			out = append(out, row.ID)
		}
	}
	return out, nil
}

// ChannelUsers calls channel/user-list. data may be an array or {items:[]}.
func (c *Client) ChannelUsers(ctx context.Context, token, channelID string) ([]User, error) {
	var raw json.RawMessage
	q := url.Values{"channel_id": {channelID}}
	if err := c.do(ctx, token, http.MethodGet, "channel/user-list", q, nil, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return []User{}, nil
	}
	if raw[0] == '[' {
		var users []User
		if err := json.Unmarshal(raw, &users); err != nil {
			return nil, &APIError{Unavailable: true}
		}
		if users == nil {
			users = []User{}
		}
		return users, nil
	}
	var wrap struct {
		Items []User `json:"items"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, &APIError{Unavailable: true}
	}
	if wrap.Items == nil {
		wrap.Items = []User{}
	}
	return wrap.Items, nil
}

// CreateMessage posts message/create. typ 9 is KMarkdown, typ 10 is a card.
// The content is not logged.
func (c *Client) CreateMessage(ctx context.Context, token string, typ int, targetID, content string) error {
	return c.post(ctx, token, "message/create", map[string]any{
		"type":      typ,
		"target_id": targetID,
		"content":   content,
	})
}

func (c *Client) post(ctx context.Context, token, route string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return &APIError{Unavailable: true}
	}
	return c.do(ctx, token, http.MethodPost, route, nil, payload, nil)
}

func (c *Client) postRaw(ctx context.Context, token, route string, body any) (json.RawMessage, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, &APIError{Unavailable: true}
	}
	var raw json.RawMessage
	if err := c.do(ctx, token, http.MethodPost, route, nil, payload, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}
