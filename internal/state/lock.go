package state

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"
)

type LockInfo struct {
	ID        string    `json:"id"`
	Holder    string    `json:"holder"`
	Operation string    `json:"operation"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires"`
}

type LockedError struct{ Info LockInfo }

func (e *LockedError) Error() string {
	i := e.Info
	return fmt.Sprintf("state is locked by %s (%s, id %s, since %s, expires %s)",
		i.Holder, i.Operation, i.ID, i.Created.Format(time.RFC3339), i.Expires.Format(time.RFC3339))
}

var ErrLockLost = errors.New("state lock was lost: another process took it over")

// A Locker stores at most one LockInfo. Acquire creates it only if none exists or the existing one
// has expired; Renew and Release act only if the stored lock still has the given ID.
type Locker interface {
	Acquire(ctx context.Context, info LockInfo) error
	Renew(ctx context.Context, info LockInfo) error
	Release(ctx context.Context, id string) error
	Current(ctx context.Context) (*LockInfo, error)
}

type Lease struct {
	l      Locker
	info   LockInfo
	cancel context.CancelFunc
	done   chan struct{}
	lost   chan struct{}
}

func NewLockID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func RunURL() string {
	s, r, id := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID")
	if s == "" || r == "" || id == "" {
		return ""
	}
	return s + "/" + r + "/actions/runs/" + id
}

func DefaultHolder() string {
	if u := RunURL(); u != "" {
		return u
	}
	host, _ := os.Hostname()
	return os.Getenv("USER") + "@" + host
}

func Lock(ctx context.Context, l Locker, holder, operation string, ttl, wait time.Duration) (*Lease, error) {
	deadline := time.Now().Add(wait)
	for {
		now := time.Now().UTC()
		info := LockInfo{ID: NewLockID(), Holder: holder, Operation: operation, Created: now, Expires: now.Add(ttl)}
		err := l.Acquire(ctx, info)
		if err == nil {
			rctx, cancel := context.WithCancel(context.Background())
			le := &Lease{l: l, info: info, cancel: cancel, done: make(chan struct{}), lost: make(chan struct{})}
			go le.renew(rctx, ttl)
			return le, nil
		}
		var locked *LockedError
		if !errors.As(err, &locked) || time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func (le *Lease) ID() string { return le.info.ID }

func (le *Lease) Lost() <-chan struct{} { return le.lost }

func (le *Lease) renew(ctx context.Context, ttl time.Duration) {
	defer close(le.done)
	t := time.NewTicker(ttl / 3)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			le.info.Expires = time.Now().UTC().Add(ttl)
			if err := le.l.Renew(ctx, le.info); errors.Is(err, ErrLockLost) {
				close(le.lost)
				return
			}
		}
	}
}

func (le *Lease) Release(ctx context.Context) error {
	le.cancel()
	<-le.done
	return le.l.Release(ctx, le.info.ID)
}
