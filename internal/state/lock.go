package state

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
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

// Lease is a held lock. A goroutine renews it until Release.
type Lease struct {
	l      Locker
	info   LockInfo
	cancel context.CancelFunc
	done   chan struct{}
	lost   chan struct{}

	mu  sync.Mutex
	err error
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

// ID returns the lock ID.
func (le *Lease) ID() string { return le.info.ID }

// Lost is closed when the lease is lost. Err then tells why.
func (le *Lease) Lost() <-chan struct{} { return le.lost }

// Err returns nil while the lease is held, and the reason after it is lost.
func (le *Lease) Err() error {
	le.mu.Lock()
	defer le.mu.Unlock()
	return le.err
}

func (le *Lease) fail(err error) {
	le.mu.Lock()
	le.err = err
	le.mu.Unlock()
	close(le.lost)
}

// renew extends the lease every ttl/3. After a failed renewal, it tries again every ttl/15.
// Another process can take an expired lock. So when no renewal works until ttl/6 before the expiry,
// the lease counts as lost, and this process stops before the expiry.
func (le *Lease) renew(ctx context.Context, ttl time.Duration) {
	defer close(le.done)
	every, retry, margin := ttl/3, ttl/15, ttl/6
	expires := le.info.Expires
	t := time.NewTimer(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		next := le.info
		next.Expires = time.Now().UTC().Add(ttl)
		err := le.l.Renew(ctx, next)
		switch {
		case err == nil:
			le.info, expires = next, next.Expires
			t.Reset(every)
		case errors.Is(err, ErrLockLost):
			le.fail(err)
			return
		case ctx.Err() != nil:
			return
		case !time.Now().Before(expires.Add(-margin)):
			le.fail(fmt.Errorf("%w: renewal failed until the lock expires at %s: %w", ErrLockLost, expires.Format(time.RFC3339), err))
			return
		default:
			t.Reset(retry)
		}
	}
}

func (le *Lease) Release(ctx context.Context) error {
	le.cancel()
	<-le.done
	return le.l.Release(ctx, le.info.ID)
}
