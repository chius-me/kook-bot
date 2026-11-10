package kook

import (
	"bytes"
	"encoding/json"
	"errors"

	"bsdx/internal/module"
)

const (
	snBufCap  = 200
	snFailCap = 8
)

// snCursor keeps the highest processed sn. It only moves forward on the next
// expected sn. Duplicates and ahead-of-time frames do not move it.
type snCursor struct {
	maxSn int64
	fails int
	buf   map[int64]module.Event
}

type snPush struct {
	Events []module.Event
	Poison bool
	Fatal  bool
}

func (c *snCursor) Push(sn int64, d json.RawMessage) snPush {
	next := c.maxSn + 1
	if sn <= c.maxSn {
		return snPush{}
	}
	if sn > next {
		ev, err := decodeEvent(sn, d)
		if err != nil {
			return snPush{}
		}
		if c.buf == nil {
			c.buf = map[int64]module.Event{}
		}
		if _, ok := c.buf[sn]; ok {
			return snPush{}
		}
		if len(c.buf) >= snBufCap {
			c.buf = map[int64]module.Event{}
			c.maxSn = 0
			c.fails = 0
			return snPush{Fatal: true}
		}
		c.buf[sn] = ev
		return snPush{}
	}
	ev, err := decodeEvent(sn, d)
	if err != nil {
		c.maxSn = sn
		c.fails++
		more := c.drain()
		return snPush{Events: more, Poison: true, Fatal: c.fails >= snFailCap}
	}
	c.maxSn = sn
	c.fails = 0
	events := []module.Event{ev}
	events = append(events, c.drain()...)
	return snPush{Events: events}
}

func (c *snCursor) drain() []module.Event {
	var out []module.Event
	for {
		ev, ok := c.buf[c.maxSn+1]
		if !ok {
			return out
		}
		delete(c.buf, c.maxSn+1)
		c.maxSn++
		c.fails = 0
		out = append(out, ev)
	}
}

func (c *snCursor) reset() {
	c.maxSn = 0
	c.fails = 0
	c.buf = map[int64]module.Event{}
}

func decodeEvent(sn int64, d json.RawMessage) (module.Event, error) {
	d = bytes.TrimSpace(d)
	if len(d) == 0 || bytes.Equal(d, []byte("null")) {
		return module.Event{}, errors.New("missing d")
	}
	var body struct {
		Type     int    `json:"type"`
		TargetID string `json:"target_id"`
		AuthorID string `json:"author_id"`
		Extra    *struct {
			Type    json.RawMessage `json:"type"`
			Body    json.RawMessage `json:"body"`
			GuildID string          `json:"guild_id"`
		} `json:"extra"`
	}
	if err := json.Unmarshal(d, &body); err != nil {
		return module.Event{}, err
	}
	if body.Extra == nil {
		return module.Event{}, errors.New("missing extra")
	}
	extraType, err := module.NormalizeExtraType(body.Extra.Type)
	if err != nil {
		return module.Event{}, err
	}
	var inner struct {
		ChannelID string `json:"channel_id"`
		UserID    string `json:"user_id"`
		GuildID   string `json:"guild_id"`
	}
	if len(bytes.TrimSpace(body.Extra.Body)) > 0 && !bytes.Equal(bytes.TrimSpace(body.Extra.Body), []byte("null")) {
		_ = json.Unmarshal(body.Extra.Body, &inner)
	}
	channelID := inner.ChannelID
	if channelID == "" {
		channelID = body.TargetID
	}
	author := inner.UserID
	if author == "" {
		author = body.AuthorID
	}
	guild := inner.GuildID
	if guild == "" {
		guild = body.Extra.GuildID
	}
	return module.Event{
		SN:        sn,
		Type:      body.Type,
		ExtraType: extraType,
		ChannelID: channelID,
		GuildID:   guild,
		AuthorID:  author,
		Body:      append(json.RawMessage(nil), body.Extra.Body...),
	}, nil
}
