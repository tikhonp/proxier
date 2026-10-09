package generations

import (
	"context"
	"errors"
	"fmt"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/modules/subscriptions"
	"github.com/tikhonp/proxier/internal/platform/tailnet"
)

// Link choices.
const (
	LinkCreate   = "create"
	LinkExisting = "existing"
	LinkType     = "type"
)

// Router choices.
const (
	RouterNew      = "new"
	RouterExisting = "existing"
	RouterNone     = "none"
)

// JumpOther is the jump-host select's "Another jump host…".
const JumpOther = "other"

// LinkChoice is the Subscription link section.
type LinkChoice struct {
	Mode           string // "create", "existing", "type"; "" when there is no link section
	Name           string // create; "" → "Router — <router name>"
	SubscriptionID int64
	LinkID         int64 // existing
}

// RouterChoice is the Register for routing section.
type RouterChoice struct {
	Mode     string // "new", "existing", "none"; "" when there is no register section
	RouterID int64  // existing
	ListID   int64  // new; 0: the default list
	Host     string // new; "" → <lanNet>.1 when the version has a lanNet
	Port     int
	User     string
	Jump     string // "", "other", or a known jump host as "host:port"
	JumpHost string // other
	JumpPort int
	JumpUser string
	Tailnet  bool
}

// Form is what the generate page posts.
type Form struct {
	Version    int
	RouterName string
	Values     map[string]string // by parameter name, as typed; "" for an untouched secret
	Link       LinkChoice
	Router     RouterChoice
	From       int64 // Generate again: the generation whose secrets untouched secret fields keep
}

// Step is a line of the summary.
type Step struct {
	Key  string // i18n key of the line
	Args map[string]any
}

// Plan is what Generate would do; the summary shows it.
type Plan struct {
	Steps    []Step
	FileName string
	Changed  []string
	Computed map[string]string
	Host     string // the router's host after the lanNet default
	HostAuto string // "<lanNet>.1" when the version has a usable lanNet
	Errors   store.FieldErrors
	Warnings []Step
}

// View is the generate page: the form, its choices and its plan.
type View struct {
	Script        scripts.Script
	Versions      []int
	Parsed        params.Script
	Form          Form
	Locked        map[string]string // parameter → source ("router", "key", "link")
	LockedValues  map[string]string // parameter → its locked value as shown (a link's URL masked)
	Kept          map[string]bool   // secret parameters an earlier generation's value is kept for
	Subscriptions []subscriptions.IssuerSubscription
	Links         []subscriptions.IssuedLink
	Lists         []routing.RegistrarList
	Routers       []routing.RegisteredRouter
	JumpHosts     []routing.JumpHost
	TailnetUp     bool
	HasLinkParam  bool // the version has a @fill subscription-link parameter
	Plan          Plan

	// what Generate uses of the check
	values  map[string]string
	link    *subscriptions.IssuedLink  // existing
	router  *routing.RegisteredRouter  // existing
	reg     routing.RouterRegistration // new
	linkSub *subscriptions.IssuerSubscription
	linkNm  string
	keyLine string
	keyFP   string
}

// DefaultLinkName is a created link's name when none is typed.
func DefaultLinkName(router string) string { return "Router — " + router }

// script reads the script and the version to generate from (0: the
// current one); ErrCantGenerate for an archived or versionless script.
func (s *Service) script(ctx context.Context, scriptID int64, version int) (scripts.Script, scripts.Version, error) {
	sc, err := s.d.Scripts.Get(ctx, scriptID)
	if err != nil {
		return sc, scripts.Version{}, err
	}
	if sc.Archived {
		return sc, scripts.Version{}, fmt.Errorf("%w: %w", ErrCantGenerate, scripts.ErrArchived)
	}
	if sc.Current == 0 {
		return sc, scripts.Version{}, ErrCantGenerate
	}
	ver, err := s.d.Scripts.Version(ctx, scriptID, version)
	return sc, ver, err
}

