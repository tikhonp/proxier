package pages

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

type tplOption struct {
	ID       int64
	Name     string
	Selected bool
}

type verOption struct {
	Number   int
	Label    string
	Selected bool
}

type locOption struct {
	ID       int64
	Label    string
	Selected bool
}

// paramField is one parameter of the chosen template version as a form field.
type paramField struct {
	Name, Label, Type, Help string
	Required, Secret        bool
	Options                 []string
	Value, Err              string
}

func (p paramField) inputType() string {
	switch {
	case p.Secret:
		return "password"
	case p.Type == "email":
		return "email"
	}
	return "text"
}

func (p paramField) inputMode() string {
	if p.Type == "int" {
		return "numeric"
	}
	return "text"
}

type newLocView struct {
	Open                bool
	Code, Name, Country string
	Errs                map[string]string
	Countries           []country.Entry
}

type newView struct {
	IP, Port, Notes string
	Errs            map[string]string // translated, by form field
	LocationID      int64
	Auto            int64 // the location the IP's country suggested last
	Hint            string
	Locations       []locOption
	TemplateID      int64
	Version         int
	Templates       []tplOption
	Versions        []verOption
	Params          []paramField
	ParamsFor       string // "<template>:<version>" the fields were made for
	Sum             provision.Summary
	SumErrors       []string
	CreateLabel     string
	NewLoc          newLocView
	NoTemplates     bool
}

// formOf reads the new-server form.
func formOf(c *echo.Context) provision.Form {
	atoi := func(k string) int64 { n, _ := strconv.ParseInt(strings.TrimSpace(c.FormValue(k)), 10, 64); return n }
	f := provision.Form{
		IP: c.FormValue("ip"), RootPassword: c.FormValue("root_password"), SSHPort: int(atoi("ssh_port")),
		LocationID: atoi("location"), TemplateID: atoi("template"), Version: int(atoi("version")),
		Notes: c.FormValue("notes"), Params: map[string]string{},
	}
	if err := c.Request().ParseForm(); err == nil {
		for k, vs := range c.Request().PostForm {
			if p, ok := strings.CutPrefix(k, "param."); ok && len(vs) > 0 {
				f.Params[p] = vs[0]
			}
		}
	}
	return f
}

// newViewOf builds the page for a form: the options, the parameter fields of
// the chosen version, the summary. A zero form is the first visit.
func (h *handler) newViewOf(ctx context.Context, f provision.Form, auto int64) (newView, error) {
	v := newView{IP: f.IP, Notes: f.Notes, LocationID: f.LocationID, Auto: auto, Version: f.Version, TemplateID: f.TemplateID}
	if f.SSHPort != 0 {
		v.Port = strconv.Itoa(f.SSHPort)
	}
	locs, err := h.Store.Locations(ctx)
	if err != nil {
		return v, err
	}
	if v.LocationID == 0 && len(locs) == 1 {
		v.LocationID, v.Auto = locs[0].ID, locs[0].ID // one place to choose from; still only a suggestion
	}
	for _, l := range locs {
		v.Locations = append(v.Locations, locOption{ID: l.ID, Label: country.Flag(l.Country) + " " + l.Code + " — " + l.Name, Selected: l.ID == v.LocationID})
	}

	tpls, err := h.Templates.List(ctx, false)
	if err != nil {
		return v, err
	}
	for _, t := range tpls {
		if t.DefaultVersion == 0 {
			continue // nothing published yet
		}
		if v.TemplateID == 0 {
			v.TemplateID = t.ID
		}
		v.Templates = append(v.Templates, tplOption{ID: t.ID, Name: t.Name, Selected: t.ID == v.TemplateID})
	}
	if len(v.Templates) == 0 {
		v.NoTemplates = true
		return v, nil
	}
	vers, err := h.Templates.Versions(ctx, v.TemplateID)
	if err != nil {
		return v, err
	}
	known := false
	for _, ver := range vers {
		if ver.Number == v.Version {
			known = true
		}
	}
	for _, ver := range vers {
		if !known && ver.Default {
			v.Version = ver.Number
		}
	}
	for _, ver := range vers {
		label := vlabel(ver.Number)
		if ver.Default {
			label += " · " + i18n.T(ctx, "servers.new.default")
		}
		v.Versions = append(v.Versions, verOption{Number: ver.Number, Label: label, Selected: ver.Number == v.Version})
	}
	v.ParamsFor = strconv.FormatInt(v.TemplateID, 10) + ":" + strconv.Itoa(v.Version)
	if ver, err := h.Templates.Version(ctx, v.TemplateID, v.Version); err == nil {
		if m, fs := manifest.Parse(ver.Files[manifest.Name]); m != nil && (finding.Report{Findings: fs}).OK() {
			for _, p := range m.Parameters {
				pf := paramField{Name: "param." + p.Key, Label: p.Label, Type: p.Type, Help: p.Help, Required: p.Required, Secret: p.Secret, Options: p.Options, Value: f.Params[p.Key]}
				if pf.Value == "" && p.Default != nil && len(f.Params) == 0 {
					pf.Value = *p.Default
				}
				if p.Secret {
					pf.Value = "" // a secret is never sent back to the page
				}
				v.Params = append(v.Params, pf)
			}
		}
	}
	return v, nil
}

