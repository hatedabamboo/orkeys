package vault

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hatedabamboo/orkeys/internal/state"
)

const lockName = ".lock"

func (s *StateStore) readLock(ctx context.Context) (*state.LockInfo, int, error) {
	data, version, err := s.C.ReadVersion(ctx, s.path(lockName))
	if err != nil || data == nil {
		return nil, version, err
	}
	var info state.LockInfo
	if err := fromMap(data, &info); err != nil {
		return nil, 0, fmt.Errorf("vault lock: %w", err)
	}
	return &info, version, nil
}

func (s *StateStore) writeLock(ctx context.Context, info state.LockInfo, cas int) error {
	m, err := toMap(info)
	if err != nil {
		return err
	}
	_, err = s.C.WriteCAS(ctx, s.path(lockName), m, cas)
	return err
}

func (s *StateStore) Acquire(ctx context.Context, info state.LockInfo) error {
	cur, version, err := s.readLock(ctx)
	if err != nil {
		return err
	}
	if cur != nil && time.Now().Before(cur.Expires) {
		return &state.LockedError{Info: *cur}
	}
	err = s.writeLock(ctx, info, version)
	if errors.Is(err, ErrCASMismatch) {
		if cur, _, rerr := s.readLock(ctx); rerr == nil && cur != nil {
			return &state.LockedError{Info: *cur}
		}
		return &state.LockedError{Info: state.LockInfo{Holder: "unknown"}}
	}
	return err
}

func (s *StateStore) Renew(ctx context.Context, info state.LockInfo) error {
	cur, version, err := s.readLock(ctx)
	if err != nil {
		return err
	}
	if cur == nil || cur.ID != info.ID {
		return state.ErrLockLost
	}
	if err := s.writeLock(ctx, info, version); errors.Is(err, ErrCASMismatch) {
		return state.ErrLockLost
	} else if err != nil {
		return err
	}
	return nil
}

func (s *StateStore) Release(ctx context.Context, id string) error {
	cur, _, err := s.readLock(ctx)
	if err != nil || cur == nil {
		return err
	}
	if cur.ID != id {
		return fmt.Errorf("lock %s is held by %s, not %s", cur.ID, cur.Holder, id)
	}
	return s.C.Destroy(ctx, s.path(lockName))
}

func (s *StateStore) Current(ctx context.Context) (*state.LockInfo, error) {
	info, _, err := s.readLock(ctx)
	return info, err
}