// Form is the generate page's first view: the version's defaults, or, with
// from, that generation's router name and values with its link and router
// chosen while they still exist.
func (s *Service) Form(ctx context.Context, scriptID int64, version int, from int64) (View, error) {
	var prev *store.Generation
	if from != 0 {
		g, err := s.row(ctx, from)
		if err != nil {
			return View{}, err
		}
		if g.ScriptID != scriptID {
			return View{}, ErrNotFound
		}
		prev, version = &g, g.Version
	}
	sc, ver, err := s.script(ctx, scriptID, version)
	if err != nil {
		return View{}, err
	}
	parsed := params.Parse([]byte(ver.Body))
	f := Form{Version: ver.Number, Values: map[string]string{}, From: from}
	for _, p := range parsed.Params {
		if !secret(p) {
			f.Values[p.Name] = p.Default
		}
	}
	f.Link.Mode = LinkCreate
	f.Router = RouterChoice{Mode: RouterNew, Port: 22, User: "proxier", Tailnet: s.tailnetUp(ctx)}
	if prev != nil {
		plain, _, err := s.values(*prev)
		if err != nil {
			return View{}, err
		}
		f.RouterName = prev.RouterName
		for k, v := range plain {
			if _, ok := f.Values[k]; ok {
				f.Values[k] = v
			}
		}
		if prev.LinkID != 0 && s.d.Links != nil {
			if l, err := s.d.Links.Link(ctx, prev.LinkID); err == nil && l.State != "deleted" {
				f.Link = LinkChoice{Mode: LinkExisting, LinkID: l.ID}
			} else if err != nil && !errors.Is(err, subscriptions.ErrLinkNotFound) {
				return View{}, err
			}
		} else if prev.LinkID == 0 {
			f.Link.Mode = LinkType
		}
		switch {
		case prev.RouterID == 0:
			f.Router.Mode = RouterNone
		case s.d.Routers != nil:
			if _, err := s.d.Routers.Router(ctx, prev.RouterID); err == nil {
				f.Router = RouterChoice{Mode: RouterExisting, RouterID: prev.RouterID}
			} else if !errors.Is(err, routing.ErrRouterNotFound) {
				return View{}, err
			}
		}
	}
	return s.view(ctx, sc, ver, parsed, f, prev)
}

// Check builds the view of a posted form and its plan, with every problem
// found; it writes nothing.
func (s *Service) Check(ctx context.Context, scriptID int64, f Form) (View, error) {
	sc, ver, err := s.script(ctx, scriptID, f.Version)
	if err != nil {
		return View{}, err
	}
	var prev *store.Generation
	if f.From != 0 {
		g, err := s.row(ctx, f.From)
		switch {
		case err == nil && g.ScriptID == scriptID:
			prev = &g
		case err != nil && !errors.Is(err, ErrNotFound):
			return View{}, err
		default:
			f.From = 0
		}
	}
	return s.view(ctx, sc, ver, params.Parse([]byte(ver.Body)), f, prev)
}

func (s *Service) tailnetUp(ctx context.Context) bool {
	return s.d.Tailnet != nil && s.d.Tailnet.Status(ctx).State == tailnet.Running
}

// view reads the ports' choices and makes the plan.
func (s *Service) view(ctx context.Context, sc scripts.Script, ver scripts.Version, parsed params.Script, f Form, prev *store.Generation) (View, error) {
	v := View{Script: sc, Parsed: parsed, Form: f, TailnetUp: s.tailnetUp(ctx)}
	if v.Form.Values == nil {
		v.Form.Values = map[string]string{}
	}
	vs, err := s.d.Scripts.Versions(ctx, sc.ID)
	if err != nil {
		return View{}, err
	}
	for _, x := range vs {
		v.Versions = append(v.Versions, x.Number)
	}
	for _, p := range parsed.Params {
		if p.Annotations.Fill == params.FillLink {
			v.HasLinkParam = true
		}
	}
	if s.d.Links != nil && v.HasLinkParam {
		if v.Subscriptions, err = s.d.Links.Subscriptions(ctx); err != nil {
			return View{}, err
		}
		if v.Links, err = s.d.Links.Links(ctx); err != nil {
			return View{}, err
		}
		switch v.Form.Link.Mode {
		case LinkCreate, LinkExisting, LinkType:
		default:
			v.Form.Link.Mode = LinkCreate
		}
	} else {
		v.Form.Link = LinkChoice{}
	}
	if s.d.Routers != nil {
		if v.Lists, err = s.d.Routers.Lists(ctx); err != nil {
			return View{}, err
		}
		if v.Routers, err = s.d.Routers.Routers(ctx); err != nil {
			return View{}, err
		}
		if v.JumpHosts, err = s.d.Routers.JumpHosts(ctx); err != nil {
			return View{}, err
		}
		switch v.Form.Router.Mode {
		case RouterNew, RouterExisting, RouterNone:
		default:
			v.Form.Router.Mode = RouterNew
		}
	} else {
		v.Form.Router = RouterChoice{}
	}
	keep := map[string]string{}
	if prev != nil {
		if _, keep, err = s.values(*prev); err != nil {
			return View{}, err
		}
	}
	if v.keyLine, v.keyFP, err = s.d.SSH.PublicKey(ctx); err != nil {
		return View{}, err
	}
	if err := s.plan(ctx, &v, ver.Number, keep); err != nil {
		return View{}, err
	}
	return v, nil
}
