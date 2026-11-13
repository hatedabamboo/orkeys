package s3store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/hatedabamboo/orkeys/internal/state"
)

type fakeS3 struct {
	objects map[string][]byte
	etags   map[string]int
}

type apiErr struct{ code string }

func (e apiErr) Error() string                 { return e.code }
func (e apiErr) ErrorCode() string             { return e.code }
func (e apiErr) ErrorMessage() string          { return e.code }
func (e apiErr) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func (f *fakeS3) etag(k string) string { return fmt.Sprintf("\"%d\"", f.etags[k]) }

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	k := aws.ToString(in.Bucket) + "/" + aws.ToString(in.Key)
	b, ok := f.objects[k]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b)), ETag: aws.String(f.etag(k))}, nil
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	k := aws.ToString(in.Bucket) + "/" + aws.ToString(in.Key)
	_, exists := f.objects[k]
	if in.IfNoneMatch != nil && exists {
		return nil, apiErr{"PreconditionFailed"}
	}
	if in.IfMatch != nil && (!exists || aws.ToString(in.IfMatch) != f.etag(k)) {
		return nil, apiErr{"PreconditionFailed"}
	}
	b, _ := io.ReadAll(in.Body)
	f.objects[k] = b
	if f.etags == nil {
		f.etags = map[string]int{}
	}
	f.etags[k]++
	return &s3.PutObjectOutput{ETag: aws.String(f.etag(k))}, nil
}

