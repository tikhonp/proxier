// Package cloudflaretest is an in-memory Cloudflare API on an httptest
// server: zones, A records, a token, and scripted failures. Tests point a
// cloudflare.Client's BaseURL at it.
package cloudflaretest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Record is a stored A record.
type Record struct {
	ID, Zone, Name, Content, Comment string
	TTL                              int
	Proxied                          bool
}

type step struct {
	status     int
	retryAfter string
}

// Fake is the API.
type Fake struct {
	t     *testing.T
	srv   *httptest.Server
	token string

	mu       sync.Mutex
	zones    []string // zone names; the id is "zone-<n>" (1-based)
	records  []Record
	next     int
	calls    []string
	script   map[string][]step
	slept    []time.Duration
	rejected bool
	// EchoAuth makes error answers repeat the Authorization header, to prove
	// the client never lets the token out.
	EchoAuth bool
	// PerPage is the page size of the zone list (default 50).
	PerPage int
}

// New starts the fake for token with the named zones ("zone-1", "zone-2", …).
func New(t *testing.T, token string, zones ...string) *Fake {
	t.Helper()
	f := &Fake{t: t, token: token, zones: zones, script: map[string][]step{}, PerPage: 50}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user/tokens/verify", f.handle(f.verify))
	mux.HandleFunc("GET /zones", f.handle(f.listZones))
	mux.HandleFunc("GET /zones/{zone}/dns_records", f.handle(f.findRecords))
	mux.HandleFunc("POST /zones/{zone}/dns_records", f.handle(f.create))
	mux.HandleFunc("GET /zones/{zone}/dns_records/{id}", f.handle(f.get))
	mux.HandleFunc("PUT /zones/{zone}/dns_records/{id}", f.handle(f.update))
	mux.HandleFunc("DELETE /zones/{zone}/dns_records/{id}", f.handle(f.remove))
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the base URL for cloudflare.Client.BaseURL.
func (f *Fake) URL() string { return f.srv.URL }

// ZoneID is the id of the named zone.
func (f *Fake) ZoneID(name string) string {
	i := slices.Index(f.zones, name)
	if i < 0 {
		f.t.Fatalf("cloudflaretest: no zone %q", name)
	}
	return "zone-" + strconv.Itoa(i+1)
}

// AddRecord stores a record as if someone else made it; it returns the id.
func (f *Fake) AddRecord(zone, name, content, comment string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	r := Record{ID: "rec-" + strconv.Itoa(f.next), Zone: zone, Name: name, Content: content, Comment: comment, TTL: 1}
	f.records = append(f.records, r)
	return r.ID
}

// Records are the stored records of a zone (all zones for "").
func (f *Fake) Records(zone string) []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Record
	for _, r := range f.records {
		if zone == "" || r.Zone == zone {
			out = append(out, r)
		}
	}
	return out
}

// Change edits a stored record, as the admin would in Cloudflare's dashboard.
func (f *Fake) Change(id string, fn func(*Record)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.records {
		if f.records[i].ID == id {
			fn(&f.records[i])
			return
		}
	}
	f.t.Fatalf("cloudflaretest: no record %s", id)
}

// Drop deletes a record behind the client's back.
func (f *Fake) Drop(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = slices.DeleteFunc(f.records, func(r Record) bool { return r.ID == id })
}

// Calls are the requests so far, as "METHOD /route/{pattern}".
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Count is how many requests matched the route pattern.
func (f *Fake) Count(route string) int {
	n := 0
	for _, c := range f.Calls() {
		if c == route {
			n++
		}
	}
	return n
}

// Fail makes the next calls to route (a mux pattern such as
// "POST /zones/{zone}/dns_records") answer with the given statuses, in order,
// before behaving normally again.
func (f *Fake) Fail(route string, statuses ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range statuses {
		f.script[route] = append(f.script[route], step{status: s})
	}
}

// RateLimit makes the next n calls to route answer 429 with Retry-After.
func (f *Fake) RateLimit(route, retryAfter string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for range n {
		f.script[route] = append(f.script[route], step{status: 429, retryAfter: retryAfter})
	}
}

// RejectToken makes every call answer 403.
func (f *Fake) RejectToken() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejected = true
}

func (f *Fake) handle(fn func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Pattern)
		var s *step
		if q := f.script[r.Pattern]; len(q) > 0 {
			s = &q[0]
			f.script[r.Pattern] = q[1:]
		}
		token, rejected := f.token, f.rejected
		f.mu.Unlock()

		if rejected || r.Header.Get("Authorization") != "Bearer "+token {
			f.fail(w, r, http.StatusForbidden, 9109, "Invalid access token")
			return
		}
		if s != nil {
			if s.retryAfter != "" {
				w.Header().Set("Retry-After", s.retryAfter)
			}
			f.fail(w, r, s.status, 1000, "scripted failure")
			return
		}
		fn(w, r)
	}
}

