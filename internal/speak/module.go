package speak

import (
	"context"

	"bsdx/internal/module"
)

// Module listens for presence in the pinned voice channel. It does not speak.
type Module struct {
	Note func(channelID, userID string, joined bool)
}

func (m Module) ID() string { return "speak" }

func (m Module) Subscriptions() []module.Subscription {
	return []module.Subscription{
		{Type: 255, ExtraType: "joined_channel"},
		{Type: 255, ExtraType: "exited_channel"},
	}
}

func (m Module) Handle(_ context.Context, ev module.Event) error {
	if m.Note == nil {
		return nil
	}
	switch ev.ExtraType {
	case "joined_channel":
		m.Note(ev.ChannelID, ev.AuthorID, true)
	case "exited_channel":
		m.Note(ev.ChannelID, ev.AuthorID, false)
	}
	return nil
}
