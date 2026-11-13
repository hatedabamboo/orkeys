package awssm

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

type fakeSM struct {
	values  map[string]string
	tags    map[string]map[string]string
	deleted map[string]int64
	untags  int
}

func (f *fakeSM) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	if _, ok := f.deleted[aws.ToString(in.SecretId)]; ok {
		return nil, &types.InvalidRequestException{Message: aws.String("secret was marked for deletion")}
	}
	v, ok := f.values[aws.ToString(in.SecretId)]
	if !ok {
		return nil, &types.ResourceNotFoundException{}
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: aws.String(v)}, nil
}

func (f *fakeSM) DescribeSecret(_ context.Context, in *secretsmanager.DescribeSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.DescribeSecretOutput, error) {
	name := aws.ToString(in.SecretId)
	out := &secretsmanager.DescribeSecretOutput{Name: &name}
	if _, ok := f.deleted[name]; ok {
		out.DeletedDate = aws.Time(time.Now())
		return out, nil
	}
	if _, ok := f.values[name]; !ok {
		return nil, &types.ResourceNotFoundException{}
	}
	for k, v := range f.tags[name] {
		out.Tags = append(out.Tags, types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return out, nil
}

func (f *fakeSM) RestoreSecret(_ context.Context, in *secretsmanager.RestoreSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.RestoreSecretOutput, error) {
	name := aws.ToString(in.SecretId)
	if _, ok := f.deleted[name]; !ok {
		return nil, &types.InvalidRequestException{Message: aws.String("not scheduled for deletion")}
	}
	delete(f.deleted, name)
	return &secretsmanager.RestoreSecretOutput{}, nil
}

func (f *fakeSM) CreateSecret(_ context.Context, in *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	f.values[aws.ToString(in.Name)] = aws.ToString(in.SecretString)
	f.tag(aws.ToString(in.Name), in.Tags)
	return &secretsmanager.CreateSecretOutput{}, nil
}

func (f *fakeSM) PutSecretValue(_ context.Context, in *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
	f.values[aws.ToString(in.SecretId)] = aws.ToString(in.SecretString)
	return &secretsmanager.PutSecretValueOutput{}, nil
}

func (f *fakeSM) TagResource(_ context.Context, in *secretsmanager.TagResourceInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.TagResourceOutput, error) {
	f.tag(aws.ToString(in.SecretId), in.Tags)
	return &secretsmanager.TagResourceOutput{}, nil
}

func (f *fakeSM) UntagResource(_ context.Context, in *secretsmanager.UntagResourceInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.UntagResourceOutput, error) {
	f.untags++
	for _, k := range in.TagKeys {
		delete(f.tags[aws.ToString(in.SecretId)], k)
	}
	return &secretsmanager.UntagResourceOutput{}, nil
}

func (f *fakeSM) tag(name string, tags []types.Tag) {
	if f.tags[name] == nil {
		f.tags[name] = map[string]string{}
	}
	for _, t := range tags {
		f.tags[name][aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
}

func (f *fakeSM) DeleteSecret(_ context.Context, in *secretsmanager.DeleteSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error) {
	name := aws.ToString(in.SecretId)
	if _, ok := f.values[name]; !ok {
		return nil, &types.ResourceNotFoundException{}
	}
	// AWS keeps the value and the tags until the recovery window ends.
	f.deleted[name] = aws.ToInt64(in.RecoveryWindowInDays)
	return &secretsmanager.DeleteSecretOutput{}, nil
}

func TestSecretStore(t *testing.T) {
	f := &fakeSM{values: map[string]string{"app/x": `{"other":"keep"}`}, tags: map[string]map[string]string{}, deleted: map[string]int64{}}
	s := &SecretStore{API: f, RecoveryWindow: 7}
	ctx := context.Background()

	if err := s.Put(ctx, "app/new", "api_key", "sk1", map[string]string{"orkeys-key": "prod-a-b"}); err != nil {
		t.Fatal(err)
	}
	if f.values["app/new"] != `{"api_key":"sk1"}` || f.tags["app/new"]["orkeys-key.api_key"] != "prod-a-b" {
		t.Fatalf("create: %v %v", f.values, f.tags)
	}
	if err := s.Put(ctx, "app/x", "api_key", "sk2", map[string]string{"orkeys-key": "prod-c-d"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read(ctx, "app/x")
	if err != nil || got["other"] != "keep" || got["api_key"] != "sk2" || f.tags["app/x"]["orkeys-key.api_key"] != "prod-c-d" {
		t.Fatalf("update: %v %v %v", got, err, f.tags)
	}
	if _, err := s.Read(ctx, "missing"); err == nil {
		t.Fatal("want not found")
	}
	if err := s.DeleteField(ctx, "app/x", "api_key"); err != nil || f.values["app/x"] != `{"other":"keep"}` {
		t.Fatalf("delete field must keep other fields: %v %v", err, f.values)
	}
	if err := s.DeleteField(ctx, "app/x", "missing"); err != nil || f.values["app/x"] == "" {
		t.Fatalf("delete of a missing field: %v", err)
	}
	if err := s.DeleteField(ctx, "app/x", "other"); err != nil || f.deleted["app/x"] != 7 {
		t.Fatalf("delete of the last field must delete the secret: %v %v", err, f.deleted)
	}
	if err := s.DeleteField(ctx, "app/x", "other"); err != nil {
		t.Fatalf("delete of a missing secret must succeed: %v", err)
	}
	f.values["bad"] = "not json"
	if _, err := s.Read(ctx, "bad"); err == nil {
		t.Fatal("want error for non-JSON value")
	}
}

func TestPutRestoresSecretScheduledForDeletion(t *testing.T) {
	f := &fakeSM{values: map[string]string{"app/x": `{"api_key":"old"}`}, tags: map[string]map[string]string{}, deleted: map[string]int64{}}
	s := &SecretStore{API: f, RecoveryWindow: 7}
	ctx := context.Background()

	if err := s.DeleteField(ctx, "app/x", "api_key"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, "app/x"); err == nil {
		t.Fatal("a secret scheduled for deletion must read as not found")
	}
	if m, err := s.Meta(ctx, "app/x", "api_key"); err != nil || m != nil {
		t.Fatalf("meta of a deleted secret: %v %v", m, err)
	}
	if err := s.Put(ctx, "app/x", "token", "new", map[string]string{"orkeys-key-id": "k2"}); err != nil {
		t.Fatal(err)
	}
	if _, pending := f.deleted["app/x"]; pending || f.values["app/x"] != `{"token":"new"}` {
		t.Fatalf("restore: deleted %v value %s", f.deleted, f.values["app/x"])
	}
	if m, _ := s.Meta(ctx, "app/x", "token"); m["orkeys-key-id"] != "k2" {
		t.Fatalf("meta %v", m)
	}
	if _, ok := f.tags["app/x"]["orkeys-key-id.api_key"]; ok {
		t.Fatalf("restore must drop the tags of the dropped fields: %v", f.tags["app/x"])
	}
}

func TestFieldTags(t *testing.T) {
	f := &fakeSM{values: map[string]string{"app/x": `{"a":"old"}`}, tags: map[string]map[string]string{
		"app/x": {"orkeys-key": "prod-a", "orkeys-key-id": "k0", "orkeys-team": "t", "owner": "infra"},
	}, deleted: map[string]int64{}}
	s := &SecretStore{API: f, RecoveryWindow: 7}
	ctx := context.Background()

	if m, _ := s.Meta(ctx, "app/x", "a"); m["orkeys-key-id"] != "k0" {
		t.Fatalf("a field without own tags must read the per-secret tags: %v", m)
	}
	if err := s.Put(ctx, "app/x", "b", "sb", map[string]string{"orkeys-key": "prod-b", "orkeys-key-id": "kb"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.tags["app/x"]["orkeys-key-id"]; ok || f.tags["app/x"]["owner"] != "infra" {
		t.Fatalf("put must drop the per-secret orkeys tags and keep other tags: %v", f.tags["app/x"])
	}
	if err := s.Put(ctx, "app/x", "a", "sa", map[string]string{"orkeys-key": "prod-a", "orkeys-key-id": "ka"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Meta(ctx, "app/x", "a"); m["orkeys-key-id"] != "ka" {
		t.Fatalf("meta a: %v", m)
	}
	if m, _ := s.Meta(ctx, "app/x", "b"); m["orkeys-key-id"] != "kb" || m["orkeys-key"] != "prod-b" {
		t.Fatalf("meta b: %v", m)
	}
	untags := f.untags
	if err := s.Put(ctx, "app/x", "a", "sa2", map[string]string{"orkeys-key": "prod-a", "orkeys-key-id": "ka2"}); err != nil {
		t.Fatal(err)
	}
	if f.untags != untags {
		t.Fatal("a put without stale tags must not call UntagResource")
	}
	if err := s.DeleteField(ctx, "app/x", "a"); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Meta(ctx, "app/x", "a"); len(m) != 0 {
		t.Fatalf("delete field must drop its tags: %v", m)
	}
	if m, _ := s.Meta(ctx, "app/x", "b"); m["orkeys-key-id"] != "kb" {
		t.Fatalf("delete field must keep the tags of other fields: %v", m)
	}
}
