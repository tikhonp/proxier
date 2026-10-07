// Package ui holds Proxier's templ components and static assets. It depends
// on i18n and the settings types only and takes plain view structs, so every
// module can use it (docs/build/0b.md).
package ui

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"sort"

	"github.com/a-h/templ"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

//go:embed static
var embedded embed.FS

// StaticFS is the embedded static directory, rooted at its contents.
var StaticFS fs.FS

// StaticHash names the asset URLs: the first 12 hex digits of a SHA-256 over
// every embedded file's path and content. A deploy that changes a file
// changes every URL, so they can be cached for good.
var StaticHash string

func init() {
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		panic(err)
	}
	StaticFS = sub
	var names []string
	_ = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		b, _ := fs.ReadFile(sub, n)
		h.Write([]byte(n + "\x00"))
		h.Write(b)
		h.Write([]byte{0})
	}
	StaticHash = hex.EncodeToString(h.Sum(nil))[:12]
}

// Asset is the URL of a static file.
func Asset(path string) string { return "/static/" + StaticHash + "/" + path }

// Shell is what the layout needs on every signed-in page.
type Shell struct {
	Title    string // already translated
	Path     string // current path, for the sidebar's current row
	CSRF     string
	Admin    string // username, for the admin menu
	Lang     i18n.Lang
	Nav      []NavGroup
	Keys     Keymap
	Warning  *HeaderWarning  // optional warning cell (0d: Telegram)
	JobsCell templ.Component // the header cell, polled from /jobs/cell
	PageKeys string          // key line hint when nothing is under the cursor (i18n key)
	// Scripts are extra ES modules of this page, as static paths
	// ("js/editor.bundle.js"); the layout loads them after proxier.js.
	Scripts []string
}

// HeaderWarning is the optional cell next to the jobs cell.
type HeaderWarning struct {
	Text string // translated
	Href string
}

type NavItem struct {
	Group string // "overview" | "servers" | "subscriptions" | "routing" | "routerscripts" | "system"
	Label string // i18n key
	Href  string
	GoKey string // letter after g, "" for none
	Order int
}

// NavGroup's Label is an i18n key.
type NavGroup struct {
	Label string
	Items []NavItem
}

type SettingsPage struct {
	Slug  string // "/settings/<slug>"
	Title string // i18n key
	Order int    // General 10, Security 20, SSH 30, Integrations 40, Notifications 50, … About 900
}

// IntegrationRow is one line of Settings → Integrations.
type IntegrationRow struct {
	Name, Href, State string // State is already translated
	On                bool
}

// DashboardArea is a module's block on the dashboard. Areas are sorted by
// Order after the platform's own.
type DashboardArea struct {
	Order int
	Title string          // already translated
	Body  templ.Component // rendered inside the area
}

// SubjectRef names a subject of an event or job and links to it.
type SubjectRef struct {
	Label string
	Href  string
}

type SearchHit struct {
	Label, Meta, Href string
}

type ActionKind int

const (
	Ordinary ActionKind = iota
	Primary
	Danger
	Ghost
)

// Action is anything a button or link does. Rendered by ActionButton with
// data-action, so the search pop-up lists it with the same words.
type Action struct {
	ID      string // "auth.sign_out_everywhere"
	Label   string // i18n key
	Method  string // "GET" link or "POST" form
	Href    string
	Key     string // optional page key, e.g. "r"
	Kind    ActionKind
	Small   bool
	Confirm *Confirm          // nil: no dialog
	Fields  map[string]string // hidden inputs for POST
	// Hidden keeps the action in the page for the search pop-up and its key
	// without drawing it (the global actions).
	Hidden bool
}

// Confirm describes the dialog before a destructive action.
type Confirm struct {
	Strength     int    // 1 simple, 2 consequences, 3 type the name
	Title, Body  string // translated
	Consequences []string
	Name         string // strength 3: the text to type
}

type Keymap []KeyGroup // rendered into the ? sheet

type KeyGroup struct {
	Where string // i18n key
	Keys  []Key
}

type Key struct{ Keys, What string } // "g d", i18n key

// SignInView is what the sign-in page shows.
type SignInView struct {
	Username  string
	Next      string // already safe
	Error     string // "" | "wrong" | "locked"
	LockedMin int
	Ended     string // "" | "idle" | "absolute"
}

type csrfKey struct{}

// WithCSRF stores the session's CSRF token for the forms ActionButton draws.
func WithCSRF(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, csrfKey{}, token)
}

// CSRF returns the token set by WithCSRF.
func CSRF(ctx context.Context) string {
	t, _ := ctx.Value(csrfKey{}).(string)
	return t
}

func csrfHeaders(token string) string {
	b, _ := json.Marshal(map[string]string{"X-CSRF-Token": token})
	return string(b)
}

const htmxConfig = `{"includeIndicatorStyles":false,"allowEval":false,"allowScriptTags":false,"selfRequestsOnly":true,"historyCacheSize":0,"refreshOnHistoryMiss":true}`

// Chip is a filter shown as a pressed or released link.
type Chip struct {
	Label string // already translated
	Href  string
	On    bool
}

// FilterForm is the part of a filter bar that needs a choice: selects sent
// with a GET.
type FilterForm struct {
	Action  string
	Selects []FilterSelect
	Hidden  map[string]string // filters to keep
	Apply   string            // already translated
}

type FilterSelect struct {
	Name, Label string // Label already translated
	Value       string
	Options     []FilterOption
}

type FilterOption struct{ Value, Label string }

// JobsCellView feeds the header jobs cell.
type JobsCellView struct {
	Running int
	Failed  int // failed since the admin last looked
}
