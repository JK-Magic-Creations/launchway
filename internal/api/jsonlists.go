package api

import (
	"encoding"
	"encoding/json"
	"reflect"
	"strings"
)

// A list in an answer is [] when it is empty, never null: the web interface, the command line and every client read
// answer.items.length without a guard, and one null blanked a whole page. writeJSON passes what it writes through
// emptyLists, which gives every nil list inside a struct (a field without omitempty) an empty one. Bytes
// (json.RawMessage), types that write themselves and maps' own entries are left as they are.

var (
	marshalerType     = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

func writesItself(t reflect.Type) bool {
	return t.Implements(marshalerType) || reflect.PointerTo(t).Implements(marshalerType) ||
		t.Implements(textMarshalerType) || reflect.PointerTo(t).Implements(textMarshalerType)
}

// emptyLists returns v with nil lists in its structs made empty. A struct passed by value is copied first, so the
// caller's data is never changed by writing it.
func emptyLists(v any) any {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return v
	}
	switch rv.Kind() {
	case reflect.Struct:
		if writesItself(rv.Type()) {
			return v
		}
		c := reflect.New(rv.Type()).Elem()
		c.Set(rv)
		fillLists(c, 0)
		return c.Interface()
	case reflect.Pointer, reflect.Slice, reflect.Map:
		if !rv.IsNil() {
			fillLists(rv, 0)
		}
	}
	return v
}

func fillLists(v reflect.Value, depth int) {
	if depth > 12 || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			fillLists(v.Elem(), depth+1)
		}
	case reflect.Struct:
		if writesItself(v.Type()) || !v.CanSet() && v.Kind() == reflect.Struct && !v.CanAddr() {
			return
		}
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			fv := v.Field(i)
			omit := strings.Contains(tag, ",omitempty") || strings.Contains(tag, ",omitzero")
			if fv.Kind() == reflect.Slice && fv.IsNil() && !omit && f.Type.Elem().Kind() != reflect.Uint8 && fv.CanSet() && !f.Anonymous {
				fv.Set(reflect.MakeSlice(f.Type, 0, 0))
				continue
			}
			fillLists(fv, depth+1)
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		for i := 0; i < v.Len(); i++ {
			fillLists(v.Index(i), depth+1)
		}
	case reflect.Map:
		// A map's entries cannot be changed in place; those that are pointers or lists hold what they hold.
		if k := v.Type().Elem().Kind(); k == reflect.Pointer || k == reflect.Slice || k == reflect.Map {
			for _, key := range v.MapKeys() {
				fillLists(v.MapIndex(key), depth+1)
			}
		}
	}
}
