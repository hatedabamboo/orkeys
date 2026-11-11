package secrets

import (
	"context"
	"fmt"
)

// Store holds secrets as maps of string fields, with string metadata per secret.
type Store interface {
	// Put sets one field and keeps the other fields. With meta, it also replaces the metadata of the secret.
	// It must write the value before the metadata. orkeys reads metadata with a key ID as proof that the field
	// holds that key.
	Put(ctx context.Context, path, field, value string, meta map[string]string) error
	// DeleteField removes one field. It deletes the secret only when no other field is left.
	DeleteField(ctx context.Context, path, field string) error
	Read(ctx context.Context, path string) (map[string]string, error)
	// Meta returns the metadata of a secret, or nil when the secret does not exist.
	Meta(ctx context.Context, path string) (map[string]string, error)
}

// MetaKeyID is the metadata field with the ID of the provider key in the orkeys fields of a secret.
const MetaKeyID = "orkeys-key-id"

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
