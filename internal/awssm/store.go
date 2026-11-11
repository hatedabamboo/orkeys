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

// API is the part of the Secrets Manager client that SecretStore uses.
type API interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
	DescribeSecret(ctx context.Context, in *secretsmanager.DescribeSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.DescribeSecretOutput, error)
	CreateSecret(ctx context.Context, in *secretsmanager.CreateSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
	PutSecretValue(ctx context.Context, in *secretsmanager.PutSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
	TagResource(ctx context.Context, in *secretsmanager.TagResourceInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.TagResourceOutput, error)
	DeleteSecret(ctx context.Context, in *secretsmanager.DeleteSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error)
	RestoreSecret(ctx context.Context, in *secretsmanager.RestoreSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.RestoreSecretOutput, error)
}

// SecretStore keeps each secret as a JSON object of string fields in AWS Secrets Manager.
type SecretStore struct {
	API            API
	RecoveryWindow int64
}

type secretState int

const (
	secretMissing secretState = iota
	secretExists
	// secretDeleting is a secret that is scheduled for deletion. AWS refuses reads and writes of it until a restore.
	secretDeleting
)

func (s *SecretStore) get(ctx context.Context, name string) (map[string]string, secretState, error) {
	out, err := s.API.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &name})
	var nf *types.ResourceNotFoundException
	if errors.As(err, &nf) {
		return nil, secretMissing, nil
	}
	var ir *types.InvalidRequestException
	if errors.As(err, &ir) {
		d, derr := s.API.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: &name})
		if derr == nil && d.DeletedDate != nil {
			return nil, secretDeleting, nil
		}
	}
	if err != nil {
		return nil, secretMissing, fmt.Errorf("secretsmanager get %s: %w", name, err)
	}
	data := map[string]string{}
	if v := aws.ToString(out.SecretString); v != "" {
		if err := json.Unmarshal([]byte(v), &data); err != nil {
			return nil, secretExists, fmt.Errorf("secretsmanager %s: value is not a JSON object of strings: %w", name, err)
		}
	}
	return data, secretExists, nil
}

// Put writes the value before the tags. Then a tag with a key ID proves that the field holds that key.
func (s *SecretStore) Put(ctx context.Context, name, field, value string, meta map[string]string) error {
	data, st, err := s.get(ctx, name)
	if err != nil {
		return err
	}
	if st == secretDeleting {
		// The recovery window keeps the old fields. An old key must not come back.
		// So the restored secret gets only the new field.
		if _, err := s.API.RestoreSecret(ctx, &secretsmanager.RestoreSecretInput{SecretId: &name}); err != nil {
			return fmt.Errorf("secretsmanager restore %s: %w", name, err)
		}
		data, st = map[string]string{}, secretExists
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
	if st == secretMissing {
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

// DeleteField removes one field. When no field is left, it schedules the secret for deletion.
func (s *SecretStore) DeleteField(ctx context.Context, name, field string) error {
	data, st, err := s.get(ctx, name)
	if err != nil || st != secretExists {
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

// Read returns the fields of a secret. A secret scheduled for deletion counts as not found.
func (s *SecretStore) Read(ctx context.Context, name string) (map[string]string, error) {
	data, st, err := s.get(ctx, name)
	if err != nil {
		return nil, err
	}
	if st != secretExists {
		return nil, fmt.Errorf("secretsmanager %s: not found", name)
	}
	return data, nil
}

// Meta returns the tags of a secret, or nil when the secret does not exist or is scheduled for deletion.
func (s *SecretStore) Meta(ctx context.Context, name string) (map[string]string, error) {
	out, err := s.API.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: &name})
	var nf *types.ResourceNotFoundException
	if errors.As(err, &nf) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("secretsmanager describe %s: %w", name, err)
	}
	if out.DeletedDate != nil {
		return nil, nil
	}
	meta := make(map[string]string, len(out.Tags))
	for _, t := range out.Tags {
		meta[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return meta, nil
}
