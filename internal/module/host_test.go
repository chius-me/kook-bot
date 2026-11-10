package module_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"bsdx/internal/module"
	"bsdx/internal/store"
)

type fake struct {
	id    string
	subs  []module.Subscription
	calls int
}

func (f *fake) ID() string                           { return f.id }
func (f *fake) Subscriptions() []module.Subscription { return f.subs }
func (f *fake) Handle(context.Context, module.Event) error {
	f.calls++
	return nil
}

func TestTextMessageHasNoSubscriber(t *testing.T) {
	extra, err := module.NormalizeExtraType(json.RawMessage(`1`))
	if err != nil {
		t.Fatal(err)
	}
	if extra != "1" {
		t.Fatalf("extra = %q", extra)
	}
	host := module.New()
	voice := &fake{id: "speak", subs: []module.Subscription{{Type: 255, ExtraType: "joined_channel"}}}
	if err := host.Register(voice); err != nil {
		t.Fatal(err)
	}
	err = host.Dispatch(context.Background(), module.Event{Type: 1, ExtraType: extra})
	if err != nil {
		t.Fatal(err)
	}
	if voice.calls != 0 {
		t.Fatalf("calls = %d", voice.calls)
	}
	if host.Unsubscribed() != 1 {
		t.Fatalf("unsubscribed = %d", host.Unsubscribed())
	}
}

func TestSpeakRowDoesNotRegisterAModule(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "kookbot.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	host := module.New()
	if host.Len() != 0 {
		t.Fatalf("len = %d", host.Len())
	}
	extra, err := module.NormalizeExtraType(json.RawMessage(`"joined_channel"`))
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Dispatch(context.Background(), module.Event{Type: 255, ExtraType: extra}); err != nil {
		t.Fatal(err)
	}
	if host.Unsubscribed() != 1 {
		t.Fatalf("unsubscribed = %d", host.Unsubscribed())
	}
}

func TestNormalizeExtraType(t *testing.T) {
	s, err := module.NormalizeExtraType(json.RawMessage(`"joined_channel"`))
	if err != nil || s != "joined_channel" {
		t.Fatalf("%q %v", s, err)
	}
	if _, err := module.NormalizeExtraType(json.RawMessage(`{"type":1}`)); err == nil {
		t.Fatal("object should fail")
	}
	if _, err := module.NormalizeExtraType(json.RawMessage(`1.5`)); err == nil {
		t.Fatal("float should fail")
	}
}
