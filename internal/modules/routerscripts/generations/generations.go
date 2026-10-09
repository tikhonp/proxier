// Package generations fills a router script's version in for one router
// (docs/processes/router-scripts/script-generation.md, "generating"): the
// form built from the version, checking its values, generating in one
// transaction through subscriptions' LinkIssuer and routing's
// RouterRegistrar, and the generations themselves. A generation stores its
// values, never its file: the file is params.Fill(the version's body, the
// values) whenever it is asked for.
package generations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aymanbagabas/go-udiff"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/modules/subscriptions"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"github.com/tikhonp/proxier/internal/platform/tailnet"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

var (
	ErrNotFound = errors.New("generations: no such generation")
	// ErrCantGenerate: the script is archived (also scripts.ErrArchived) or
	// has no version.
	ErrCantGenerate = errors.New("generations: the script is archived or has no version")
)

// Value is one parameter's value in a generation.
type Value struct {
	Name    string
	Value   string // "" for a secret one
	Secret  bool
	Changed bool
	Source  string // "", "link", "router", "key"
}

// Generation is a version filled in for one router, with the link and the
// router as they are now.
type Generation struct {
	ID               int64
	ScriptID         int64
	Script, Slug     string
	Version          int
	Current          int // the script's current version now
	RouterName       string
	FileName         string
	Values           []Value // in script order
	Changed          []string
	LinkID           int64
	LinkName         string
	LinkCreated      bool
	RouterID         int64
	RouterRegistered bool
	HasKey           bool // a @fill proxier-ssh-key parameter got Proxier's key
	CreatedAt        time.Time
	CreatedBy        string
	// As they are now, through the ports and the platform:
	Link        *subscriptions.IssuedLink // nil: no port, no link, or gone (LinkGone)
	LinkGone    bool
	LinkChanged bool // the URL in the file isn't the link's URL now
	Router      *routing.RegisteredRouter
	RouterGone  bool
	KeyChanged  bool
}

// Row is a generation in a list (the script page's Generations area, search).
type Row struct {
	ID          int64
	ScriptID    int64
	Script      string
	RouterName  string
	Version     int
	CreatedAt   time.Time
	RouterState string // awaiting, active, paused, removing, removed, ""
}

// Deps are what the service uses.
type Deps struct {
	DB      *db.DB
	Vault   *vault.Vault
	Events  *events.Catalog
	SSH     *sshx.SSH
	Tailnet *tailnet.Node // may be nil
	Scripts *scripts.Service
	Links   subscriptions.LinkIssuer // may be nil
	Routers routing.RouterRegistrar  // may be nil
	I18n    *i18n.Catalog
	// BaseURL is Proxier's address, the start of every fetch URL.
	BaseURL *url.URL
	Now     func() time.Time
	Log     *slog.Logger
}

// Service runs generations.
type Service struct{ d Deps }

// NewService returns the service.
func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}
}

// HasLinks and HasRouters report whether the ports are there (absent ports
// hide their sections).
func (s *Service) HasLinks() bool   { return s.d.Links != nil }
func (s *Service) HasRouters() bool { return s.d.Routers != nil }

// Subject is a generation's event subject.
func Subject(id int64) events.Subject {
	return events.Subject{Type: "generation", ID: strconv.FormatInt(id, 10)}
}

func secretsAAD(id int64) string { return "generation:" + strconv.FormatInt(id, 10) + ":secrets" }

// secret: @secret parameters and the subscription link (its URL holds the
// link's token), with or without the subscriptions module.
func secret(p params.Param) bool {
	return p.Annotations.Secret || p.Annotations.Fill == params.FillLink
}

func (s *Service) row(ctx context.Context, id int64) (store.Generation, error) {
	g, err := store.GetGeneration(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return g, ErrNotFound
	}
	return g, err
}

