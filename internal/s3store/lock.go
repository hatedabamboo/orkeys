package s3store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/hatedabamboo/orkeys/internal/state"
)

func (s *StateStore) lockKey() string { return strings.TrimSuffix(s.key(".lock"), ".json") }

func conditionFailed(err error) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && (ae.ErrorCode() == "PreconditionFailed" || ae.ErrorCode() == "ConditionalRequestConflict")
}

func (s *StateStore) readLock(ctx context.Context) (*state.LockInfo, string, error) {
	out, err := s.API.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.Bucket, Key: aws.String(s.lockKey())})
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("s3 get lock: %w", err)
	}
	defer func() { _ = out.Body.Close() }()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, "", err
	}
	var info state.LockInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, "", fmt.Errorf("s3 lock %s: %w", s.lockKey(), err)
	}
	return &info, aws.ToString(out.ETag), nil
}

func (s *StateStore) putLock(ctx context.Context, info state.LockInfo, ifMatch, ifNoneMatch *string) error {
	b, err := json.Marshal(info)
	if err != nil {
		return err
	}
	_, err = s.API.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.Bucket, Key: aws.String(s.lockKey()), Body: bytes.NewReader(b),
		ContentType: aws.String("application/json"), IfMatch: ifMatch, IfNoneMatch: ifNoneMatch,
	})
	return err
}

func (s *StateStore) Acquire(ctx context.Context, info state.LockInfo) error {
	err := s.putLock(ctx, info, nil, aws.String("*"))
	if err == nil {
		return nil
	}
	if !conditionFailed(err) {
		return fmt.Errorf("s3 put lock: %w", err)
	}
	cur, etag, err := s.readLock(ctx)
	if err != nil {
		return err
	}
	if cur == nil {
		return s.Acquire(ctx, info)
	}
	if time.Now().Before(cur.Expires) {
		return &state.LockedError{Info: *cur}
	}
	if err := s.putLock(ctx, info, &etag, nil); err != nil {
		if conditionFailed(err) {
			return &state.LockedError{Info: *cur}
		}
		return fmt.Errorf("s3 take over expired lock: %w", err)
	}
	return nil
}

func (s *StateStore) Renew(ctx context.Context, info state.LockInfo) error {
	cur, etag, err := s.readLock(ctx)
	if err != nil {
		return err
	}
	if cur == nil || cur.ID != info.ID {
		return state.ErrLockLost
	}
	if err := s.putLock(ctx, info, &etag, nil); err != nil {
		if conditionFailed(err) {
			return state.ErrLockLost
		}
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
	if _, err := s.API.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.Bucket, Key: aws.String(s.lockKey())}); err != nil {
		return fmt.Errorf("s3 delete lock: %w", err)
	}
	return nil
}

func (s *StateStore) Current(ctx context.Context) (*state.LockInfo, error) {
	info, _, err := s.readLock(ctx)
	return info, err
}
