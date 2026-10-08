// Package sourcestest is one httptest server playing every upstream of the
// routing module: v2fly's raw files, the GitHub API and codeload (3c), the
// three iplist portals, and any URL list.
package sourcestest

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/sources"
)

// Upstream is the fake. Paths: /v2fly/data/<name>, /github/…, /codeload/…,
// /iplist/<portal>/?…, /files/<path>.
type Upstream struct {
	srv *httptest.Server

	mu       sync.Mutex
	v2fly    map[string]string
	sites    map[string]map[string][]string // portal → site → names
	groups   map[string]map[string][]string // portal → group → sites
	down     map[string]bool
	files    map[string]string
	fail     map[string]int
	requests []string
	agents   []string
}

// New starts the fake for the test.
func New(t *testing.T) *Upstream {
	t.Helper()
	u := &Upstream{
		v2fly: map[string]string{}, sites: map[string]map[string][]string{}, groups: map[string]map[string][]string{},
		down: map[string]bool{}, files: map[string]string{}, fail: map[string]int{},
	}
	u.srv = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.srv.Close)
	return u
}

// Endpoints point every source at the fake.
func (u *Upstream) Endpoints() sources.Endpoints {
	base := u.srv.URL
	return sources.Endpoints{
		V2flyRaw:  base + "/v2fly/data/",
		GitHubAPI: base + "/github/",
		Codeload:  base + "/codeload/",
		Portals: []sources.Portal{
			{Name: "main", Base: base + "/iplist/main/"},
			{Name: "beta", Base: base + "/iplist/beta/"},
			{Name: "russia", Base: base + "/iplist/russia/"},
		},
	}
}

// URL is the fake's address.
func (u *Upstream) URL() string { return u.srv.URL }

// V2fly serves a list file under data/.
func (u *Upstream) V2fly(name, body string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.v2fly[name] = body
}

// Site puts a site with its names into a group of a portal.
func (u *Upstream) Site(portal, group, site string, names ...string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.sites[portal] == nil {
		u.sites[portal], u.groups[portal] = map[string][]string{}, map[string][]string{}
	}
	u.sites[portal][site] = names
	u.groups[portal][group] = append(u.groups[portal][group], site)
}

// PortalDown makes a portal answer 502.
func (u *Upstream) PortalDown(portal string, down bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.down[portal] = down
}

// File serves a URL list at /files/<path> and returns its URL.
func (u *Upstream) File(path, body string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	path = strings.TrimPrefix(path, "/")
	u.files[path] = body
	return u.srv.URL + "/files/" + path
}

// Fail makes that path (as requested, without the query) answer status.
func (u *Upstream) Fail(path string, status int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.fail[path] = status
}

// Requests counts the requests whose path starts with prefix.
func (u *Upstream) Requests(prefix string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, p := range u.requests {
		if strings.HasPrefix(p, prefix) {
			n++
		}
	}
	return n
}

// Agents lists every User-Agent seen.
func (u *Upstream) Agents() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.agents...)
}

func (u *Upstream) serve(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	p := r.URL.Path
	u.requests = append(u.requests, p)
	u.agents = append(u.agents, r.UserAgent())
	if st, ok := u.fail[p]; ok {
		http.Error(w, http.StatusText(st), st)
		return
	}
	switch {
	case strings.HasPrefix(p, "/v2fly/data/"):
		body, ok := u.v2fly[strings.TrimPrefix(p, "/v2fly/data/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	case strings.HasPrefix(p, "/iplist/"):
		portal := strings.Trim(strings.TrimPrefix(p, "/iplist/"), "/")
		if u.down[portal] {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		q := r.URL.Query()
		var names []string
		if site := q.Get("site"); site != "" {
			names = u.sites[portal][site]
		} else if group := q.Get("group"); group != "" {
			for _, s := range u.groups[portal][group] {
				names = append(names, u.sites[portal][s]...)
			}
			sort.Strings(names)
		}
		_, _ = w.Write([]byte(strings.Join(names, "\n")))
	case strings.HasPrefix(p, "/files/"):
		body, ok := u.files[strings.TrimPrefix(p, "/files/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	default:
		http.NotFound(w, r)
	}
}