// values reads a generation's values: the plain ones and the opened secrets.
func (s *Service) values(g store.Generation) (plain, secrets map[string]string, err error) {
	plain, secrets = map[string]string{}, map[string]string{}
	if err := json.Unmarshal([]byte(g.Vals), &plain); err != nil {
		return nil, nil, fmt.Errorf("generations: values of %d: %w", g.ID, err)
	}
	if len(g.Secrets) > 0 {
		b, err := s.d.Vault.Open(g.Secrets, secretsAAD(g.ID))
		if err != nil {
			return nil, nil, fmt.Errorf("generations: secrets of %d: %w", g.ID, err)
		}
		if err := json.Unmarshal(b, &secrets); err != nil {
			return nil, nil, fmt.Errorf("generations: secrets of %d: %w", g.ID, err)
		}
	}
	return plain, secrets, nil
}

func splitNames(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// Get reads a generation with its link, router and key as they are now.
func (s *Service) Get(ctx context.Context, id int64) (Generation, error) {
	g, err := s.row(ctx, id)
	if err != nil {
		return Generation{}, err
	}
	sc, err := s.d.Scripts.Get(ctx, g.ScriptID)
	if err != nil {
		return Generation{}, err
	}
	ver, err := s.d.Scripts.Version(ctx, g.ScriptID, g.Version)
	if err != nil {
		return Generation{}, err
	}
	plain, secrets, err := s.values(g)
	if err != nil {
		return Generation{}, err
	}
	out := Generation{
		ID: g.ID, ScriptID: g.ScriptID, Script: sc.Name, Slug: sc.Slug, Version: g.Version, Current: sc.Current,
		RouterName: g.RouterName, FileName: g.FileName, Changed: splitNames(g.Changed), LinkID: g.LinkID, LinkName: g.LinkName,
		LinkCreated: g.LinkCreated, RouterID: g.RouterID, RouterRegistered: g.RouterRegistered, CreatedAt: g.CreatedAt.Time, CreatedBy: g.CreatedBy,
	}
	changed := map[string]bool{}
	for _, n := range out.Changed {
		changed[n] = true
	}
	parsed := params.Parse([]byte(ver.Body))
	fileURL := ""
	for _, p := range parsed.Params {
		v := Value{Name: p.Name, Secret: secret(p), Changed: changed[p.Name]}
		if !v.Secret {
			v.Value = plain[p.Name]
		}
		switch fill := p.Annotations.Fill; {
		case fill == params.FillKey:
			v.Source, out.HasKey = "key", true
		case fill == params.FillLink && g.LinkID != 0:
			v.Source, fileURL = "link", secrets[p.Name]
		case (fill == params.FillList || fill == params.FillForwarder) && g.RouterID != 0:
			v.Source = "router"
		}
		out.Values = append(out.Values, v)
	}
	if g.LinkID != 0 && s.d.Links != nil {
		l, err := s.d.Links.Link(ctx, g.LinkID)
		switch {
		case errors.Is(err, subscriptions.ErrLinkNotFound) || err == nil && l.State == "deleted":
			out.LinkGone = true
			out.LinkChanged = fileURL != ""
		case err != nil:
			return Generation{}, err
		default:
			out.Link = &l
			out.LinkChanged = fileURL != "" && fileURL != l.URL
		}
	}
	if g.RouterID != 0 && s.d.Routers != nil {
		r, err := s.d.Routers.Router(ctx, g.RouterID)
		switch {
		case errors.Is(err, routing.ErrRouterNotFound):
			out.RouterGone = true
		case err != nil:
			return Generation{}, err
		default:
			out.Router = &r
		}
	}
	if g.KeyFingerprint != "" {
		if _, fp, err := s.d.SSH.PublicKey(ctx); err == nil && fp != g.KeyFingerprint {
			out.KeyChanged = true
		}
	}
	return out, nil
}

// routerState is a generation's router's state now: removed once gone, ""
// without one (or without routing).
func (s *Service) routerState(ctx context.Context, g store.Generation) (string, error) {
	if g.RouterID == 0 || s.d.Routers == nil {
		return "", nil
	}
	r, err := s.d.Routers.Router(ctx, g.RouterID)
	if errors.Is(err, routing.ErrRouterNotFound) {
		return "removed", nil
	}
	return r.State, err
}

func (s *Service) rows(ctx context.Context, gens []store.Generation) ([]Row, error) {
	names := map[int64]string{}
	out := make([]Row, 0, len(gens))
	for _, g := range gens {
		st, err := s.routerState(ctx, g)
		if err != nil {
			return nil, err
		}
		if _, ok := names[g.ScriptID]; !ok {
			if sc, err := s.d.Scripts.Get(ctx, g.ScriptID); err == nil {
				names[g.ScriptID] = sc.Name
			}
		}
		out = append(out, Row{ID: g.ID, ScriptID: g.ScriptID, Script: names[g.ScriptID], RouterName: g.RouterName, Version: g.Version,
			CreatedAt: g.CreatedAt.Time, RouterState: st})
	}
	return out, nil
}

// OfScript lists a script's generations, newest first, with their routers'
// states now.
func (s *Service) OfScript(ctx context.Context, scriptID int64) ([]Row, error) {
	gens, err := store.ScriptGenerations(ctx, s.d.DB.R, scriptID)
	if err != nil {
		return nil, err
	}
	return s.rows(ctx, gens)
}

// AwaitingCount counts a script's generations whose router awaits setup.
func (s *Service) AwaitingCount(ctx context.Context, scriptID int64) (int, error) {
	gens, err := store.ScriptGenerations(ctx, s.d.DB.R, scriptID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, g := range gens {
		st, err := s.routerState(ctx, g)
		if err != nil {
			return 0, err
		}
		if st == "awaiting" {
			n++
		}
	}
	return n, nil
}

// Search finds generations by router name (any case), newest first.
func (s *Service) Search(ctx context.Context, q string, limit int) ([]Row, error) {
	all, err := store.AllGenerations(ctx, s.d.DB.R)
	if err != nil {
		return nil, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	var keep []store.Generation
	for _, g := range all {
		if strings.Contains(strings.ToLower(g.RouterName), q) {
			keep = append(keep, g)
			if len(keep) == limit {
				break
			}
		}
	}
	return s.rows(ctx, keep)
}

// file is the generation's version body and filled file.
func (s *Service) file(ctx context.Context, id int64) (body string, file []byte, parsed params.Script, g store.Generation, err error) {
	g, err = s.row(ctx, id)
	if err != nil {
		return
	}
	ver, err := s.d.Scripts.Version(ctx, g.ScriptID, g.Version)
	if err != nil {
		return
	}
	plain, secrets, err := s.values(g)
	if err != nil {
		return
	}
	for k, v := range secrets {
		plain[k] = v
	}
	file, err = params.Fill([]byte(ver.Body), plain)
	return ver.Body, file, params.Parse([]byte(ver.Body)), g, err
}

// Body is the generated file: the version's body with every value filled in.
// It holds secrets: never log it.
func (s *Service) Body(ctx context.Context, id int64) ([]byte, error) {
	_, file, _, _, err := s.file(ctx, id)
	return file, err
}

// Changes is the unified diff of the version's body against the file, every
// secret parameter's literal shown as •••.
func (s *Service) Changes(ctx context.Context, id int64) (string, error) {
	body, file, parsed, g, err := s.file(ctx, id)
	if err != nil {
		return "", err
	}
	if body == string(file) {
		return "", nil
	}
	sc, err := s.d.Scripts.Get(ctx, g.ScriptID)
	if err != nil {
		return "", err
	}
	unified := udiff.Unified(fmt.Sprintf("%s-v%d.rsc", sc.Slug, g.Version), g.FileName, body, string(file))
	return maskSecrets(unified, parsed), nil
}

// maskSecrets replaces the literal of every secret parameter's line in a
// unified diff, on both sides and in the context lines.
func maskSecrets(unified string, parsed params.Script) string {
	var names []string
	for _, p := range parsed.Params {
		if secret(p) {
			names = append(names, regexp.QuoteMeta(p.Name))
		}
	}
	if len(names) == 0 {
		return unified
	}
	re := regexp.MustCompile(`(?m)^([ +-][ \t]*:local[ \t]+(?:` + strings.Join(names, "|") + `)[ \t]+)[^\r\n]*`)
	return re.ReplaceAllString(unified, `${1}"•••"`)
}
