// Package nullable holds a request field that can be absent, explicitly null, or set to a value.
package nullable

// Value is a field that can be absent, null, or set.
type Value[T any] struct {
	Set   bool // the field is present in the request
	Valid bool // the value is not null
	V     T
}

// Of returns a Value that holds v.
func Of[T any](v T) Value[T] { return Value[T]{Set: true, Valid: true, V: v} }

// Null returns a Value that is sent as JSON null.
func Null[T any]() Value[T] { return Value[T]{Set: true} }

// Ptr returns nil for absent or null, else a pointer to a copy of the value.
func (n Value[T]) Ptr() *T {
	if !n.Set || !n.Valid {
		return nil
	}
	v := n.V
	return &v
}

// JSON returns the value for a JSON encoder: nil for null.
func (n Value[T]) JSON() any {
	if !n.Valid {
		return nil
	}
	return n.V
}
