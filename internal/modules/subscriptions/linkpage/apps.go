// Package linkpage is what a browser that opens a link's URL gets: a page
// that gets the person connected, with apps for their device, buttons that
// add the link to an app, a copy button and a QR code
// (docs/integrations/subscription-format.md#browser-page). It is pure: the
// fetch decides when it is served and what it holds.
package linkpage

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"text/template"

	"go.yaml.in/yaml/v3"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// Platform is one tab of the page.
type Platform string

// The platforms, as their tabs and section ids name them.
const (
	IOS     Platform = "ios"
	Android Platform = "android"
	MacOS   Platform = "macos"
	Windows Platform = "windows"
	Linux   Platform = "linux"
	TV      Platform = "tv"
)

var platformList = []Platform{IOS, Android, MacOS, Windows, Linux, TV}

// App is a client app that takes the link as a subscription.
type App struct {
	Name string
	imp  *template.Template // nil: no deep link, the person adds the link by hand
}

// importData is what an app's deep link template is executed over.
type importData struct{ URL, Title string }

// HasImport: the app has a deep link that adds the link.
func (a *App) HasImport() bool { return a.imp != nil }

// Import is the app's deep link that adds url, named title; "" for an app
// without one.
func (a *App) Import(url, title string) string {
	if a.imp == nil {
		return ""
	}
	var b strings.Builder
	if err := a.imp.Execute(&b, importData{url, title}); err != nil {
		panic(err) // load ran every template, and esc and b64 never fail
	}
	return b.String()
}

// Download is one button to an app's official store or release page.
type Download struct {
	Label i18n.Message
	URL   string
}

// Offer is an app as one platform's tab suggests it.
type Offer struct {
	App       *App
	Note      i18n.Message // zero: none
	Downloads []Download
	Manual    i18n.Message // the app's own import, for when there is no deep link or it does nothing
}

// Shelf is one platform's tab: its apps in order, the first recommended.
type Shelf struct {
	Platform Platform
	// DeepLinks: a browser on the platform can open an app's deep link (a TV
	// has no browser to tap one in).
	DeepLinks bool
	Offers    []Offer
}

//go:embed apps.yaml
var appsYAML []byte

// Shelves are the tabs in order, each with its apps in order: the one
// table the page is drawn from, read from apps.yaml.
var Shelves = mustLoad(appsYAML)

func mustLoad(data []byte) []Shelf {
	s, err := load(data)
	if err != nil {
		panic("linkpage: apps.yaml: " + err.Error())
	}
	return s
}

var importFuncs = template.FuncMap{
	"esc": output.Escape,
	"b64": func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) },
}

// appsFile is apps.yaml as written; load checks it and resolves its names.
type appsFile struct {
	Apps      map[string]struct{ Name, Import string }
	Labels    map[string]i18n.Message
	Manuals   map[string]i18n.Message
	Platforms []struct {
		Platform Platform
		Apps     []struct {
			App       string
			Note      i18n.Message
			Downloads []struct{ Label, URL string }
			Manual    string
		}
	}
}

func load(data []byte) ([]Shelf, error) {
	var f appsFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	whole := func(m i18n.Message) bool { return m.EN != "" && m.RU != "" }

	apps := map[string]*App{}
	for id, a := range f.Apps {
		if a.Name == "" {
			bad("app %s: no name", id)
		}
		app := &App{Name: a.Name}
		if a.Import != "" {
			t, err := template.New(id).Funcs(importFuncs).Option("missingkey=error").Parse(a.Import)
			if err == nil {
				err = t.Execute(new(strings.Builder), importData{"https://example.com/s/x", "x"})
			}
			if err != nil {
				bad("app %s: import: %v", id, err)
			}
			app.imp = t
		}
		apps[id] = app
	}
	for _, texts := range []map[string]i18n.Message{f.Labels, f.Manuals} {
		for k, m := range texts {
			if !whole(m) {
				bad("%s: needs both en and ru", k)
			}
		}
	}

	seen := map[Platform]bool{}
	var shelves []Shelf
	for _, p := range f.Platforms {
		switch {
		case !slices.Contains(platformList, p.Platform):
			bad("platform %q: not one of %v", p.Platform, platformList)
		case seen[p.Platform]:
			bad("platform %s: twice", p.Platform)
		case len(p.Apps) == 0:
			bad("platform %s: no apps", p.Platform)
		}
		seen[p.Platform] = true
		sh := Shelf{Platform: p.Platform, DeepLinks: p.Platform != TV}
		for _, o := range p.Apps {
			where := fmt.Sprintf("%s, %s", p.Platform, o.App)
			app, ok := apps[o.App]
			if !ok {
				bad("%s: no such app", where)
			}
			if o.Note != (i18n.Message{}) && !whole(o.Note) {
				bad("%s: the note needs both en and ru", where)
			}
			manual, ok := f.Manuals[o.Manual]
			if !ok {
				bad("%s: manual %q: no such manual", where, o.Manual)
			}
			if len(o.Downloads) == 0 {
				bad("%s: no downloads", where)
			}
			offer := Offer{App: app, Note: o.Note, Manual: manual}
			for _, d := range o.Downloads {
				label, ok := f.Labels[d.Label]
				if !ok {
					bad("%s: label %q: no such label", where, d.Label)
				}
				if !strings.HasPrefix(d.URL, "https://") {
					bad("%s: %q is not an https URL", where, d.URL)
				}
				offer.Downloads = append(offer.Downloads, Download{Label: label, URL: d.URL})
			}
			sh.Offers = append(sh.Offers, offer)
		}
		shelves = append(shelves, sh)
	}
	for _, p := range platformList {
		if !seen[p] {
			bad("platform %s: missing", p)
		}
	}
	return shelves, errors.Join(errs...)
}
