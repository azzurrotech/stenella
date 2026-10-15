// codec.go — record encoding between Go structs and pod's map[string]string.
//
// pod stores every row as a plain XML file whose fields are name/value text
// pairs (pod.Field), so a record type has to be flattened to
// map[string]string and rebuilt. Rather than hand-writing that for nine record
// types, the mapping is derived from the struct itself with reflection:
//
//   - each exported field's JSON name becomes the pod field name;
//   - a `,omitempty` field is left out of the map when it is its zero value;
//   - strings, bools, numbers and time.Time map to their natural text form;
//   - slices, maps and nested structs are stored as compact JSON.
//
// The pod XML therefore stays readable — the field names are exactly the names
// the API uses — and adding a field to a record needs no migration code: pod's
// schema model is a hint, not a gate, so a new field is observed on the next
// write (see pod's ObserveFields).
package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

var timeType = reflect.TypeOf(time.Time{})

// jsonTag returns the field's pod/JSON name and whether it is omitempty.
func jsonTag(f reflect.StructField) (name string, omit bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "-", false
	}
	name = f.Name
	if tag == "" {
		return name, false
	}
	parts := strings.Split(tag, ",")
	if parts[0] != "" {
		name = parts[0]
	}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			omit = true
		}
	}
	return name, omit
}

// fieldsOf flattens a record struct into pod's field map.
func fieldsOf(v any) (map[string]string, error) {
	rv, err := structValue(v)
	if err != nil {
		return nil, err
	}
	rt := rv.Type()
	out := make(map[string]string, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		name, omit := jsonTag(f)
		if name == "-" {
			continue
		}
		s, skip, err := encodeValue(rv.Field(i), omit)
		if err != nil {
			return nil, fmt.Errorf("codec: %s.%s: %w", rt.Name(), f.Name, err)
		}
		if skip {
			continue
		}
		out[name] = s
	}
	return out, nil
}

// applyFields rebuilds a record struct from pod's field map. Fields absent from
// the map keep their zero value, so an older record stays readable.
func applyFields(m map[string]string, v any) error {
	rv, err := structValue(v)
	if err != nil {
		return err
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name, _ := jsonTag(f)
		if name == "-" {
			continue
		}
		s, ok := m[name]
		if !ok {
			continue
		}
		if err := decodeValue(s, rv.Field(i)); err != nil {
			return fmt.Errorf("codec: %s.%s: %w", rt.Name(), f.Name, err)
		}
	}
	return nil
}

func structValue(v any) (reflect.Value, error) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return reflect.Value{}, fmt.Errorf("codec: nil %T", v)
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("codec: %T is not a struct", v)
	}
	return rv, nil
}

// encodeValue renders one field as text. skip reports that an omitempty field
// was empty and should not be written.
func encodeValue(fv reflect.Value, omit bool) (s string, skip bool, err error) {
	if !fv.IsValid() {
		return "", true, nil
	}
	if fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			return "", true, nil
		}
		fv = fv.Elem()
	}
	if fv.Type() == timeType {
		t := fv.Interface().(time.Time)
		if t.IsZero() {
			return "", omit, nil
		}
		return t.UTC().Format(time.RFC3339), false, nil
	}
	switch fv.Kind() {
	case reflect.String:
		s = fv.String()
		return s, omit && s == "", nil
	case reflect.Bool:
		b := fv.Bool()
		if omit && !b {
			return "", true, nil
		}
		return strconv.FormatBool(b), false, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := fv.Int()
		if omit && n == 0 {
			return "", true, nil
		}
		return strconv.FormatInt(n, 10), false, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n := fv.Uint()
		if omit && n == 0 {
			return "", true, nil
		}
		return strconv.FormatUint(n, 10), false, nil
	case reflect.Float32, reflect.Float64:
		f := fv.Float()
		if omit && f == 0 {
			return "", true, nil
		}
		return strconv.FormatFloat(f, 'g', -1, 64), false, nil
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
		if omit && fv.Len() == 0 {
			return "", true, nil
		}
		b, err := json.Marshal(fv.Interface())
		if err != nil {
			return "", false, err
		}
		return string(b), false, nil
	}
	return "", false, fmt.Errorf("unsupported field kind %s", fv.Kind())
}

// decodeValue parses one field's text back into the struct field.
func decodeValue(s string, fv reflect.Value) error {
	if fv.Type() == timeType {
		t, err := parseTime(s)
		if err != nil {
			return err
		}
		fv.Set(reflect.ValueOf(t))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(s)
	case reflect.Bool:
		b, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return err
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return err
		}
		fv.SetFloat(f)
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
		return json.Unmarshal([]byte(s), fv.Addr().Interface())
	default:
		return fmt.Errorf("unsupported field kind %s", fv.Kind())
	}
	return nil
}

// parseTime accepts the RFC 3339 form the codec writes, and degrades to the
// plain textual forms pod's HTML forms submit.
func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, l := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("codec: cannot parse time %q", s)
}

// columnNames derives pod column definitions from a record struct, so a table's
// declared schema always matches the type that fills it.
func columnNames(sample any) ([]string, error) {
	rv, err := structValue(sample)
	if err != nil {
		return nil, err
	}
	rt := rv.Type()
	names := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name, _ := jsonTag(f)
		if name == "-" {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}
