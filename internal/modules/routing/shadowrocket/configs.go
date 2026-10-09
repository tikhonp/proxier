package shadowrocket

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

// Limits of a config's fields.
const (
	MaxPolicy = 64
	MaxNote   = 200
	// DefaultPolicy is the rule policy of a new config.
	DefaultPolicy = "PROXY"
)

var nameShape = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// Config is a hosted Shadowrocket config.
type Config struct {
	ID          int64
	Name        string
	ListID      int64
	List        string
	Policy      string
	Enabled     bool
	Version     int // the current base version
	Rules       int // rules it serves now (pages)
	CreatedAt   time.Time
	LastFetchAt time.Time
	LastFetchUA string
}

// File is the file name the URL ends in.
func (c Config) File() string { return c.Name + ".conf" }

// Version is a base config version.
type Version struct {
	Number    int
	Content   string // empty in Versions
	Note      string
	CreatedAt time.Time
}

// New is what a new config is made of.
type New struct {
	Name   string
	ListID int64
	Policy string
	Base   string
	Note   string
}

// Fetch is a line of the fetch log.
type Fetch struct {
	ID        int64
	At        time.Time
	IP        string
	UserAgent string
}

// Deps are what the service uses.
type Deps struct {
	DB      *db.DB
	Vault   *vault.Vault
	Events  *events.Catalog
	Lists   *lists.Service
	Fetch   *sources.Fetcher // Import from URL
	BaseURL *url.URL
	Now     func() time.Time
	Log     *slog.Logger
}

// Service manages the configs and serves them.
type Service struct{ d Deps }

// NewService returns the service (New is the create form's type).
func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Service{d: d}
}

// Subject is a config's event subject.
func Subject(id int64) events.Subject {
	return events.Subject{Type: "shadowrocket", ID: strconv.FormatInt(id, 10)}
}

func tokenAAD(id int64) string { return "shadowrocket:" + strconv.FormatInt(id, 10) + ":token" }

func (s *Service) record(ctx context.Context, tx *sqlx.Tx, typ string, id int64, actor string, payload map[string]any) error {
	_, err := s.d.Events.Record(ctx, tx, events.Event{Time: db.At(s.d.Now()), Type: typ, Subject: Subject(id), Actor: actor, Payload: payload})
	return err
}

func toConfig(r store.Shadowrocket) Config {
	return Config{
		ID: r.ID, Name: r.Name, ListID: r.ListID, List: r.List, Policy: r.Policy, Enabled: r.Enabled, Version: r.Version,
		CreatedAt: r.CreatedAt.Time, LastFetchAt: r.LastFetchAt.Time, LastFetchUA: r.LastFetchUA,
	}
}

// CleanName is a name as it is stored: trimmed, lower case.
func CleanName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// CheckName puts a bad or taken name (already cleaned) into fe under field.
func CheckName(ctx context.Context, q sqlx.QueryerContext, name string, except int64, field string, fe store.FieldErrors) error {
	if !nameShape.MatchString(name) {
		fe[field] = "shadowrocket.err.name"
		return nil
	}
	taken, err := store.ShadowrocketNameTaken(ctx, q, name, except)
	if taken {
		fe[field] = "shadowrocket.err.name_taken"
	}
	return err
}

// checkPolicy returns the policy to store, or puts its error into fe.
func checkPolicy(policy string, fe store.FieldErrors) string {
	policy = strings.TrimSpace(policy)
	if n := utf8.RuneCountInString(policy); n < 1 || n > MaxPolicy || strings.ContainsAny(policy, ",\r\n") {
		fe["policy"] = "shadowrocket.err.policy"
	}
	return policy
}

// checkBase puts a base config's error into fe.
func checkBase(base string, fe store.FieldErrors) {
	switch err := Validate([]byte(base)); {
	case errors.Is(err, ErrNoRule):
		fe["base"] = "shadowrocket.err.no_rule"
	case errors.Is(err, ErrTooBig):
		fe["base"] = "shadowrocket.err.too_big"
	case errors.Is(err, ErrNotUTF8):
		fe["base"] = "shadowrocket.err.utf8"
	}
}

func checkNote(note string, fe store.FieldErrors) string {
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > MaxNote {
		fe["note"] = "shadowrocket.err.note"
	}
	return note
}

func checkList(ctx context.Context, q sqlx.QueryerContext, listID int64, fe store.FieldErrors) (store.List, error) {
	l, err := store.GetList(ctx, q, listID)
	if errors.Is(err, store.ErrNotFound) {
		fe["list"] = "shadowrocket.err.list"
		return l, nil
	}
	return l, err
}

