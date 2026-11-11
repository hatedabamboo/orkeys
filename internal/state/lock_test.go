package state

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type memLocker struct {
	mu     sync.Mutex
	cur    *LockInfo
	renews int
}

func (m *memLocker) Acquire(_ context.Context, info LockInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil && time.Now().Before(m.cur.Expires) {
		return &LockedError{Info: *m.cur}
	}
	m.cur = &info
	return nil
}

func (m *memLocker) Renew(_ context.Context, info LockInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil || m.cur.ID != info.ID {
		return ErrLockLost
	}
	m.cur = &info
	m.renews++
	return nil
}

func (m *memLocker) Release(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil && m.cur.ID == id {
		m.cur = nil
	}
	return nil
}

func (m *memLocker) Current(context.Context) (*LockInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur, nil
}

func TestLease(t *testing.T) {
	m := &memLocker{}
	ctx := context.Background()
	le, err := Lock(ctx, m, "me", "apply", 30*time.Millisecond, 0)
	if err != nil {
		t.Fatal(err)
	}
	var locked *LockedError
	if _, err := Lock(ctx, m, "other", "rotate", time.Second, 0); !errors.As(err, &locked) || locked.Info.Holder != "me" {
		t.Fatalf("want locked, got %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	m.mu.Lock()
	renews := m.renews
	m.mu.Unlock()
	if renews == 0 {
		t.Fatal("lease not renewed")
	}
	if err := le.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if cur, _ := m.Current(ctx); cur != nil {
		t.Fatal("not released")
	}

	le, _ = Lock(ctx, m, "me", "apply", 30*time.Millisecond, 0)
	m.mu.Lock()
	m.cur = &LockInfo{ID: "thief", Expires: time.Now().Add(time.Hour)}
	m.mu.Unlock()
	select {
	case <-le.Lost():
	case <-time.After(time.Second):
		t.Fatal("lost lock not reported")
	}
	_ = le.Release(ctx)
}

type failingLocker struct{ memLocker }

func (f *failingLocker) Renew(context.Context, LockInfo) error { return errors.New("store down") }

func TestLeaseLostWhenRenewalKeepsFailing(t *testing.T) {
	f := &failingLocker{}
	ctx := context.Background()
	le, err := Lock(ctx, f, "me", "apply", 60*time.Millisecond, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = le.Release(ctx) }()
	select {
	case <-le.Lost():
	case <-time.After(time.Second):
		t.Fatal("lease not lost while renewals fail")
	}
	if err := le.Err(); !errors.Is(err, ErrLockLost) {
		t.Fatalf("err %v", err)
	}
}
