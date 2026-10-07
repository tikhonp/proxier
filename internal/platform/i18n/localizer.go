package i18n

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Localizer renders for one language and display time zone.
type Localizer struct {
	Lang Lang
	TZ   *time.Location
	cat  *Catalog
	now  func() time.Time
}

// Localizer returns a renderer for l and tz (UTC when nil).
func (c *Catalog) Localizer(l Lang, tz *time.Location) *Localizer {
	if !l.Valid() {
		l = EN
	}
	if tz == nil {
		tz = time.UTC
	}
	return &Localizer{Lang: l, TZ: tz, cat: c, now: time.Now}
}

// WithNow returns a copy whose clock is now, for tests.
func (l *Localizer) WithNow(now func() time.Time) *Localizer {
	c := *l
	c.now = now
	return &c
}

func (l *Localizer) text(m Message) string {
	if l.Lang == RU {
		return m.RU
	}
	return m.EN
}

func (l *Localizer) lookup(key string) (string, bool) {
	if l.cat == nil {
		return "", false
	}
	m, ok := l.cat.get(key)
	if !ok {
		return "", false
	}
	return l.text(m), true
}

// T renders key. A missing key renders as the key itself.
func (l *Localizer) T(key string, args ...Args) string {
	s, ok := l.lookup(key)
	if !ok {
		return key
	}
	return l.fill(s, merge(args))
}

// N renders the plural form for n; {n} is the formatted count.
func (l *Localizer) N(key string, n int64, args ...Args) string {
	s, ok := l.lookup(key)
	if !ok {
		return key
	}
	a := merge(args)
	a["n"] = l.Number(n)
	forms := strings.Split(s, "|")
	i := l.pluralIndex(n)
	if i >= len(forms) {
		i = len(forms) - 1
	}
	return l.fill(forms[i], a)
}

// pluralIndex: EN one|other; RU one|few|many.
func (l *Localizer) pluralIndex(n int64) int {
	if n < 0 {
		n = -n
	}
	if l.Lang != RU {
		if n == 1 {
			return 0
		}
		return 1
	}
	m10, m100 := n%10, n%100
	switch {
	case m10 == 1 && m100 != 11:
		return 0
	case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
		return 1
	}
	return 2
}

func merge(args []Args) Args {
	out := Args{}
	for _, a := range args {
		for k, v := range a {
			out[k] = v
		}
	}
	return out
}

// fill replaces {name} with its argument; "{{" and "}}" are literal braces.
func (l *Localizer) fill(s string, a Args) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '{' {
			if i+1 < len(s) && s[i+1] == '{' {
				b.WriteByte('{')
				i++
				continue
			}
			if j := strings.IndexByte(s[i:], '}'); j > 0 {
				name := s[i+1 : i+j]
				if v, ok := a[name]; ok {
					b.WriteString(l.format(v))
					i += j
					continue
				}
			}
		}
		if c == '}' && i+1 < len(s) && s[i+1] == '}' {
			i++
		}
		b.WriteByte(c)
	}
	return b.String()
}

func (l *Localizer) format(v any) string {
	switch x := v.(type) {
	case int:
		return l.Number(int64(x))
	case int64:
		return l.Number(x)
	case string:
		return x
	case time.Time:
		return l.Time(x)
	case fmt.Stringer:
		return x.String()
	}
	return fmt.Sprint(v)
}

// Number groups thousands: EN "1,284", RU "1 284" (no-break space).
func (l *Localizer) Number(n int64) string {
	sep := ","
	if l.Lang == RU {
		sep = " "
	}
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(sep)
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// Time renders t in the display zone: "2026-10-07 16:52".
func (l *Localizer) Time(t time.Time) string {
	return t.In(l.TZ).Format("2006-01-02 15:04")
}

// Ago renders "4 min ago" / "4 мин назад"; older than two days it is a date.
func (l *Localizer) Ago(t time.Time) string {
	d := l.now().Sub(t)
	if d < 0 {
		d = 0
	}
	ru := l.Lang == RU
	switch {
	case d < time.Minute:
		if ru {
			return "только что"
		}
		return "just now"
	case d < time.Hour:
		n := int64(d / time.Minute)
		if ru {
			return fmt.Sprintf("%d мин назад", n)
		}
		return fmt.Sprintf("%d min ago", n)
	case d < 48*time.Hour:
		n := int64(d / time.Hour)
		if ru {
			return fmt.Sprintf("%d ч назад", n)
		}
		return fmt.Sprintf("%d h ago", n)
	case d < 60*24*time.Hour:
		n := int64(d / (24 * time.Hour))
		if ru {
			return fmt.Sprintf("%d дн назад", n)
		}
		return fmt.Sprintf("%d d ago", n)
	}
	return t.In(l.TZ).Format("2006-01-02")
}

type ctxKey struct{}

// WithLocalizer stores l in ctx.
func WithLocalizer(ctx context.Context, l *Localizer) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

var fallback = &Localizer{Lang: EN, TZ: time.UTC, now: time.Now}

// From returns the Localizer in ctx; EN/UTC without one, never nil.
func From(ctx context.Context) *Localizer {
	if l, ok := ctx.Value(ctxKey{}).(*Localizer); ok && l != nil {
		return l
	}
	return fallback
}

// T is shorthand for From(ctx).T, for templates.
func T(ctx context.Context, key string, args ...Args) string { return From(ctx).T(key, args...) }

// N is shorthand for From(ctx).N, for templates.
func N(ctx context.Context, key string, n int64, args ...Args) string {
	return From(ctx).N(key, n, args...)
}

// Has reports whether key is defined.
func (l *Localizer) Has(key string) bool {
	_, ok := l.lookup(key)
	return ok
}