func (f *Fake) fail(w http.ResponseWriter, r *http.Request, status, code int, msg string) {
	if f.EchoAuth {
		msg += " [" + r.Header.Get("Authorization") + "]"
	}
	write(w, status, map[string]any{"success": false, "errors": []any{map[string]any{"code": code, "message": msg}}, "result": nil})
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func ok(w http.ResponseWriter, result any, info map[string]any) {
	body := map[string]any{"success": true, "errors": []any{}, "result": result}
	if info != nil {
		body["result_info"] = info
	}
	write(w, 200, body)
}

func (f *Fake) verify(w http.ResponseWriter, _ *http.Request) {
	ok(w, map[string]any{"id": "tok", "status": "active"}, nil)
}

func (f *Fake) listZones(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	per := f.PerPage
	if n, err := strconv.Atoi(r.URL.Query().Get("per_page")); err == nil && n > 0 && n < per {
		per = n
	}
	var all []map[string]any
	for i, name := range f.zones {
		all = append(all, map[string]any{"id": "zone-" + strconv.Itoa(i+1), "name": name, "status": "active"})
	}
	pages := (len(all) + per - 1) / per
	lo := min((page-1)*per, len(all))
	hi := min(lo+per, len(all))
	ok(w, all[lo:hi], map[string]any{"page": page, "per_page": per, "total_pages": max(pages, 1), "count": hi - lo, "total_count": len(all)})
}

func (f *Fake) zoneName(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("zone")
	n, err := strconv.Atoi(strings.TrimPrefix(id, "zone-"))
	if err != nil || n < 1 || n > len(f.zones) {
		f.fail(w, r, 404, 7003, "Could not route to /zones/"+id)
		return "", false
	}
	return f.zones[n-1], true
}

func wireOf(r Record) map[string]any {
	return map[string]any{"id": r.ID, "type": "A", "name": r.Name, "content": r.Content, "ttl": r.TTL, "proxied": r.Proxied, "comment": r.Comment}
}

func (f *Fake) findRecords(w http.ResponseWriter, r *http.Request) {
	zone, good := f.zoneName(w, r)
	if !good {
		return
	}
	name := r.URL.Query().Get("name")
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []map[string]any{}
	for _, rec := range f.records {
		if rec.Zone == zone && (name == "" || strings.EqualFold(rec.Name, name)) {
			out = append(out, wireOf(rec))
		}
	}
	ok(w, out, map[string]any{"page": 1, "total_pages": 1})
}

type input struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

func (f *Fake) create(w http.ResponseWriter, r *http.Request) {
	zone, good := f.zoneName(w, r)
	if !good {
		return
	}
	var in input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Type != "A" || in.Name == "" || in.Content == "" {
		f.fail(w, r, 400, 9005, "Content for A record is invalid.")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	rec := Record{ID: "rec-" + strconv.Itoa(f.next), Zone: zone, Name: in.Name, Content: in.Content, Comment: in.Comment, TTL: in.TTL, Proxied: in.Proxied}
	f.records = append(f.records, rec)
	ok(w, wireOf(rec), nil)
}

func (f *Fake) index(zone, id string) int {
	return slices.IndexFunc(f.records, func(r Record) bool { return r.Zone == zone && r.ID == id })
}

func (f *Fake) get(w http.ResponseWriter, r *http.Request) {
	zone, good := f.zoneName(w, r)
	if !good {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.index(zone, r.PathValue("id"))
	if i < 0 {
		f.fail(w, r, 404, 81044, "Record does not exist.")
		return
	}
	ok(w, wireOf(f.records[i]), nil)
}

func (f *Fake) update(w http.ResponseWriter, r *http.Request) {
	zone, good := f.zoneName(w, r)
	if !good {
		return
	}
	var in input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		f.fail(w, r, 400, 9005, "bad body")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.index(zone, r.PathValue("id"))
	if i < 0 {
		f.fail(w, r, 404, 81044, "Record does not exist.")
		return
	}
	rec := &f.records[i]
	rec.Name, rec.Content, rec.TTL, rec.Proxied, rec.Comment = in.Name, in.Content, in.TTL, in.Proxied, in.Comment
	ok(w, wireOf(*rec), nil)
}

func (f *Fake) remove(w http.ResponseWriter, r *http.Request) {
	zone, good := f.zoneName(w, r)
	if !good {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.index(zone, r.PathValue("id"))
	if i < 0 {
		f.fail(w, r, 404, 81044, "Record does not exist.")
		return
	}
	id := f.records[i].ID
	f.records = slices.Delete(f.records, i, i+1)
	ok(w, map[string]any{"id": id}, nil)
}
