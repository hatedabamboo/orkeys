package secrets

import (
	"context"
	"fmt"
	"maps"
	"strings"
)

// Store holds secrets as maps of string fields, with metadata per field.
type Store interface {
	// Put sets one field and keeps the other fields. With meta, it also replaces the metadata of that field and
	// keeps the metadata of the other fields. It must write the value before the metadata. orkeys reads metadata
	// with a key ID as proof that the field holds that key.
	Put(ctx context.Context, path, field, value string, meta map[string]string) error
	// DeleteField removes one field and its metadata. It deletes the secret only when no other field is left.
	DeleteField(ctx context.Context, path, field string) error
	Read(ctx context.Context, path string) (map[string]string, error)
	// Meta returns the metadata of one field, or nil when the secret does not exist.
	Meta(ctx context.Context, path, field string) (map[string]string, error)
}

// Metadata names. A store keeps each name per field, as <name>.<field> (see FieldMetaName).
const (
	// MetaKey is the name of the key manifest.
	MetaKey = "orkeys-key"
	// MetaKeyID is the ID of the provider key in the field.
	MetaKeyID = "orkeys-key-id"
	// MetaTeam is the team of the key.
	MetaTeam = "orkeys-team"
)

var metaNames = []string{MetaKey, MetaKeyID, MetaTeam}

// FieldMetaName returns the stored name of the metadata name of field.
func FieldMetaName(name, field string) string {
	return name + "." + field
}

// IsMetaName reports whether a stored metadata name belongs to orkeys.
func IsMetaName(stored string) bool {
	for _, n := range metaNames {
		if stored == n || strings.HasPrefix(stored, n+".") {
			return true
		}
	}
	return false
}

// FieldMeta returns the metadata of field from the stored metadata of a secret, or nil when stored is nil.
// orkeys up to v0.4.1 kept one set of names per secret. FieldMeta returns that set when field has no names of its
// own.
func FieldMeta(stored map[string]string, field string) map[string]string {
	if stored == nil {
		return nil
	}
	out := map[string]string{}
	for _, n := range metaNames {
		if v, ok := stored[FieldMetaName(n, field)]; ok {
			out[n] = v
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, n := range metaNames {
		if v, ok := stored[n]; ok {
			out[n] = v
		}
	}
	return out
}

// SetFieldMeta returns a copy of stored with the metadata of field replaced by meta. The copy has no per-secret
// names: they can name the key of another field.
func SetFieldMeta(stored map[string]string, field string, meta map[string]string) map[string]string {
	out := DropFieldMeta(stored, field)
	for _, n := range metaNames {
		delete(out, n)
	}
	for k, v := range meta {
		out[FieldMetaName(k, field)] = v
	}
	return out
}

// DropFieldMeta returns a copy of stored without the metadata of field.
func DropFieldMeta(stored map[string]string, field string) map[string]string {
	out := maps.Clone(stored)
	if out == nil {
		out = map[string]string{}
	}
	for _, n := range metaNames {
		delete(out, FieldMetaName(n, field))
	}
	return out
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
