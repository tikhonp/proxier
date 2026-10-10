package pages

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/generations"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// GenerateHref is a script's generate form.
func GenerateHref(id int64) string { return ScriptHref(id) + "/generate" }

// GenerationHref is a generation's page.
func GenerationHref(id int64) string { return ListPath + "/generations/" + i64(id) }

// genField is a parameter or computed value of the form.
type genField struct {
	Name, Value, Desc   string
	Secret, Required    bool
	Choices             []string
	Pattern             string
	Placeholder         string
	Lock                string // where a locked value comes from ("" typed)
	Lockable            bool   // a @fill link or router parameter: its lock follows the choices
	LockNote, LockValue string
	Changed             bool
	Err                 string
	Computed            bool
	Expr, CompValue     string
}

type genGroup struct {
	Heading string
	Items   []genField
}

type option struct {
	Value, Label string
	Selected     bool
}

// genView is the generate page and its summary.
type genView struct {
	V          generations.View
	Action     string // the form's URL
	Versions   []option
	Groups     []genGroup
	Errs       map[string]string
	Problems   []string
	Steps      []string
	Warnings   []string
	Subs       []option
	Links      []option
	Lists      []option
	Routers    []option
	Jumps      []option
	HostPh     string
	RouterLock string // the existing router's name when its name is locked
	GoLabel    string
	ParamCount int
	FillsNote  bool // the version has @fill routing-* parameters
	// SecretNote: what makes the file secret, if anything (generations.sum.*)
	SecretNote string
}

// genForm reads the posted form.
func genForm(c *echo.Context) generations.Form {
	atoi := func(k string) int { n, _ := strconv.Atoi(strings.TrimSpace(c.FormValue(k))); return n }
	id := func(k string) int64 { n, _ := strconv.ParseInt(strings.TrimSpace(c.FormValue(k)), 10, 64); return n }
	f := generations.Form{
		Version:    atoi("version"),
		RouterName: c.FormValue("router_name"),
		Values:     map[string]string{},
		From:       id("from"),
		Link: generations.LinkChoice{
			Mode: c.FormValue("link_mode"), Name: c.FormValue("link_name"), SubscriptionID: id("link_sub"), LinkID: id("link_id"),
		},
		Router: generations.RouterChoice{
			Mode: c.FormValue("router_mode"), RouterID: id("router_id"), ListID: id("router_list"), Host: c.FormValue("router_host"),
			Port: atoi("router_port"), User: c.FormValue("router_user"), Jump: c.FormValue("router_jump"),
			JumpHost: c.FormValue("jump_host"), JumpPort: atoi("jump_port"), JumpUser: c.FormValue("jump_user"),
			Tailnet: c.FormValue("router_tailnet") == "1",
		},
	}
	if form, err := c.FormValues(); err == nil {
		for k, vs := range form {
			if name, ok := strings.CutPrefix(k, "p."); ok && len(vs) > 0 {
				f.Values[name] = vs[0]
			}
		}
	}
	return f
}

// cantGenerate answers a script that can't generate (archived, no version)
// with 409 and the reason.
func (h *handler) cantGenerate(c *echo.Context, s scripts.Script, err error) error {
	ctx := c.Request().Context()
	var reason string
	switch {
	case errors.Is(err, scripts.ErrArchived):
		reason = i18n.T(ctx, "generations.cant.archived")
	case errors.Is(err, generations.ErrCantGenerate):
		reason = i18n.T(ctx, "generations.cant.no_version")
	case errors.Is(err, generations.ErrNotFound):
		return echo.ErrNotFound
	default:
		return notFoundOr(err)
	}
	title := i18n.T(ctx, "generations.form.title")
	return web.Render(c, http.StatusConflict, cantPage(h.shell(c, title, ListPath), s, title, reason))
}

func (h *handler) generatePage(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	version, _ := strconv.Atoi(c.QueryParam("version"))
	from, _ := strconv.ParseInt(c.QueryParam("from"), 10, 64)
	v, err := h.Generations.Form(ctx, s.ID, version, from)
	if err != nil {
		return h.cantGenerate(c, s, err)
	}
	return h.renderGenerate(c, http.StatusOK, v, false)
}

