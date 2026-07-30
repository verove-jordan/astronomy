// Non-finite-float sanitizing for the persisted run record. encoding/json refuses NaN/±Inf, and a
// single rogue metric silently cost task #353 both its run.json and its stored job result (both
// persist paths swallowed the marshal error). Zeroing the values with a visible note beats losing
// the whole record.
package pipeline

import (
	"fmt"
	"math"
	"reflect"
	"strings"
)

// SanitizeNonFinite zeroes every NaN/±Inf float reachable from v through exported fields (the same
// set encoding/json serializes) and returns the JSON-style paths it touched, e.g.
// "channels[2].metrics[14].wfwhm". Values it cannot address (inside interfaces holding non-pointer
// values) are left as-is.
func SanitizeNonFinite(v any) []string {
	var touched []string
	sanitizeValue(reflect.ValueOf(v), "", &touched)
	return touched
}

func sanitizeValue(rv reflect.Value, path string, touched *[]string) {
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !rv.IsNil() {
			sanitizeValue(rv.Elem(), path, touched)
		}
	case reflect.Struct:
		t := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" { // unexported: invisible to JSON
				continue
			}
			sanitizeValue(rv.Field(i), path+"."+jsonFieldName(f), touched)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			sanitizeValue(rv.Index(i), fmt.Sprintf("%s[%d]", path, i), touched)
		}
	case reflect.Map:
		for _, k := range rv.MapKeys() {
			// Map values are not addressable: sanitize a copy and write it back only when touched.
			cp := reflect.New(rv.MapIndex(k).Type()).Elem()
			cp.Set(rv.MapIndex(k))
			before := len(*touched)
			sanitizeValue(cp, fmt.Sprintf("%s[%v]", path, k.Interface()), touched)
			if len(*touched) > before {
				rv.SetMapIndex(k, cp)
			}
		}
	case reflect.Float32, reflect.Float64:
		if f := rv.Float(); (math.IsNaN(f) || math.IsInf(f, 0)) && rv.CanSet() {
			rv.SetFloat(0)
			*touched = append(*touched, strings.TrimPrefix(path, "."))
		}
	}
}

// jsonFieldName mirrors encoding/json's field naming: the tag name when present, else the Go name.
func jsonFieldName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" || tag == "-" {
		return f.Name
	}
	if i := strings.Index(tag, ","); i >= 0 {
		tag = tag[:i]
	}
	if tag == "" {
		return f.Name
	}
	return tag
}

// nonFiniteNote renders the sanitized paths as one bounded warning line.
func nonFiniteNote(paths []string) string {
	const show = 8
	head := paths
	if len(head) > show {
		head = head[:show]
	}
	note := "run record contained non-finite numbers (zeroed): " + strings.Join(head, ", ")
	if extra := len(paths) - len(head); extra > 0 {
		note += fmt.Sprintf(" … and %d more", extra)
	}
	return note
}
