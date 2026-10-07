// Package i18n is Proxier's small message catalog: two languages compiled in,
// each module's texts as a Go map with EN and RU side by side, so a missing
// translation is a test failure and not a runtime surprise.
package i18n

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Lang is a supported language.
type Lang string

const (
	EN Lang = "en"
	RU Lang = "ru"
)

// Langs lists the supported languages.
var Langs = []Lang{EN, RU}

// Valid reports whether l is supported.
func (l Lang) Valid() bool { return slices.Contains(Langs, l) }

// Message is one text in both languages. Plural texts hold their forms
// separated by "|": EN one|other, RU one|few|many.
type Message struct{ EN, RU string }

// Messages is a module's catalog: key → message.
type Messages map[string]Message

// Args fill {name} placeholders.
type Args map[string]any

// Catalog holds every module's messages.
type Catalog struct {
	mu    sync.RWMutex
	msgs  map[string]Message
	owner map[string]string
}

// NewCatalog returns an empty catalog.
func NewCatalog() *Catalog {
	return &Catalog{msgs: map[string]Message{}, owner: map[string]string{}}
}

var placeholder = regexp.MustCompile(`\{([a-z_][a-z0-9_]*)\}`)

// placeholders lists the {names} in s, ignoring escaped "{{".
func placeholders(s string) []string {
	s = strings.ReplaceAll(s, "{{", "")
	var out []string
	for _, m := range placeholder.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// Add registers a module's messages. A key added twice, a message missing a
// language, or EN and RU with different placeholder sets is an error; nothing
// is added then.
func (c *Catalog) Add(module string, m Messages) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for k, msg := range m {
		switch {
		case msg.EN == "" || msg.RU == "":
			errs = append(errs, fmt.Errorf("i18n: %s: %q is missing a language", module, k))
		case !slices.Equal(placeholders(msg.EN), placeholders(msg.RU)):
			errs = append(errs, fmt.Errorf("i18n: %s: %q has different placeholders in EN and RU", module, k))
		}
		if o, dup := c.owner[k]; dup {
			errs = append(errs, fmt.Errorf("i18n: %s: %q is already defined by %s", module, k, o))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	for k, msg := range m {
		c.msgs[k] = msg
		c.owner[k] = module
	}
	return nil
}

// Has reports whether key is defined.
func (c *Catalog) Has(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.msgs[key]
	return ok
}

// Keys lists every defined key, sorted.
func (c *Catalog) Keys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.msgs))
	for k := range c.msgs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *Catalog) get(key string) (Message, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.msgs[key]
	return m, ok
}