func (h *handler) newServer(c *echo.Context) error {
	ctx := c.Request().Context()
	f := provision.Form{Params: map[string]string{}}
	if t, err := strconv.ParseInt(c.QueryParam("template"), 10, 64); err == nil {
		f.TemplateID = t
	}
	v, err := h.newViewOf(ctx, f, 0)
	if err != nil {
		return err
	}
	if !v.NoTemplates {
		v.Sum = h.Provision.Summarize(ctx, h.formOfView(v), true)
		v.SumErrors = sumErrors(ctx, v.Sum)
	}
	return h.renderNewServer(c, http.StatusOK, v)
}

// formOfView is the form a view stands for, for the summary of a first visit.
func (h *handler) formOfView(v newView) provision.Form {
	port, _ := strconv.Atoi(v.Port)
	return provision.Form{IP: v.IP, SSHPort: port, LocationID: v.LocationID, TemplateID: v.TemplateID, Version: v.Version, Params: map[string]string{}}
}

func (h *handler) renderNewServer(c *echo.Context, status int, v newView) error {
	ctx := c.Request().Context()
	v.CreateLabel = i18n.T(ctx, "servers.new.create")
	if v.Sum.Name != "" {
		v.CreateLabel = i18n.T(ctx, "servers.new.create_named", i18n.Args{"name": v.Sum.Name})
	}
	if v.NewLoc.Countries == nil {
		v.NewLoc.Countries = country.List(string(i18n.From(ctx).Lang))
	}
	return web.Render(c, status, newServerPage(h.shell(c, i18n.T(ctx, "servers.new.title"), "/servers"), v))
}

