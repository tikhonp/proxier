package render

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"text/template"
)

// Funcs is the whole function map of templates. Nothing reaches the
// environment or the file system (docs/modules/servers.md#rendering); the
// builtins of text/template (if, eq, printf, len, index…) stay.
func Funcs() template.FuncMap {
	return template.FuncMap{
		"json":     toJSON,
		"quote":    toJSON,
		"default":  def,
		"lower":    strings.ToLower,
		"upper":    strings.ToUpper,
		"trim":     strings.TrimSpace,
		"urlquery": url.QueryEscape,
		"b64enc":   func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) },
		"sha256": func(s string) string {
			h := sha256.Sum256([]byte(s))
			return hex.EncodeToString(h[:])
		},
		"join":   join,
		"indent": indent,
	}
}

// toJSON marshals v without HTML escaping, so "<" and "&" stay as written.
func toJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// def returns v, or d when v is empty: `{{ .Params.x | default "y" }}`.
func def(d, v any) any {
	if v == nil {
		return d
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String, reflect.Slice, reflect.Map, reflect.Array:
		if rv.Len() == 0 {
			return d
		}
	case reflect.Bool:
		if !rv.Bool() {
			return d
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if rv.Int() == 0 {
			return d
		}
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return d
		}
	}
	return v
}

// join is `{{ .List | join "," }}`: the separator first, so it pipes.
func join(sep string, list any) (string, error) {
	rv := reflect.ValueOf(list)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return "", fmt.Errorf("join: %T is not a list", list)
	}
	parts := make([]string, rv.Len())
	for i := range parts {
		parts[i] = fmt.Sprint(rv.Index(i).Interface())
	}
	return strings.Join(parts, sep), nil
}

// indent prefixes every line (the first too) with n spaces: `{{ .X | indent 4 }}`.
func indent(n int, s string) string {
	if n < 0 {
		n = 0
	}
	pad := strings.Repeat(" ", n)
	return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
}
