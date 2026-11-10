package module

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// Module handles gateway facts. The host does not load modules from module_state.
type Module interface {
	ID() string
	Subscriptions() []Subscription
	Handle(ctx context.Context, ev Event) error
}

type registered struct {
	mod  Module
	subs []Subscription
}

// Host fans events out to modules registered in code.
type Host struct {
	mu           sync.RWMutex
	mods         []registered
	unsubscribed atomic.Uint64
}

func New() *Host { return &Host{} }

// Register snapshots subscriptions. The process starts with none.
func (h *Host) Register(m Module) error {
	if m == nil || m.ID() == "" {
		return fmt.Errorf("module: empty id")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, existing := range h.mods {
		if existing.mod.ID() == m.ID() {
			return fmt.Errorf("module: %s already registered", m.ID())
		}
	}
	subs := append([]Subscription(nil), m.Subscriptions()...)
	h.mods = append(h.mods, registered{mod: m, subs: subs})
	return nil
}

func (h *Host) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.mods)
}

// Unsubscribed is the number of events that had no matching module.
func (h *Host) Unsubscribed() uint64 { return h.unsubscribed.Load() }

// Dispatch calls matching modules. Events with no subscriber are counted and dropped.
func (h *Host) Dispatch(ctx context.Context, ev Event) error {
	h.mu.RLock()
	regs := append([]registered(nil), h.mods...)
	h.mu.RUnlock()
	var matched bool
	for _, reg := range regs {
		if !matches(reg.subs, ev) {
			continue
		}
		matched = true
		if err := reg.mod.Handle(ctx, ev); err != nil {
			return err
		}
	}
	if !matched {
		h.unsubscribed.Add(1)
	}
	return nil
}

func matches(subs []Subscription, ev Event) bool {
	for _, sub := range subs {
		if sub.Type != ev.Type {
			continue
		}
		if sub.ExtraType == "" || sub.ExtraType == ev.ExtraType {
			return true
		}
	}
	return false
}