// Create makes a config with base version 1 and a new token. Errors are
// store.FieldErrors (nothing created).
func (s *Service) Create(ctx context.Context, n New, actor string) (int64, error) {
	var id int64
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		id, err = s.CreateTx(ctx, tx, n, actor)
		return err
	})
	return id, err
}

// CreateTx is Create in the caller's transaction (the import).
func (s *Service) CreateTx(ctx context.Context, tx *sqlx.Tx, n New, actor string) (int64, error) {
	fe := store.FieldErrors{}
	name := CleanName(n.Name)
	if err := CheckName(ctx, tx, name, 0, "name", fe); err != nil {
		return 0, err
	}
	policy := checkPolicy(n.Policy, fe)
	checkBase(n.Base, fe)
	note := checkNote(n.Note, fe)
	l, err := checkList(ctx, tx, n.ListID, fe)
	if err != nil {
		return 0, err
	}
	if len(fe) > 0 {
		return 0, fe
	}
	token := vault.NewToken()
	lookup := s.d.Vault.Lookup(token)
	now := db.At(s.d.Now())
	id, err := store.InsertShadowrocket(ctx, tx, store.Shadowrocket{Name: name, ListID: l.ID, Policy: policy, CreatedAt: now}, lookup)
	if err != nil {
		if store.Unique(err, "routing_shadowrocket.name") {
			return 0, store.FieldErrors{"name": "shadowrocket.err.name_taken"}
		}
		return 0, err
	}
	if err := store.SetShadowrocketToken(ctx, tx, id, s.d.Vault.SealString(token, tokenAAD(id)), lookup); err != nil {
		return 0, err
	}
	if err := store.InsertShadowrocketVersion(ctx, tx, id, store.ShadowrocketVersion{Number: 1, Content: n.Base, Note: note, CreatedAt: now}); err != nil {
		return 0, err
	}
	return id, s.record(ctx, tx, "routing.shadowrocket_created", id, actor, map[string]any{"name": name, "list": l.Name, "policy": policy})
}

// Get reads one config with the number of rules it serves now.
func (s *Service) Get(ctx context.Context, id int64) (Config, error) {
	r, err := store.GetShadowrocket(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return Config{}, ErrNotFound
	}
	if err != nil {
		return Config{}, err
	}
	c := toConfig(r)
	blocks, err := s.blocks(ctx, c.ListID)
	c.Rules = Count(blocks)
	return c, err
}

// List reads every config by name, each with the rules it serves now.
func (s *Service) List(ctx context.Context) ([]Config, error) {
	rows, err := store.ShadowrocketConfigs(ctx, s.d.DB.R)
	if err != nil {
		return nil, err
	}
	return s.withRules(ctx, rows)
}

// ByList reads the configs following a list.
func (s *Service) ByList(ctx context.Context, listID int64) ([]Config, error) {
	rows, err := store.ShadowrocketOfList(ctx, s.d.DB.R, listID)
	if err != nil {
		return nil, err
	}
	return s.withRules(ctx, rows)
}

func (s *Service) withRules(ctx context.Context, rows []store.Shadowrocket) ([]Config, error) {
	counts := map[int64]int{}
	out := make([]Config, 0, len(rows))
	for _, r := range rows {
		c := toConfig(r)
		n, ok := counts[c.ListID]
		if !ok {
			blocks, err := s.blocks(ctx, c.ListID)
			if err != nil {
				return nil, err
			}
			n = Count(blocks)
			counts[c.ListID] = n
		}
		c.Rules = n
		out = append(out, c)
	}
	return out, nil
}

// blocks are a list's rules as they are now.
func (s *Service) blocks(ctx context.Context, listID int64) ([]Block, error) {
	v, err := s.d.Lists.View(ctx, listID, nil)
	if err != nil {
		return nil, err
	}
	return Rules(v), nil
}

// token opens a config's token.
func (s *Service) token(ctx context.Context, id int64) (string, error) {
	blob, err := store.ShadowrocketToken(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return s.d.Vault.OpenString(blob, tokenAAD(id))
}

// URL is a config's address with its token in clear: for its page only.
func (s *Service) URL(ctx context.Context, id int64) (string, error) {
	c, err := store.GetShadowrocket(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	token, err := s.token(ctx, id)
	if err != nil {
		return "", err
	}
	return s.base() + "/r/" + token + "/" + c.Name + ".conf", nil
}

// MaskedURL is the address with its token hidden.
func (s *Service) MaskedURL(name string) string {
	return s.base() + "/r/" + strings.Repeat("•", 16) + "/" + name + ".conf"
}

func (s *Service) base() string {
	if s.d.BaseURL == nil {
		return ""
	}
	return strings.TrimSuffix(s.d.BaseURL.String(), "/")
}

// change runs fn on a config inside one transaction.
func (s *Service) change(ctx context.Context, id int64, fn func(tx *sqlx.Tx, c store.Shadowrocket) error) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		c, err := store.GetShadowrocket(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return fn(tx, c)
	})
}

