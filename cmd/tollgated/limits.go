package main

import (
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/ethosagent/tollgate"
)

var durationType = reflect.TypeFor[time.Duration]()

// snake converts a Go field name to its YAML key: MaxRequestBody ->
// max_request_body.
func snake(name string) string {
	rs := []rune(name)
	var b strings.Builder
	for i, r := range rs {
		// A word starts at an upper-case rune after a lower-case one, or at the
		// last upper-case rune of an acronym (SessionTTL -> session_ttl).
		if i > 0 && unicode.IsUpper(r) && (!unicode.IsUpper(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]))) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// applyLimits copies the limits section onto l. Keys are the snake_case names
// of tollgate.Limits fields; durations are strings ("30s"), the rest integers.
// Because the keys come from the struct itself, no field can be invented.
func applyLimits(c *collector, raw map[string]any, l *tollgate.Limits) {
	v := reflect.ValueOf(l).Elem()
	fields := make(map[string]reflect.Value, v.NumField())
	for i := range v.NumField() {
		fields[snake(v.Type().Field(i).Name)] = v.Field(i)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		f, ok := fields[k]
		if !ok {
			c.addf("limits.%s: unknown limit", k)
			continue
		}
		setLimit(c, "limits."+k, f, raw[k])
	}
}

func setLimit(c *collector, field string, f reflect.Value, val any) {
	if f.Type() == durationType {
		s, ok := val.(string)
		if !ok {
			c.addf("%s: want a duration string such as \"30s\"", field)
			return
		}
		f.SetInt(int64(c.duration(field, s)))
		return
	}
	n, ok := val.(int)
	if !ok || n < 0 {
		c.addf("%s: want a non-negative integer", field)
		return
	}
	f.SetInt(int64(n))
}
