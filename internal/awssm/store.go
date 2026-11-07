package awssm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

type API interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
	CreateSecret(ctx context.Context, in *secretsmanager.CreateSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
	PutSecretValue(ctx context.Context, in *secretsmanager.PutSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
	TagResource(ctx context.Context, in *secretsmanager.TagResourceInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.TagResourceOutput, error)
	DeleteSecret(ctx context.Context, in *secretsmanager.DeleteSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error)
}

type SecretStore struct {
	API            API
	RecoveryWindow int64
}

func (s *SecretStore) get(ctx context.Context, name string) (map[string]string, bool, error) {
	out, err := s.API.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &name})
	var nf *types.ResourceNotFoundException
	if errors.As(err, &nf) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("secretsmanager get %s: %w", name, err)
	}
	data := map[string]string{}
	if v := aws.ToString(out.SecretString); v != "" {
		if err := json.Unmarshal([]byte(v), &data); err != nil {
			return nil, true, fmt.Errorf("secretsmanager %s: value is not a JSON object of strings: %w", name, err)
		}
	}
	return data, true, nil
}

func (s *SecretStore) Put(ctx context.Context, name, field, value string, meta map[string]string) error {
	data, exists, err := s.get(ctx, name)
	if err != nil {
		return err
	}
	if data == nil {
		data = map[string]string{}
	}
	data[field] = value
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var tags []types.Tag
	for k, v := range meta {
		tags = append(tags, types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	if !exists {
		_, err = s.API.CreateSecret(ctx, &secretsmanager.CreateSecretInput{Name: &name, SecretString: aws.String(string(b)), Tags: tags})
		if err != nil {
			return fmt.Errorf("secretsmanager create %s: %w", name, err)
		}
		return nil
	}
	if _, err := s.API.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{SecretId: &name, SecretString: aws.String(string(b))}); err != nil {
		return fmt.Errorf("secretsmanager put %s: %w", name, err)
	}
	if len(tags) > 0 {
		if _, err := s.API.TagResource(ctx, &secretsmanager.TagResourceInput{SecretId: &name, Tags: tags}); err != nil {
			return fmt.Errorf("secretsmanager tag %s: %w", name, err)
		}
	}
	return nil
}

func (s *SecretStore) DeleteField(ctx context.Context, name, field string) error {
	data, exists, err := s.get(ctx, name)
	if err != nil || !exists {
		return err
	}
	if _, ok := data[field]; !ok {
		return nil
	}
	delete(data, field)
	if len(data) > 0 {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if _, err := s.API.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{SecretId: &name, SecretString: aws.String(string(b))}); err != nil {
			return fmt.Errorf("secretsmanager put %s: %w", name, err)
		}
		return nil
	}
	in := &secretsmanager.DeleteSecretInput{SecretId: &name}
	if s.RecoveryWindow > 0 {
		in.RecoveryWindowInDays = aws.Int64(s.RecoveryWindow)
	}
	_, err = s.API.DeleteSecret(ctx, in)
	var nf *types.ResourceNotFoundException
	if err != nil && !errors.As(err, &nf) {
		return fmt.Errorf("secretsmanager delete %s: %w", name, err)
	}
	return nil
}

func (s *SecretStore) Read(ctx context.Context, name string) (map[string]string, error) {
	data, exists, err := s.get(ctx, name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("secretsmanager %s: not found", name)
	}
	return data, nil
}