func (h *handler) generateSummary(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	f := genForm(c)
	v, err := h.Generations.Check(ctx, s.ID, f)
	if err != nil {
		return h.cantGenerate(c, s, err)
	}
	gv := h.genView(ctx, v, true)
	// the fields whose lock changed, and the host while its placeholder is
	// all it shows, swap themselves out of band
	var swap []genField
	for _, g := range gv.Groups {
		for _, it := range g.Items {
			if it.Lockable && (it.Lock != "" || c.FormValue("lock."+it.Name) != "") {
				swap = append(swap, it)
			}
		}
	}
	nameSwap := gv.RouterLock != "" || c.FormValue("lock.router_name") != ""
	hostSwap := strings.TrimSpace(c.FormValue("router_host")) == "" && v.Form.Router.Mode != ""
	return web.Render(c, http.StatusOK, summaryAnswer(gv, swap, nameSwap, hostSwap))
}

func (h *handler) generate(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	f := genForm(c)
	id, err := h.Generations.Generate(ctx, s.ID, f, events.ActorAdmin)
	var fe store.FieldErrors
	switch {
	case errors.As(err, &fe):
		v, cerr := h.Generations.Check(ctx, s.ID, f)
		if cerr != nil {
			return h.cantGenerate(c, s, cerr)
		}
		// the ports' refusals are only known to Generate
		v.Plan.Errors = fe
		return h.renderGenerate(c, http.StatusUnprocessableEntity, v, true)
	case err != nil:
		return h.cantGenerate(c, s, err)
	}
	return web.Redirect(c, GenerationHref(id))
}

func (h *handler) renderGenerate(c *echo.Context, code int, v generations.View, showErrs bool) error {
	ctx := c.Request().Context()
	gv := h.genView(ctx, v, showErrs)
	title := i18n.T(ctx, "generations.form.title")
	return web.Render(c, code, generatePage(h.shell(c, title, ListPath), gv, title))
}

// fieldLabel names a form field in the summary's problem list.
func fieldLabel(ctx context.Context, key string) string {
	if name, ok := strings.CutPrefix(key, "param."); ok {
		return name
	}
	return i18n.T(ctx, "generations.field."+strings.ReplaceAll(key, ".", "_"))
}

