// Package settings stores what the admin configures in Settings
// (docs/modules/platform.md#settings). Each module declares its own sections:
// fields with a kind, a default and validation. A stored row exists only for a
// value that differs from its default, so a changed default reaches every
// install that never touched it. Secret values are sealed by the vault, and a
// change records settings.changed with the keys, never the values.
package settings

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

// ChangedEvent is recorded for every save that changed something. The
// platform declares it.
var ChangedEvent = events.Type{
	Name: "settings.changed", Module: "platform",
	Description: "A settings section was changed (the keys, never the values).",
}

// Kind is the type of a field's value.
type Kind string

const (
	String   Kind = "string"
	Int      Kind = "int"
	Bool     Kind = "bool"
	Duration Kind = "duration"
	Enum     Kind = "enum"
	Secret   Kind = "secret"
)

// Field is one setting.
type Field struct {
	// Key is "<section>.<name>", e.g. "general.time_zone".
	Key     string
	Kind    Kind
	Default string
	// Options are the allowed values of an Enum.
	Options []string
	// Min and Max bound an Int or a Duration (as nanoseconds) when Max > Min.
	Min, Max int64
	// MaxLen bounds a String or a Secret in characters; 0 means 1000.
	MaxLen int
	// Validate checks the normalised value further.
	Validate func(string) error
}

// Section is a group of fields shown and saved together.
type Section struct {
	// Name is the key prefix and the subject of settings.changed.
	Name   string
	Module string
	Fields []Field
}

// FieldErrors maps a key to why its value was refused. A Set that returns
// FieldErrors wrote nothing.
type FieldErrors map[string]string

func (fe FieldErrors) Error() string {
	keys := slices.Sorted(maps.Keys(fe))
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + ": " + fe[k]
	}
	return "invalid settings: " + strings.Join(parts, "; ")
}

// ErrUnknownKey is returned for a key no section declared.
var ErrUnknownKey = errors.New("settings: unknown key")

// Store reads and writes settings.
type Store struct {
	db     *db.DB
	vault  *vault.Vault
	events *events.Catalog

	mu       sync.RWMutex
	sections map[string]Section
	fields   map[string]Field
}

// New returns a store with no sections.
func New(d *db.DB, v *vault.Vault, c *events.Catalog) *Store {
	return &Store{db: d, vault: v, events: c, sections: map[string]Section{}, fields: map[string]Field{}}
}

// Register declares sections. Defaults must pass their own validation.
func (s *Store) Register(sections ...Section) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, sec := range sections {
		if sec.Name == "" || strings.Contains(sec.Name, ".") {
			errs = append(errs, fmt.Errorf("settings: section name %q", sec.Name))
			continue
		}
		if _, dup := s.sections[sec.Name]; dup {
			errs = append(errs, fmt.Errorf("settings: section %q registered twice", sec.Name))
			continue
		}
		var secErrs []error
		keys := map[string]bool{}
		for _, f := range sec.Fields {
			if !strings.HasPrefix(f.Key, sec.Name+".") {
				secErrs = append(secErrs, fmt.Errorf("settings: key %q is outside section %q", f.Key, sec.Name))
			}
			if _, dup := s.fields[f.Key]; dup || keys[f.Key] {
				secErrs = append(secErrs, fmt.Errorf("settings: key %q registered twice", f.Key))
			}
			keys[f.Key] = true
			if norm, err := f.normalize(f.Default); err != nil {
				secErrs = append(secErrs, fmt.Errorf("settings: default of %s: %w", f.Key, err))
			} else if norm != f.Default {
				secErrs = append(secErrs, fmt.Errorf("settings: default of %s is %q, write it as %q", f.Key, f.Default, norm))
			}
		}
		if len(secErrs) > 0 {
			errs = append(errs, secErrs...)
			continue
		}
		s.sections[sec.Name] = sec
		for _, f := range sec.Fields {
			s.fields[f.Key] = f
		}
	}
	return errors.Join(errs...)
}

// Sections returns the registered sections, sorted by name.
func (s *Store) Sections() []Section {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.SortedFunc(maps.Values(s.sections), func(a, b Section) int { return cmp.Compare(a.Name, b.Name) })
}

// Field returns a registered field.
func (s *Store) Field(key string) (Field, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.fields[key]
	return f, ok
}

type row struct {
	Value  sql.NullString `db:"value"`
	Secret []byte         `db:"secret_value"`
}

func aad(key string) string { return "setting:" + key }

// Value is a setting's current value and whether it is the default.
type Value struct {
	Field     Field
	Value     string
	IsDefault bool
}

// Lookup returns the current value of key, decrypted when secret.
func (s *Store) Lookup(ctx context.Context, key string) (Value, error) {
	f, ok := s.Field(key)
	if !ok {
		return Value{}, fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
	v, stored, err := s.read(ctx, s.db.R, f)
	if err != nil {
		return Value{}, err
	}
	return Value{Field: f, Value: v, IsDefault: !stored}, nil
}

func (s *Store) read(ctx context.Context, q sqlx.QueryerContext, f Field) (string, bool, error) {
	var r row
	err := sqlx.GetContext(ctx, q, &r, `SELECT value, secret_value FROM settings WHERE key = ?`, f.Key)
	if errors.Is(err, sql.ErrNoRows) {
		return f.Default, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("settings: read %s: %w", f.Key, err)
	}
	if r.Secret != nil {
		v, err := s.vault.OpenString(r.Secret, aad(f.Key))
		if err != nil {
			return "", false, fmt.Errorf("settings: open %s: %w", f.Key, err)
		}
		return v, true, nil
	}
	return r.Value.String, true, nil
}

// Get returns the current value of key as text.
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	v, err := s.Lookup(ctx, key)
	return v.Value, err
}

