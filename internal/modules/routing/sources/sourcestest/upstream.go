// Package sourcestest is one httptest server playing every upstream of the
// routing module: v2fly's raw files, the GitHub API and codeload (3c), the
// three iplist portals, and any URL list.
package sourcestest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	hang     map[string]bool
	requests []string
	agents   []string

	commit     string          // the GitHub commits API answer; "" answers 500
	githubDown bool            // the API and codeload answer 502
	exportDown map[string]bool // only that portal's custom export fails
	auth       string          // the last Authorization header seen by the API
}

// New starts the fake for the test.
func New(t *testing.T) *Upstream {
	t.Helper()
	u := &Upstream{
		v2fly: map[string]string{}, sites: map[string]map[string][]string{}, groups: map[string]map[string][]string{},
		down: map[string]bool{}, files: map[string]string{}, fail: map[string]int{}, hang: map[string]bool{},
		exportDown: map[string]bool{}, commit: DefaultCommit,
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

// Fail makes that path (as requested, without the query) answer status; 0
// makes it answer normally again.
func (u *Upstream) Fail(path string, status int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if status == 0 {
		delete(u.fail, path)
		return
	}
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

// DefaultCommit is the commit the GitHub API answers until Commit changes it.
const DefaultCommit = "1111111111111111111111111111111111111111"

// Commit sets the GitHub commits API answer; "" makes it answer 500.
func (u *Upstream) Commit(sha string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.commit = sha
}

// GitHubDown makes the GitHub API and codeload answer 502.
func (u *Upstream) GitHubDown(down bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.githubDown = down
}

// ExportDown makes only that portal's custom export (the catalog) answer 502.
func (u *Upstream) ExportDown(portal string, down bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.exportDown[portal] = down
}

// Hang makes requests for that path (without the query) wait until their
// client gives up: a refresh cut short.
func (u *Upstream) Hang(path string, on bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.hang[path] = on
}

// Auth is the last Authorization header the GitHub API saw.
func (u *Upstream) Auth() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.auth
}

func (u *Upstream) serve(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	if u.hang[r.URL.Path] {
		u.mu.Unlock()
		<-r.Context().Done()
		return
	}
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
	case strings.HasPrefix(p, "/github/"):
		u.auth = r.Header.Get("Authorization")
		switch {
		case u.githubDown:
			http.Error(w, "bad gateway", http.StatusBadGateway)
		case p != "/github/repos/v2fly/domain-list-community/commits/master" || r.Header.Get("Accept") != "application/vnd.github.sha":
			http.NotFound(w, r)
		case u.commit == "":
			http.Error(w, "server error", http.StatusInternalServerError)
		default:
			_, _ = w.Write([]byte(u.commit))
		}
	case strings.HasPrefix(p, "/codeload/v2fly/domain-list-community/tar.gz/"):
		if u.githubDown {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		ref := strings.TrimPrefix(p, "/codeload/v2fly/domain-list-community/tar.gz/")
		w.Header().Set("Content-Type", "application/x-gzip")
		_, _ = w.Write(u.archive(ref))
	case strings.HasPrefix(p, "/iplist/"):
		portal := strings.Trim(strings.TrimPrefix(p, "/iplist/"), "/")
		if u.down[portal] {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		q := r.URL.Query()
		if q.Get("format") == "custom" {
			if u.exportDown[portal] {
				http.Error(w, "bad gateway", http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte(u.export(portal, q.Get("template"))))
			return
		}
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

// archive is a tar.gz of every V2fly file under domain-list-community-<ref>/data/,
// as codeload names the top directory.
func (u *Upstream) archive(ref string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	top := "domain-list-community-" + ref + "/"
	_ = tw.WriteHeader(&tar.Header{Name: top, Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: top + "data/", Typeflag: tar.TypeDir, Mode: 0o755})
	names := make([]string, 0, len(u.v2fly))
	for n := range u.v2fly {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		body := u.v2fly[n]
		_ = tw.WriteHeader(&tar.Header{Name: top + "data/" + n, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	readme := "domain list community"
	_ = tw.WriteHeader(&tar.Header{Name: top + "README.md", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(readme))})
	_, _ = tw.Write([]byte(readme))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// export is the custom export of a portal: one line per domain of every
// site, by the template ({group}, {site}, {data}).
func (u *Upstream) export(portal, template string) string {
	var lines []string
	groups := make([]string, 0, len(u.groups[portal]))
	for g := range u.groups[portal] {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	for _, g := range groups {
		for _, site := range u.groups[portal][g] {
			for _, d := range u.sites[portal][site] {
				lines = append(lines, strings.NewReplacer("{group}", g, "{site}", site, "{data}", d).Replace(template))
			}
		}
	}
	return strings.Join(lines, "\n")
}