func (f *fakeS3) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(f.objects, aws.ToString(in.Bucket)+"/"+aws.ToString(in.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	prefix := aws.ToString(in.Bucket) + "/" + aws.ToString(in.Prefix)
	var keys []string
	for k := range f.objects {
		if rest, ok := strings.CutPrefix(k, prefix); ok && !strings.Contains(rest, "/") {
			keys = append(keys, strings.TrimPrefix(k, aws.ToString(in.Bucket)+"/"))
		}
	}
	sort.Strings(keys)
	out := &s3.ListObjectsV2Output{}
	for _, k := range keys {
		out.Contents = append(out.Contents, types.Object{Key: aws.String(k)})
	}
	return out, nil
}

func TestLock(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}}
	s := &StateStore{API: f, Bucket: "b", Prefix: "st"}
	ctx := context.Background()
	now := time.Now().UTC()
	a := state.LockInfo{ID: "a", Holder: "run-1", Created: now, Expires: now.Add(time.Hour)}
	if err := s.Acquire(ctx, a); err != nil {
		t.Fatal(err)
	}
	var locked *state.LockedError
	if err := s.Acquire(ctx, state.LockInfo{ID: "b", Expires: now.Add(time.Hour)}); !errors.As(err, &locked) || locked.Info.ID != "a" {
		t.Fatalf("second acquire: %v", err)
	}
	if err := s.Renew(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.Renew(ctx, state.LockInfo{ID: "x"}); !errors.Is(err, state.ErrLockLost) {
		t.Fatalf("renew by another id: %v", err)
	}
	if err := s.Release(ctx, "x"); err == nil {
		t.Fatal("release by another id must fail")
	}
	if all, _ := s.ListStates(ctx); len(all) != 0 {
		t.Fatal("lock object listed as state")
	}
	if err := s.Release(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.Current(ctx); cur != nil {
		t.Fatal("lock not released")
	}

	stale := state.LockInfo{ID: "old", Created: now.Add(-2 * time.Hour), Expires: now.Add(-time.Hour)}
	if err := s.Acquire(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if err := s.Acquire(ctx, state.LockInfo{ID: "new", Expires: now.Add(time.Hour)}); err != nil {
		t.Fatalf("expired lock not taken over: %v", err)
	}
	if err := s.Renew(ctx, stale); !errors.Is(err, state.ErrLockLost) {
		t.Fatal("old holder must see a lost lock")
	}
}

func TestStateStore(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{"b/orkeys/state/nested/x.json": []byte("{}"), "b/orkeys/state/readme.txt": []byte("x")}}
	s := &StateStore{API: f, Bucket: "b", Prefix: "/orkeys/state/"}
	ctx := context.Background()
	exp := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, n := range []string{"prod-a-b", "local-x-1"} {
		if err := s.PutState(ctx, &state.State{Name: n, KeyID: "id-" + n, ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := f.objects["b/orkeys/state/prod-a-b.json"]; !ok {
		t.Fatalf("object key: %v", f.objects)
	}
	all, err := s.ListStates(ctx)
	if err != nil || len(all) != 2 || all[0].Name != "local-x-1" || !all[1].ExpiresAt.Equal(exp) {
		t.Fatalf("list: %+v %v", all, err)
	}
	if st, err := s.GetState(ctx, "missing"); st != nil || err != nil {
		t.Fatal("missing state must be nil, nil")
	}
	if err := s.DeleteState(ctx, "prod-a-b"); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.ListStates(ctx); len(all) != 1 {
		t.Fatal("delete")
	}
}

func TestStateConflicts(t *testing.T) {
	s := &StateStore{API: &fakeS3{objects: map[string][]byte{}}, Bucket: "b", Prefix: "st"}
	ctx := context.Background()
	st := &state.State{Name: "prod-a-b", KeyID: "k1"}
	if err := s.PutState(ctx, st); err != nil || st.Rev == "" {
		t.Fatalf("create: %v rev %q", err, st.Rev)
	}
	if err := s.PutState(ctx, &state.State{Name: "prod-a-b", KeyID: "k2"}); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("second create must conflict: %v", err)
	}
	read, _ := s.GetState(ctx, "prod-a-b")
	st.KeyID = "k3"
	if err := s.PutState(ctx, st); err != nil {
		t.Fatal(err)
	}
	read.KeyID = "stale"
	if err := s.PutState(ctx, read); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("write with a stale revision must conflict: %v", err)
	}
	if got, _ := s.GetState(ctx, "prod-a-b"); got.KeyID != "k3" || got.Rev != st.Rev {
		t.Fatalf("state %+v, want k3 at rev %s", got, st.Rev)
	}
}

func TestHeartbeats(t *testing.T) {
	s := &StateStore{API: &fakeS3{objects: map[string][]byte{}}, Bucket: "b", Prefix: "st"}
	ctx := context.Background()
	t1 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	if err := s.PutHeartbeat(ctx, "rotate", t1); err != nil {
		t.Fatal(err)
	}
	if err := s.PutHeartbeat(ctx, "apply", t1.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Heartbeats(ctx)
	if err != nil || !got["rotate"].Equal(t1) || !got["apply"].Equal(t1.Add(time.Hour)) {
		t.Fatalf("heartbeats %v %v", got, err)
	}
	if all, _ := s.ListStates(ctx); len(all) != 0 {
		t.Fatal("heartbeat listed as state")
	}
}

func TestStoredPolicy(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}}
	s := &StateStore{API: f, Bucket: "b", Prefix: "st"}
	ctx := context.Background()
	if p, err := s.GetPolicy(ctx); p != nil || err != nil {
		t.Fatalf("no policy must be nil, nil: %v %v", p, err)
	}
	t1 := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	if err := s.PutPolicy(ctx, &state.StoredPolicy{YAML: "secrets: aws\n", UpdatedAt: t1}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.objects["b/st/.policy.json"]; !ok {
		t.Fatalf("object key: %v", f.objects)
	}
	p, err := s.GetPolicy(ctx)
	if err != nil || p.YAML != "secrets: aws\n" || !p.UpdatedAt.Equal(t1) {
		t.Fatalf("policy %+v %v", p, err)
	}
	if all, _ := s.ListStates(ctx); len(all) != 0 {
		t.Fatal("policy listed as state")
	}
}
