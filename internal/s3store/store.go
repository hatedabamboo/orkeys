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
	return &st, nil
}

func (s *StateStore) PutState(ctx context.Context, st *state.State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	_, err = s.API.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.Bucket,
		Key:         aws.String(s.key(st.Name)),
		Body:        bytes.NewReader(b),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", s.key(st.Name), err)
	}
	return nil
}

func (s *StateStore) DeleteState(ctx context.Context, name string) error {
	_, err := s.API.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.Bucket, Key: aws.String(s.key(name))})
	if err != nil {
		return fmt.Errorf("s3 delete %s: %w", s.key(name), err)
	}
	return nil
}
