package awssm

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

type fakeSM struct {
	values  map[string]string
	tags    map[string]map[string]string
	deleted map[string]int64
}

func (f *fakeSM) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	v, ok := f.values[aws.ToString(in.SecretId)]
	if !ok {
		return nil, &types.ResourceNotFoundException{}
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: aws.String(v)}, nil
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
	delete(f.values, name)
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
	if f.values["app/new"] != `{"api_key":"sk1"}` || f.tags["app/new"]["orkeys-key"] != "prod-a-b" {
		t.Fatalf("create: %v %v", f.values, f.tags)
	}
	if err := s.Put(ctx, "app/x", "api_key", "sk2", map[string]string{"orkeys-key": "prod-c-d"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read(ctx, "app/x")
	if err != nil || got["other"] != "keep" || got["api_key"] != "sk2" || f.tags["app/x"]["orkeys-key"] != "prod-c-d" {
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