// sumErrors are the summary's problems as lines; "required" ones wait until
// the admin has typed something.
func sumErrors(ctx context.Context, s provision.Summary) []string {
	var keys []string
	for k := range s.Errors {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var out []string
	for _, k := range keys {
		m := s.Errors[k]
		if strings.HasSuffix(m.Key, "_required") || m.Key == "servers.err.no_version" {
			continue
		}
		out = append(out, i18n.T(ctx, m.Key, m.Args))
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// newSummary answers the form while it is typed in: the summary panel, and,
// out of band, the location (when the IP suggests one) and the parameter fields
// (when the template or version changed). The root password is never posted
// here (hx-params on the form); it would be ignored if it were.
func (h *handler) newSummary(c *echo.Context) error {
	ctx := c.Request().Context()
	f := formOf(c)
	f.RootPassword = ""
	auto, _ := strconv.ParseInt(c.FormValue("location_auto"), 10, 64)
	touched := f.LocationID != 0 && f.LocationID != auto

	sum := h.Provision.Summarize(ctx, f, touched)
	hint := ""
	if !touched && sum.SuggestedLocation != 0 {
		f.LocationID, auto = sum.SuggestedLocation, sum.SuggestedLocation
		hint = i18n.T(ctx, "servers.new.suggested", i18n.Args{"country": sum.SuggestedCountry})
		sum = h.Provision.Summarize(ctx, f, true)
		sum.SuggestedLocation, sum.SuggestedCountry = f.LocationID, ""
	}
	v, err := h.newViewOf(ctx, f, auto)
	if err != nil {
		return err
	}
	v.Hint, v.Sum, v.SumErrors = hint, sum, sumErrors(ctx, sum)
	v.CreateLabel = i18n.T(ctx, "servers.new.create")
	if sum.Name != "" {
		v.CreateLabel = i18n.T(ctx, "servers.new.create_named", i18n.Args{"name": sum.Name})
	}
	// Only what changed is sent back out of band, so the field being typed in
	// keeps its focus.
	changedParams := c.FormValue("params_for") != v.ParamsFor
	changedLocation := !touched
	return web.Render(c, http.StatusOK, summaryFragment(v, changedParams, changedLocation))
}

// newLocation adds a location from the form and answers with the location
// field, the new one selected, and the summary for it. A refusal or a failure
// comes back inside the field with the mini form open and what was typed in
// it, as 200: htmx swaps no 4xx or 5xx, so anything else would leave the click
// without an answer.
func (h *handler) newLocation(c *echo.Context) error {
	ctx := c.Request().Context()
	f := formOf(c)
	auto, _ := strconv.ParseInt(c.FormValue("location_auto"), 10, 64)
	nl := newLocView{Code: strings.ToLower(strings.TrimSpace(c.FormValue("nl_code"))), Name: c.FormValue("nl_name"), Country: c.FormValue("nl_country")}
	id, err := h.Store.CreateLocation(ctx, nl.Code, nl.Name, nl.Country, "admin")
	if err != nil {
		errs, ok := fieldErrors(c, err)
		if !ok {
			h.Log.Error("pages: new location", "error", err)
			errs = map[string]string{"failed": i18n.T(ctx, "servers.new.location.failed")}
		}
		v, verr := h.newViewOf(ctx, f, auto)
		if verr != nil {
			return verr
		}
		nl.Open, nl.Errs, nl.Countries = true, errs, country.List(string(i18n.From(ctx).Lang))
		v.NewLoc = nl
		return web.Render(c, http.StatusOK, locationField(v))
	}
	// The chosen place is the admin's, not a suggestion (Auto stays 0).
	f.LocationID = id
	v, err := h.newViewOf(ctx, f, 0)
	if err != nil {
		return err
	}
	v.NewLoc.Countries = country.List(string(i18n.From(ctx).Lang))
	v.Sum = h.Provision.Summarize(ctx, f, true)
	v.SumErrors = sumErrors(ctx, v.Sum)
	return web.Render(c, http.StatusOK, newLocationAnswer(v))
}

func (h *handler) createServer(c *echo.Context) error {
	ctx := c.Request().Context()
	f := formOf(c)
	id, err := h.Provision.Create(ctx, f, "admin")
	var fe provision.FieldErrors
	if asFieldErrors(err, &fe) {
		auto, _ := strconv.ParseInt(c.FormValue("location_auto"), 10, 64)
		v, verr := h.newViewOf(ctx, f, auto)
		if verr != nil {
			return verr
		}
		v.Errs = translate(c, fe)
		v.Sum = h.Provision.Summarize(ctx, f, true)
		v.SumErrors = sumErrors(ctx, v.Sum)
		return h.renderNewServer(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, serverHref(id))
}

func asFieldErrors(err error, target *provision.FieldErrors) bool { return errors.As(err, target) }

var _ = store.ErrNotFound
