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

type newView struct {
	IP, Port, Notes string
	Errs            map[string]string // translated, by form field
	Country         string            // the picker's text: a country's label, or what was typed
	Auto            string            // the country the IP suggested last, "" for none
	Hint            string
	Countries       []string // the picker's list: every country's label
	TemplateID      int64
	Version         int
	Templates       []tplOption
	Versions        []verOption
	Params          []paramField
	ParamsFor       string // "<template>:<version>" the fields were made for
	Sum             provision.Summary
	SumErrors       []string
	CreateLabel     string
	NoTemplates     bool
}

// formOf reads the new-server form. The country is the code the picker's
// text names, or that text itself when it names none, so Create refuses it.
func formOf(c *echo.Context) provision.Form {
	atoi := func(k string) int64 { n, _ := strconv.ParseInt(strings.TrimSpace(c.FormValue(k)), 10, 64); return n }
	f := provision.Form{
		IP: c.FormValue("ip"), RootPassword: c.FormValue("root_password"), SSHPort: int(atoi("ssh_port")),
		Country: countryOf(c.FormValue("country")), TemplateID: atoi("template"), Version: int(atoi("version")),
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

// countryOf is the code the picker's text names, or the text when it names
// none.
func countryOf(text string) string {
	if cc := country.Parse(text); cc != "" {
		return cc
	}
	return strings.TrimSpace(text)
}

// newViewOf builds the page for a form: the options, the parameter fields of
// the chosen version, the summary. A zero form is the first visit. A country
// is shown by its label in the admin's language, anything else as typed.
func (h *handler) newViewOf(ctx context.Context, f provision.Form, auto string) (newView, error) {
	lang := string(i18n.From(ctx).Lang)
	v := newView{IP: f.IP, Notes: f.Notes, Country: f.Country, Auto: auto, Version: f.Version, TemplateID: f.TemplateID}
	if country.Valid(f.Country) {
		v.Country = country.Label(f.Country, lang)
	}
	if f.SSHPort != 0 {
		v.Port = strconv.Itoa(f.SSHPort)
	}
	for _, e := range country.List(lang) {
		v.Countries = append(v.Countries, country.Label(e.Code, lang))
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
	v, err := h.newViewOf(ctx, f, "")
	if err != nil {
		return err
	}
	if !v.NoTemplates {
		v.Sum = h.Provision.Summarize(ctx, h.formOfView(v), false)
		v.SumErrors = sumErrors(ctx, v.Sum)
	}
	return h.renderNewServer(c, http.StatusOK, v)
}

// formOfView is the form a view stands for, for the summary of a first visit.
func (h *handler) formOfView(v newView) provision.Form {
	port, _ := strconv.Atoi(v.Port)
	return provision.Form{IP: v.IP, SSHPort: port, Country: countryOf(v.Country), TemplateID: v.TemplateID, Version: v.Version, Params: map[string]string{}}
}

func (h *handler) renderNewServer(c *echo.Context, status int, v newView) error {
	ctx := c.Request().Context()
	v.CreateLabel = i18n.T(ctx, "servers.new.create")
	if v.Sum.Name != "" {
		v.CreateLabel = i18n.T(ctx, "servers.new.create_named", i18n.Args{"name": v.Sum.Name})
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
		// a country being typed names none until it is picked from the list
		if strings.HasSuffix(m.Key, "_required") || m.Key == "servers.err.no_version" || m.Key == "servers.err.country_unknown" {
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
// out of band, the country (when the IP suggests another; otherwise only the
// line under it) and the parameter fields (when the template or version
// changed). The root password is never
// posted here (hx-params on the form); it would be ignored if it were.
//
// The IP's country fills the picker only while the admin hasn't chosen in it:
// it is empty with nothing suggested yet, or shows what was suggested last.
// Anything else, a half-typed name or an emptied field included, is theirs,
// and the summary never replaces the field they may be typing in.
func (h *handler) newSummary(c *echo.Context) error {
	ctx := c.Request().Context()
	f := formOf(c)
	f.RootPassword = ""
	text := strings.TrimSpace(c.FormValue("country"))
	auto := country.Parse(c.FormValue("country_auto"))
	untouched := text == "" && auto == "" || text != "" && auto != "" && f.Country == auto

	sum := h.Provision.Summarize(ctx, f, untouched)
	hint, swap := "", false
	if untouched && sum.Suggested != "" && sum.Suggested != auto {
		f.Country, auto, swap = sum.Suggested, sum.Suggested, true
		sum = h.Provision.Summarize(ctx, f, false)
	}
	if untouched && auto != "" {
		hint = i18n.T(ctx, "servers.new.suggested", i18n.Args{"country": auto})
	}
	v, err := h.newViewOf(ctx, f, auto)
	if err != nil {
		return err
	}
	if !country.Valid(f.Country) {
		v.Country = c.FormValue("country") // as typed, spaces too
	}
	v.Hint, v.Sum, v.SumErrors = hint, sum, sumErrors(ctx, sum)
	v.CreateLabel = i18n.T(ctx, "servers.new.create")
	if sum.Name != "" {
		v.CreateLabel = i18n.T(ctx, "servers.new.create_named", i18n.Args{"name": sum.Name})
	}
	// Only what changed is sent back out of band, so the field being typed in
	// keeps its focus.
	changedParams := c.FormValue("params_for") != v.ParamsFor
	return web.Render(c, http.StatusOK, summaryFragment(v, changedParams, swap))
}

func (h *handler) createServer(c *echo.Context) error {
	ctx := c.Request().Context()
	f := formOf(c)
	id, err := h.Provision.Create(ctx, f, web.Actor(ctx))
	var fe provision.FieldErrors
	if asFieldErrors(err, &fe) {
		v, verr := h.newViewOf(ctx, f, country.Parse(c.FormValue("country_auto")))
		if verr != nil {
			return verr
		}
		v.Errs = translate(c, fe)
		v.Sum = h.Provision.Summarize(ctx, f, false)
		v.SumErrors = sumErrors(ctx, v.Sum)
		return h.renderNewServer(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, serverHref(id))
}

func asFieldErrors(err error, target *provision.FieldErrors) bool { return errors.As(err, target) }
