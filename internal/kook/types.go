package kook

import "fmt"

// Channel kinds on the channel object. List queries use 1 and 2 only.
const (
	ChannelText  = 1
	ChannelVoice = 2
)

// User is the GET user/me object. IDs stay strings.
type User struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	IdentifyNum string `json:"identify_num"`
	Avatar      string `json:"avatar"`
}

// Guild is one row of guild/list.
type Guild struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
}

// Channel is a KOOK channel object from list or view.
// HasPassword is nil when the payload omits the field.
type Channel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        int    `json:"type"`
	GuildID     string `json:"guild_id"`
	ParentID    string `json:"parent_id"`
	HasPassword *bool  `json:"has_password"`
	IsCategory  bool   `json:"is_category"`
}

// ListedChannel is a text or voice channel plus the parent group name.
type ListedChannel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        int    `json:"type"`
	GuildID     string `json:"guild_id"`
	ParentID    string `json:"parent_id"`
	ParentName  string `json:"parent_name"`
	HasPassword bool   `json:"has_password"`
}

// ChannelGroups splits a guild into the two lists the settings page shows.
type ChannelGroups struct {
	Text  []ListedChannel `json:"text"`
	Voice []ListedChannel `json:"voice"`
}

type pageMeta struct {
	Page      int `json:"page"`
	PageTotal int `json:"page_total"`
	PageSize  int `json:"page_size"`
	Total     int `json:"total"`
}

type envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    []byte `json:"-"`
}

// APIError is a KOOK HTTP or business failure.
// Error text is the KOOK message when there is one. It does not include the token.
type APIError struct {
	HTTPStatus  int
	Code        int
	Message     string
	Unavailable bool
}

func (e *APIError) Error() string {
	if e == nil {
		return "kook error"
	}
	if e.Message != "" && !e.Unavailable {
		return e.Message
	}
	if e.Code != 0 && !e.Unavailable {
		return fmt.Sprintf("kook code %d", e.Code)
	}
	return "kook unavailable"
}
