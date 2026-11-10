package module

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode"
)

// Event is a gateway fact after extra.type has been normalized.
// Do not json.Unmarshal a raw frame directly into Event: extra.type may be a JSON number.
type Event struct {
	SN        int64
	Type      int
	ExtraType string
	ChannelID string
	GuildID   string
	AuthorID  string
	Body      json.RawMessage
}

// Subscription selects events. An empty ExtraType matches every event of Type.
// v1 modules must set ExtraType. The host still implements the empty match.
type Subscription struct {
	Type      int
	ExtraType string
}

// NormalizeExtraType turns a raw extra.type into the match key.
// A JSON string is unquoted. A JSON integer becomes its decimal text, so 1 becomes "1".
func NormalizeExtraType(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", errors.New("extra type missing")
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return s, nil
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var n json.Number
		if err := dec.Decode(&n); err != nil {
			return "", err
		}
		var extra any
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			return "", errors.New("extra type trailing data")
		}
		if !decimalInteger(string(n)) {
			return "", errors.New("extra type not an integer")
		}
		return string(n), nil
	default:
		return "", errors.New("extra type not string or number")
	}
}

func decimalInteger(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	if _, err := strconv.ParseInt(s, 10, 64); err != nil {
		return false
	}
	return true
}
