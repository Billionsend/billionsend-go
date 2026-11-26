package billionsend

import (
	"bytes"
	"encoding/json"
)

// Nullable is a field of an update that the API reads in three ways: left out,
// it keeps its value; null clears or resets it; anything else sets it. A nil *Nullable leaves the field out, Null sends null and
// Value sends a value:
//
//	&billionsend.UpdateDomainRequest{
//		TrackingSubdomain:    billionsend.Value("links"),
//		UnsubscribeSubdomain: billionsend.Null[string](),
//	}
//
// A field typed any that accepts null takes Null[any]().
type Nullable[T any] struct {
	value *T
}

// Value is a field set to v.
func Value[T any](v T) *Nullable[T] { return &Nullable[T]{value: &v} }

// Null is a field sent as null.
func Null[T any]() *Nullable[T] { return &Nullable[T]{} }

// Get returns the value; false for null.
func (n Nullable[T]) Get() (T, bool) {
	if n.value == nil {
		var zero T
		return zero, false
	}
	return *n.value, true
}

// MarshalJSON writes the value, or null.
func (n Nullable[T]) MarshalJSON() ([]byte, error) { return json.Marshal(n.value) }

// UnmarshalJSON reads a value or null. Note that encoding/json leaves a
// *Nullable field nil for a null in the document.
func (n *Nullable[T]) UnmarshalJSON(b []byte) error {
	n.value = nil
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	v := new(T)
	if err := json.Unmarshal(b, v); err != nil {
		return err
	}
	n.value = v
	return nil
}
