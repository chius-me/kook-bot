package voice

import (
	"context"
	"sync"
	"time"
)

type sleeper interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWait
}

type fakeWait struct {
	deadline time.Time
	ch       chan struct{}
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(0, 0).UTC()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	c.mu.Lock()
	deadline := c.now.Add(d)
	if !c.now.Before(deadline) {
		c.mu.Unlock()
		return ctx.Err()
	}
	ch := make(chan struct{})
	c.waiters = append(c.waiters, fakeWait{deadline: deadline, ch: ch})
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ch:
		return nil
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if !c.now.Before(w.deadline) {
			close(w.ch)
			continue
		}
		keep = append(keep, w)
	}
	c.waiters = keep
}

func (c *fakeClock) Waiters() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}
