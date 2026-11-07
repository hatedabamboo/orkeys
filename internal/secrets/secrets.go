package secrets

import (
	"context"
	"fmt"
)

type Store interface {
	Put(ctx context.Context, path, field, value string, meta map[string]string) error
	// DeleteField removes one field. It deletes the secret only when no other field is left.
	DeleteField(ctx context.Context, path, field string) error
	Read(ctx context.Context, path string) (map[string]string, error)
}

func ReadField(ctx context.Context, s Store, path, field string) (string, error) {
	data, err := s.Read(ctx, path)
	if err != nil {
		return "", err
	}
	if data[field] == "" {
		return "", fmt.Errorf("secret %s: field %q is missing or empty", path, field)
	}
	return data[field], nil
}