// Edit gives a config another routing list or policy, from the next fetch
// on. Errors are store.FieldErrors; nothing changed records nothing.
func (s *Service) Edit(ctx context.Context, id, listID int64, policy, actor string) (bool, error) {
	changed := false
	err := s.change(ctx, id, func(tx *sqlx.Tx, c store.Shadowrocket) error {
		fe := store.FieldErrors{}
		policy = checkPolicy(policy, fe)
		l, err := checkList(ctx, tx, listID, fe)
		if err != nil {
			return err
		}
		if len(fe) > 0 {
			return fe
		}
		var changes []string
		payload := map[string]any{}
		if l.ID != c.ListID {
			changes = append(changes, "list")
			payload["from"], payload["to"] = c.List, l.Name
		}
		if policy != c.Policy {
			changes = append(changes, "policy")
			payload["policy_from"], payload["policy"] = c.Policy, policy
		}
		if len(changes) == 0 {
			return nil
		}
		changed = true
		if err := store.SetShadowrocketFields(ctx, tx, id, l.ID, policy); err != nil {
			return err
		}
		payload["changes"] = strings.Join(changes, ", ")
		return s.record(ctx, tx, "routing.shadowrocket_updated", id, actor, payload)
	})
	return changed, err
}

// RegenerateToken gives the config a new URL; the old one answers 404 at once.
func (s *Service) RegenerateToken(ctx context.Context, id int64, actor string) error {
	token := vault.NewToken()
	return s.change(ctx, id, func(tx *sqlx.Tx, _ store.Shadowrocket) error {
		if err := store.SetShadowrocketToken(ctx, tx, id, s.d.Vault.SealString(token, tokenAAD(id)), s.d.Vault.Lookup(token)); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.shadowrocket_updated", id, actor, map[string]any{"changes": "token"})
	})
}

// SetEnabled disables a config (its URL answers 404) or enables it again;
// one already so records nothing.
func (s *Service) SetEnabled(ctx context.Context, id int64, enabled bool, actor string) error {
	return s.change(ctx, id, func(tx *sqlx.Tx, c store.Shadowrocket) error {
		if c.Enabled == enabled {
			return nil
		}
		if err := store.SetShadowrocketEnabled(ctx, tx, id, enabled); err != nil {
			return err
		}
		word := "disabled"
		if enabled {
			word = "enabled"
		}
		return s.record(ctx, tx, "routing.shadowrocket_updated", id, actor, map[string]any{"changes": word})
	})
}

// Delete deletes a config with its versions and fetch log; its URL answers 404.
func (s *Service) Delete(ctx context.Context, id int64, actor string) error {
	return s.change(ctx, id, func(tx *sqlx.Tx, c store.Shadowrocket) error {
		if err := s.record(ctx, tx, "routing.shadowrocket_deleted", id, actor, map[string]any{"name": c.Name, "list": c.List}); err != nil {
			return err
		}
		return store.DeleteShadowrocket(ctx, tx, id)
	})
}

// Output is what a fetch of the config would answer now. Rendering it
// records nothing.
func (s *Service) Output(ctx context.Context, id int64) ([]byte, error) {
	r, err := store.GetShadowrocket(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.render(ctx, r)
}

func (s *Service) render(ctx context.Context, r store.Shadowrocket) ([]byte, error) {
	v, err := store.GetShadowrocketVersion(ctx, s.d.DB.R, r.ID, 0)
	if err != nil {
		return nil, err
	}
	blocks, err := s.blocks(ctx, r.ListID)
	if err != nil {
		return nil, err
	}
	return Render([]byte(v.Content), r.List, blocks, r.Policy, s.d.Now())
}

// Interactive bounds Import from URL inside a request.
const Interactive = 45 * time.Second

// HTTPError is a base config's address answering other than 200.
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return "shadowrocket: HTTP " + strconv.Itoa(e.Status) }

// ImportBase reads a base config once from an http(s) address. Nothing is
// saved: the page puts it into the field.
func (s *Service) ImportBase(ctx context.Context, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", ErrBadURL
	}
	ctx, cancel := context.WithTimeout(ctx, Interactive)
	defer cancel()
	status, body, err := s.d.Fetch.Get(ctx, rawURL, MaxBase)
	if errors.Is(err, sources.ErrTooBig) {
		return "", ErrTooBig
	}
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", &HTTPError{Status: status}
	}
	if !utf8.Valid(body) {
		return "", ErrNotUTF8
	}
	return string(body), nil
}

// ErrBadURL: Import from URL takes only http(s) addresses.
var ErrBadURL = errors.New("shadowrocket: only http:// and https:// addresses")