// genView turns the service's view into what the templates show. Errors
// show on the fields only after a post (showErrs); the summary always lists
// them.
func (h *handler) genView(ctx context.Context, v generations.View, showErrs bool) genView {
	loc := i18n.From(ctx)
	f := v.Form
	gv := genView{V: v, Action: GenerateHref(v.Script.ID), Errs: map[string]string{}}
	for _, n := range v.Versions {
		label := "v" + itoa(n)
		if n == v.Script.Current {
			label += " · " + i18n.T(ctx, "scripts.version.current")
		}
		gv.Versions = append(gv.Versions, option{Value: itoa(n), Label: label, Selected: n == f.Version})
	}
	keys := make([]string, 0, len(v.Plan.Errors))
	for k := range v.Plan.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		msg := i18n.T(ctx, v.Plan.Errors[k])
		if showErrs {
			gv.Errs[k] = msg
		}
		gv.Problems = append(gv.Problems, fieldLabel(ctx, k)+": "+msg)
	}
	for _, st := range v.Plan.Steps {
		gv.Steps = append(gv.Steps, stepText(ctx, st))
	}
	for _, w := range v.Plan.Warnings {
		gv.Warnings = append(gv.Warnings, stepText(ctx, w))
	}

	routerName := strings.TrimSpace(f.RouterName)
	if f.Router.Mode == generations.RouterExisting && v.Plan.Errors["router.id"] == "" {
		gv.RouterLock = routerName
	}
	gv.GoLabel = i18n.T(ctx, "generations.form.go")
	if routerName != "" {
		gv.GoLabel = i18n.T(ctx, "generations.form.go_for", i18n.Args{"name": routerName})
	}
	changed := map[string]bool{}
	for _, n := range v.Plan.Changed {
		changed[n] = true
	}
	for _, g := range v.Parsed.Groups {
		gg := genGroup{Heading: g.Heading}
		for i, it := range g.Items {
			desc := it.Description()
			if i == 0 && g.Heading != "" {
				desc = ""
			}
			if c := it.Computed; c != nil {
				gg.Items = append(gg.Items, genField{Name: c.Name, Desc: desc, Computed: true, Expr: c.Expr, CompValue: v.Plan.Computed[c.Name]})
				continue
			}
			p := it.Param
			gv.ParamCount++
			a := p.Annotations
			fd := genField{
				Name: p.Name, Desc: desc, Secret: a.Secret || a.Fill == params.FillLink, Required: a.Required, Choices: a.Choices,
				Pattern: a.Pattern, Lock: v.Locked[p.Name], Changed: changed[p.Name], Err: gv.Errs["param."+p.Name],
			}
			switch {
			case a.Fill == params.FillLink:
				gv.SecretNote = "generations.sum.secret"
			case a.Secret && gv.SecretNote == "":
				gv.SecretNote = "generations.sum.secret_values"
			}
			switch a.Fill {
			case params.FillLink:
				fd.Lockable = h.Generations.HasLinks() && v.HasLinkParam
			case params.FillList, params.FillForwarder:
				fd.Lockable = h.Generations.HasRouters()
				gv.FillsNote = gv.FillsNote || fd.Lockable
			}
			if !fd.Secret {
				fd.Value = f.Values[p.Name]
				if _, ok := f.Values[p.Name]; !ok {
					fd.Value = p.Default
				}
			}
			switch {
			case fd.Secret && v.Kept[p.Name]:
				fd.Placeholder = i18n.T(ctx, "generations.field.kept", i18n.Args{"id": f.From})
			case fd.Secret:
				fd.Placeholder = i18n.T(ctx, "generations.field.default")
			}
			switch fd.Lock {
			case "key":
				fd.LockNote, fd.LockValue = i18n.T(ctx, "generations.lock.key"), v.LockedValues[p.Name]
			case "link":
				fd.LockNote, fd.LockValue = i18n.T(ctx, "generations.lock.link"), "•••"
			case "router":
				fd.LockValue = v.LockedValues[p.Name]
				fd.LockNote = i18n.T(ctx, "generations.lock.router_new")
				if routerName != "" {
					fd.LockNote = i18n.T(ctx, "generations.lock.router", i18n.Args{"name": routerName})
				}
			}
			gg.Items = append(gg.Items, fd)
		}
		gv.Groups = append(gv.Groups, gg)
	}

	for _, sub := range v.Subscriptions {
		gv.Subs = append(gv.Subs, option{Value: i64(sub.ID), Label: sub.Name + " · " + loc.N("generations.servers", int64(sub.Servers)), Selected: sub.ID == f.Link.SubscriptionID})
	}
	for _, l := range v.Links {
		gv.Links = append(gv.Links, option{Value: i64(l.ID), Label: l.Name + " · " + l.Subscription + " · " + i18n.T(ctx, "generations.link_state."+l.State), Selected: l.ID == f.Link.LinkID})
	}
	for _, l := range v.Lists {
		label := l.Name
		if l.Default {
			label += " · " + i18n.T(ctx, "generations.list_default")
		}
		gv.Lists = append(gv.Lists, option{Value: i64(l.ID), Label: label, Selected: l.ID == f.Router.ListID || f.Router.ListID == 0 && l.Default})
	}
	for _, r := range v.Routers {
		gv.Routers = append(gv.Routers, option{Value: i64(r.ID), Label: r.Name + " · " + i18n.T(ctx, "generations.router_state."+r.State) + " · " + r.List, Selected: r.ID == f.Router.RouterID})
	}
	for _, j := range v.JumpHosts {
		val := net.JoinHostPort(j.Host, strconv.Itoa(j.Port))
		label := i18n.T(ctx, "generations.jump.known", i18n.Args{"address": val, "user": j.User})
		if j.Tailnet {
			label += " · tailnet"
		}
		gv.Jumps = append(gv.Jumps, option{Value: val, Label: label, Selected: f.Router.Jump == val})
	}
	gv.HostPh = "192.168.88.1"
	if v.Plan.HostAuto != "" {
		gv.HostPh = i18n.T(ctx, "generations.host.auto", i18n.Args{"host": v.Plan.HostAuto})
	}
	return gv
}

// stepText is a summary line in the admin's language.
func stepText(ctx context.Context, st generations.Step) string {
	args := i18n.Args{}
	for k, v := range st.Args {
		args[k] = v
	}
	if st.Key == "generations.sum.write" {
		n, _ := st.Args["n"].(int)
		if n == 0 {
			return i18n.T(ctx, "generations.sum.write_same", args)
		}
		return i18n.N(ctx, st.Key, int64(n), args)
	}
	return i18n.T(ctx, st.Key, args)
}

// portText is a port field's value: empty for none.
func portText(n int) string {
	if n == 0 {
		return ""
	}
	return itoa(n)
}

// nameOr is the router name for a placeholder.
func nameOr(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return "…"
	}
	return s
}
