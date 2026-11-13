package s3store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/hatedabamboo/orkeys/internal/state"
)

type API interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

type StateStore struct {
	API    API
	Bucket string
	Prefix string
}

func (s *StateStore) key(name string) string {
	p := strings.Trim(s.Prefix, "/")
	if p == "" {
		return name + ".json"
	}
	return p + "/" + name + ".json"
}

func (s *StateStore) ListStates(ctx context.Context) ([]*state.State, error) {
	prefix := strings.Trim(s.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	var names []string
	p := s3.NewListObjectsV2Paginator(s.API, &s3.ListObjectsV2Input{Bucket: &s.Bucket, Prefix: &prefix, Delimiter: aws.String("/")})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3 list s3://%s/%s: %w", s.Bucket, prefix, err)
		}
		for _, o := range page.Contents {
			k := strings.TrimPrefix(aws.ToString(o.Key), prefix)
			if strings.HasSuffix(k, ".json") && !strings.HasPrefix(k, ".") {
				names = append(names, strings.TrimSuffix(k, ".json"))
			}
		}
	}
	sort.Strings(names)
	var out []*state.State
	for _, n := range names {
		st, err := s.GetState(ctx, n)
		if err != nil {
			return nil, err
		}
		if st != nil {
			out = append(out, st)
		}
	}
	return out, nil
}

func (s *StateStore) GetState(ctx context.Context, name string) (*state.State, error) {
	out, err := s.API.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.Bucket, Key: aws.String(s.key(name))})
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", s.key(name), err)
	}
	defer func() { _ = out.Body.Close() }()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, err
	}
	var st state.State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("state %s: %w", name, err)
	}
	st.Rev = aws.ToString(out.ETag)
	return &st, nil
}

// PutState writes with If-Match on the ETag in st.Rev, or with If-None-Match when st.Rev is empty (create only).
func (s *StateStore) PutState(ctx context.Context, st *state.State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	in := &s3.PutObjectInput{
		Bucket:      &s.Bucket,
		Key:         aws.String(s.key(st.Name)),
		Body:        bytes.NewReader(b),
		ContentType: aws.String("application/json"),
	}
	if st.Rev == "" {
		in.IfNoneMatch = aws.String("*")
	} else {
		in.IfMatch = aws.String(st.Rev)
	}
	out, err := s.API.PutObject(ctx, in)
	if conditionFailed(err) {
		return fmt.Errorf("state %s: %w", st.Name, state.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", s.key(st.Name), err)
	}
	st.Rev = aws.ToString(out.ETag)
	return nil
}

func (s *StateStore) DeleteState(ctx context.Context, name string) error {
	_, err := s.API.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.Bucket, Key: aws.String(s.key(name))})
	if err != nil {
		return fmt.Errorf("s3 delete %s: %w", s.key(name), err)
	}
	return nil
}

// heartbeatName is the object with the heartbeats. ListStates skips names that start with ".".
const heartbeatName = ".heartbeat"

// PutHeartbeat records t as the last success of operation. It runs under the state lock.
// So a read and an unconditional write are safe.
func (s *StateStore) PutHeartbeat(ctx context.Context, operation string, t time.Time) error {
	beats, err := s.readHeartbeats(ctx)
	if err != nil {
		return err
	}
	beats[operation] = t.UTC().Format(time.RFC3339)
	b, err := json.Marshal(beats)
	if err != nil {
		return err
	}
	_, err = s.API.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.Bucket, Key: aws.String(s.key(heartbeatName)),
		Body: bytes.NewReader(b), ContentType: aws.String("application/json")})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", s.key(heartbeatName), err)
	}
	return nil
}

// Heartbeats returns the last success of each operation.
func (s *StateStore) Heartbeats(ctx context.Context) (map[string]time.Time, error) {
	beats, err := s.readHeartbeats(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for op, v := range beats {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			out[op] = t
		}
	}
	return out, nil
}

// policyName is the object with the copy of policy.yaml. ListStates skips names that start with ".".
const policyName = ".policy"

// PutPolicy writes the copy of policy.yaml. apply calls it under the state lock.
func (s *StateStore) PutPolicy(ctx context.Context, p *state.StoredPolicy) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.API.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.Bucket, Key: aws.String(s.key(policyName)),
		Body: bytes.NewReader(b), ContentType: aws.String("application/json")})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", s.key(policyName), err)
	}
	return nil
}

// GetPolicy returns the copy of policy.yaml, or nil when there is none.
func (s *StateStore) GetPolicy(ctx context.Context) (*state.StoredPolicy, error) {
	out, err := s.API.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.Bucket, Key: aws.String(s.key(policyName))})
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", s.key(policyName), err)
	}
	defer func() { _ = out.Body.Close() }()
	var p state.StoredPolicy
	if err := json.NewDecoder(out.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("s3 %s: %w", s.key(policyName), err)
	}
	return &p, nil
}

func (s *StateStore) readHeartbeats(ctx context.Context) (map[string]string, error) {
	out, err := s.API.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.Bucket, Key: aws.String(s.key(heartbeatName))})
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", s.key(heartbeatName), err)
	}
	defer func() { _ = out.Body.Close() }()
	beats := map[string]string{}
	if err := json.NewDecoder(out.Body).Decode(&beats); err != nil {
		return nil, fmt.Errorf("s3 %s: %w", s.key(heartbeatName), err)
	}
	return beats, nil
}