// GetInt returns an Int setting.
func (s *Store) GetInt(ctx context.Context, key string) (int64, error) {
	v, err := s.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// GetBool returns a Bool setting.
func (s *Store) GetBool(ctx context.Context, key string) (bool, error) {
	v, err := s.Get(ctx, key)
	if err != nil {
		return false, err
	}
	return v == "true", nil
}

// GetDuration returns a Duration setting.
func (s *Store) GetDuration(ctx context.Context, key string) (time.Duration, error) {
	v, err := s.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	return time.ParseDuration(v)
}

// Set saves values (key → text) of one section as actor. Keys not in values
// keep their value. Every value is validated first: if any is refused, Set
// returns FieldErrors and writes nothing. A value equal to its default removes
// the stored row. If anything changed, settings.changed is recorded in the
// same transaction, listing the changed keys.
func (s *Store) Set(ctx context.Context, actor, section string, values map[string]string) error {
	s.mu.RLock()
	_, ok := s.sections[section]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("settings: unknown section %q", section)
	}

	type change struct {
		f    Field
		norm string
	}
	var changes []change
	fe := FieldErrors{}
	for key, raw := range values {
		f, ok := s.Field(key)
		if !ok || !strings.HasPrefix(key, section+".") {
			return fmt.Errorf("%w: %q in section %q", ErrUnknownKey, key, section)
		}
		norm, err := f.normalize(raw)
		if err != nil {
			fe[key] = err.Error()
			continue
		}
		changes = append(changes, change{f, norm})
	}
	if len(fe) > 0 {
		return fe
	}
	slices.SortFunc(changes, func(a, b change) int { return cmp.Compare(a.f.Key, b.f.Key) })

	return s.db.Write(ctx, func(tx *sqlx.Tx) error {
		var changed []string
		for _, c := range changes {
			cur, _, err := s.read(ctx, tx, c.f)
			if err != nil {
				return err
			}
			if cur == c.norm {
				continue
			}
			if err := s.write(ctx, tx, c.f, c.norm); err != nil {
				return err
			}
			changed = append(changed, c.f.Key)
		}
		if len(changed) == 0 {
			return nil
		}
		_, err := s.events.Record(ctx, tx, events.Event{
			Type:    ChangedEvent.Name,
			Subject: events.Subject{Type: "settings", ID: section},
			Actor:   actor,
			Payload: map[string]any{"keys": changed},
		})
		return err
	})
}

func (s *Store) write(ctx context.Context, tx *sqlx.Tx, f Field, v string) error {
	if v == f.Default {
		_, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, f.Key)
		return err
	}
	var value, secret any
	if f.Kind == Secret {
		secret = s.vault.SealString(v, aad(f.Key))
	} else {
		value = v
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO settings (key, value, secret_value, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, secret_value = excluded.secret_value, updated_at = excluded.updated_at`,
		f.Key, value, secret, db.Now())
	if err != nil {
		return fmt.Errorf("settings: write %s: %w", f.Key, err)
	}
	return nil
}

// normalize checks raw against the field and returns its canonical text.
func (f Field) normalize(raw string) (string, error) {
	v := raw
	if f.Kind != Secret {
		v = strings.TrimSpace(raw)
	}
	switch f.Kind {
	case String, Secret:
		limit := cmp.Or(f.MaxLen, 1000)
		if utf8.RuneCountInString(v) > limit {
			return "", fmt.Errorf("at most %d characters", limit)
		}
		if !utf8.ValidString(v) {
			return "", errors.New("not valid text")
		}
	case Int:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return "", errors.New("must be a whole number")
		}
		if f.Max > f.Min && (n < f.Min || n > f.Max) {
			return "", fmt.Errorf("must be from %d to %d", f.Min, f.Max)
		}
		v = strconv.FormatInt(n, 10)
	case Bool:
		switch strings.ToLower(v) {
		case "true", "on", "1", "yes":
			v = "true"
		case "false", "off", "0", "no", "":
			v = "false"
		default:
			return "", errors.New("must be on or off")
		}
	case Duration:
		d, err := time.ParseDuration(v)
		if err != nil {
			return "", errors.New("must be a duration such as 5m or 1h30m")
		}
		if f.Max > f.Min && (int64(d) < f.Min || int64(d) > f.Max) {
			return "", fmt.Errorf("must be from %s to %s", time.Duration(f.Min), time.Duration(f.Max))
		}
		v = d.String()
	case Enum:
		if !slices.Contains(f.Options, v) {
			return "", fmt.Errorf("must be one of %s", strings.Join(f.Options, ", "))
		}
	default:
		return "", fmt.Errorf("unknown kind %q", f.Kind)
	}
	if f.Validate != nil {
		if err := f.Validate(v); err != nil {
			return "", err
		}
	}
	return v, nil
}
