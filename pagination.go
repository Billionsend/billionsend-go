package billionsend

import (
	"iter"
	"reflect"
)

// cursor is where a walk starts: forward from after, or backward from before.
type cursor struct {
	after, before *string
}

// startCursor reads the After and Before fields of a list's params.
func startCursor(params any) cursor {
	v := reflect.ValueOf(params)
	if !v.IsValid() || v.IsNil() {
		return cursor{}
	}
	v = v.Elem()
	read := func(name string) *string {
		f := v.FieldByName(name)
		if !f.IsValid() || f.IsNil() || f.Elem().String() == "" {
			return nil
		}
		s := f.Elem().String()
		return &s
	}
	return cursor{after: read("After"), before: read("Before")}
}

// itemID is the id of a list item (its ID field).
func itemID(item any) string {
	v := reflect.Indirect(reflect.ValueOf(item))
	if v.Kind() != reflect.Struct {
		return ""
	}
	f := v.FieldByName("ID")
	if !f.IsValid() {
		return ""
	}
	if f.Kind() == reflect.Pointer {
		if f.IsNil() {
			return ""
		}
		f = f.Elem()
	}
	return f.String()
}

// paginate walks a cursor-paginated list one item at a time, fetching pages
// on demand. Starting with before set walks backward (by the first id of each
// page); otherwise it walks forward from after (by the last id). A failed
// page is yielded as the error and ends the walk.
func paginate[T any](
	fetch func(after, before *string) ([]T, bool, error),
	start cursor,
	idOf func(T) string,
) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		backward := start.before != nil
		after, before := start.after, start.before
		if backward {
			after = nil
		}
		for {
			items, more, err := fetch(after, before)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range items {
				if !yield(item, nil) {
					return
				}
			}
			if !more || len(items) == 0 {
				return
			}
			edge := items[len(items)-1]
			if backward {
				edge = items[0]
			}
			id := idOf(edge)
			if id == "" {
				return
			}
			if backward {
				before = &id
			} else {
				after = &id
			}
		}
	}
}
