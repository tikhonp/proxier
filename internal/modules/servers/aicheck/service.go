package aicheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

var ErrDisabled = errors.New("AI checks are disabled")
var ErrNotActive = errors.New("AI checks require an active server")

type Deps struct {
	DB       *db.DB
	Events   *events.Catalog
	Jobs     *jobs.System
	Settings *settings.Store
	Vault    *vault.Vault
}

type Service struct {
	Deps
	Now       func() time.Time
	Dial      func(context.Context, endpoint.Endpoint) (proxy.DialFunc, io.Closer, error)
	HTTP      *http.Client
	BaseURL   string
	TraceURLs map[string]string
	APIURL    string
	// Serialize reputation lookups through cache publication. Trace requests
	// remain concurrent with other servers and health checks.
	mu sync.Mutex
}

func New(d Deps) *Service {
	// HTTP/2 lookups stall on the production network even when the same
	// Net.Coffee response completes over HTTP/1.1.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	return &Service{Deps: d, Now: time.Now, BaseURL: "https://ip.net.coffee",
		HTTP: &http.Client{Timeout: 20 * time.Second, CheckRedirect: noRedirect, Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, Protocols: protocols,
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, IdleConnTimeout: 90 * time.Second,
		}},
		TraceURLs: map[string]string{Claude: "https://claude.ai/cdn-cgi/trace", ChatGPT: "https://chatgpt.com/cdn-cgi/trace"},
		APIURL:    "https://api.openai.com/",
		Dial: func(ctx context.Context, e endpoint.Endpoint) (proxy.DialFunc, io.Closer, error) {
			return proxy.Dialer(ctx, e, proxy.Options{})
		},
	}
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func (s *Service) Enabled(ctx context.Context) (bool, error) {
	return s.Settings.GetBool(ctx, conf.AIEnabled)
}

// Generation includes encrypted connection settings, their update time and
// address changes. The digest is never used as a credential lookup.
func generation(srv store.Server, row store.EndpointRow, addresses []store.Address) string {
	b, _ := json.Marshal(struct {
		IP        string
		Changed   db.Time
		Endpoint  store.EndpointRow
		Addresses []store.Address
	}{srv.IP, srv.IPChangedAt, row, addresses})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Snapshot separates the latest attempt from its last successful lookup.
type Snapshot struct {
	EndpointKey, Service string
	Attempt              store.AIResult
	Success              *store.AIResult
	CurrentIP            string
	Fresh                bool
	Invalidated          bool
}

func (r Snapshot) Score(mode string) (float64, bool) {
	if !r.Fresh || r.Success == nil || r.Success.RawScore == nil {
		return 0, false
	}
	if mode == ReputationMode && r.Service == Claude {
		return *r.Success.RawScore, true
	}
	if mode != r.Service || (mode != Claude && mode != ChatGPT) {
		return 0, false
	}
	switch Eligibility(r.Success.Country) {
	case "unknown":
		return 0, false
	case "restricted":
		return 0, true
	default:
		return *r.Success.RawScore, true
	}
}

func (s *Service) Read(ctx context.Context, id int64) ([]Snapshot, error) {
	on, err := s.Enabled(ctx)
	if err != nil || !on {
		return nil, err
	}
	srv, err := store.GetServer(ctx, s.DB.R, id)
	if err != nil {
		return nil, err
	}
	if srv.State != "active" || srv.Retiring() {
		return nil, nil
	}
	rows, err := store.Endpoints(ctx, s.DB.R, id)
	if err != nil {
		return nil, err
	}
	return s.read(ctx, srv, rows)
}

func (s *Service) read(ctx context.Context, srv store.Server, rows []store.EndpointRow) ([]Snapshot, error) {
	id := srv.ID
	addresses, err := store.ServerAddresses(ctx, s.DB.R, id)
	if err != nil {
		return nil, err
	}
	results, err := store.AIResults(ctx, s.DB.R, id)
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for _, row := range rows {
		gen := generation(srv, row, addresses)
		for _, service := range []string{Claude, ChatGPT, OpenAIAPI} {
			r := Snapshot{EndpointKey: row.Key, Service: service}
			for _, attempt := range results {
				if attempt.EndpointKey != row.Key || attempt.Generation != gen || attempt.Service != service {
					continue
				}
				if r.Attempt.ID == 0 {
					r.Attempt = attempt
				}
				if r.CurrentIP == "" && attempt.IP != "" {
					r.CurrentIP = attempt.IP
				}
				if r.Success == nil && attempt.RawScore != nil {
					copy := attempt
					r.Success = &copy
				}
			}
			if r.Success != nil {
				for _, attempt := range results {
					if attempt.EndpointKey == row.Key && attempt.Generation == gen && attempt.Service == service && attempt.ID > r.Success.ID && attempt.IP != "" && attempt.IP != r.Success.IP {
						r.Invalidated = true
					}
				}
				now := s.Now()
				r.Fresh = !r.Invalidated && r.CurrentIP == r.Success.IP && !r.Success.ObservedAt.After(now) && now.Sub(r.Success.ObservedAt.Time) < FreshAge && !r.Success.LookupAt.After(now) && now.Sub(r.Success.LookupAt.Time) < FreshAge
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// Scores contains only usable output values. Diagnostics stay in this module.
func (s *Service) Scores(ctx context.Context, id int64) (map[string]map[string]float64, error) {
	rows, err := s.Read(ctx, id)
	if err != nil {
		return nil, err
	}
	return scoresOf(rows), nil
}

// ScoresFor uses the catalog's connection snapshot, so concurrent redeploys
// cannot attach a replacement endpoint's score to old connection URIs.
func (s *Service) ScoresFor(ctx context.Context, srv store.Server, eps []store.EndpointRow) (map[string]map[string]float64, error) {
	on, err := s.Enabled(ctx)
	if err != nil || !on {
		return nil, err
	}
	rows, err := s.read(ctx, srv, eps)
	if err != nil {
		return nil, err
	}
	return scoresOf(rows), nil
}

func scoresOf(rows []Snapshot) map[string]map[string]float64 {
	out := map[string]map[string]float64{}
	for _, r := range rows {
		for _, mode := range []string{ReputationMode, Claude, ChatGPT} {
			if score, ok := r.Score(mode); ok {
				if out[r.EndpointKey] == nil {
					out[r.EndpointKey] = map[string]float64{}
				}
				out[r.EndpointKey][mode] = score
			}
		}
	}
	return out
}

// Check never substitutes a direct dial for a failed proxy connection.
func (s *Service) Check(ctx context.Context, id int64, actor string) error {
	on, err := s.Enabled(ctx)
	if err != nil {
		return err
	}
	if !on {
		return ErrDisabled
	}
	srv, err := store.GetServer(ctx, s.DB.R, id)
	if err != nil {
		return err
	}
	if srv.State != "active" || srv.Retiring() {
		return ErrNotActive
	}
	rows, err := store.Endpoints(ctx, s.DB.R, id)
	if err != nil {
		return err
	}
	addresses, err := store.ServerAddresses(ctx, s.DB.R, id)
	if err != nil {
		return err
	}
	eps, err := sealed.OpenEndpoints(s.Vault, rows)
	if err != nil {
		return err
	}
	for i, e := range eps {
		current, err := store.GetServer(ctx, s.DB.R, id)
		if err != nil {
			return err
		}
		if current.State != "active" || current.Retiring() {
			return nil
		}
		gen := generation(srv, rows[i], addresses)
		dial, closer, dialErr := s.Dial(ctx, e)
		if dialErr == nil && (dial == nil || closer == nil) {
			dialErr = errors.New("proxy dialer unavailable")
		}
		if err := s.checkEndpoint(ctx, id, e.Key, gen, dial, closer, dialErr, actor); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) checkEndpoint(ctx context.Context, id int64, key, gen string, dial proxy.DialFunc, closer io.Closer, dialErr error, actor string) error {
	if closer != nil {
		defer func() { _ = closer.Close() }()
	}
	transport := &http.Transport{DialContext: dial, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	defer transport.CloseIdleConnections()
	hc := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: noRedirect}
	for _, service := range []string{Claude, ChatGPT, OpenAIAPI} {
		if err := ctx.Err(); err != nil {
			return err
		}
		on, err := s.Enabled(ctx)
		if err != nil {
			return err
		}
		if !on {
			return ErrDisabled
		}
		current, err := store.GetServer(ctx, s.DB.R, id)
		if err != nil {
			return err
		}
		if current.State != "active" || current.Retiring() {
			return nil
		}
		r := store.AIResult{ServerID: id, EndpointKey: key, Generation: gen, Service: service, AttemptedAt: db.At(s.Now()), Flags: "{}"}
		if dialErr != nil {
			r.Error = "proxy dialer failed"
		} else if service == OpenAIAPI {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.APIURL, nil)
			if err != nil {
				return err
			}
			resp, err := hc.Do(req)
			if err != nil {
				r.Error = "API request failed"
			} else {
				r.HTTPStatus = resp.StatusCode
				_ = resp.Body.Close()
				r.Reachable = resp.StatusCode >= 200 && resp.StatusCode < 500
				if !r.Reachable {
					r.Error = "API HTTP " + strconv.Itoa(resp.StatusCode)
				}
			}
		} else {
			ip, status, err := observe(ctx, hc, s.TraceURLs[service])
			r.HTTPStatus = status
			if err != nil {
				r.Error = err.Error()
			} else {
				r.Reachable, r.IP, r.ObservedAt = true, ip.String(), db.At(s.Now())
				r.Family = 6
				if ip.Is4() {
					r.Family = 4
				}
				if err := s.reputation(ctx, r, ip, actor); err != nil {
					return err
				}
				continue
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.save(ctx, r, nil, time.Time{}, actor); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reputation(ctx context.Context, r store.AIResult, ip netip.Addr, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	on, err := s.Enabled(ctx)
	if err != nil {
		return err
	}
	if !on {
		return ErrDisabled
	}
	var cache *store.AICache
	var cooldown time.Time
	cached, ok, err := store.GetAICache(ctx, s.DB.R, r.IP)
	if err != nil {
		return err
	}
	var rep Reputation
	if ok && !cached.FetchedAt.After(s.Now()) && s.Now().Sub(cached.FetchedAt.Time) < CacheAge {
		if err := json.Unmarshal([]byte(cached.Reputation), &rep); err != nil {
			return err
		}
		r.LookupAt = cached.FetchedAt
	} else {
		until, err := store.AIBackoff(ctx, s.DB.R)
		if err != nil {
			return err
		}
		if until.After(s.Now()) {
			r.Error = "reputation rate limited"
		} else {
			rep, cooldown, err = lookup(ctx, s.HTTP, s.BaseURL, ip, s.Now)
			if err != nil {
				r.Error = err.Error()
			} else {
				r.LookupAt = db.At(s.Now())
				b, _ := json.Marshal(rep)
				cache = &store.AICache{IP: r.IP, FetchedAt: r.LookupAt, Reputation: string(b)}
			}
		}
	}
	if r.Error == "" {
		r.RawScore, r.Country = rep.Score, rep.Country
		b, _ := json.Marshal(rep)
		r.Flags = string(b)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.save(ctx, r, cache, cooldown, actor)
}

func (s *Service) save(ctx context.Context, r store.AIResult, cache *store.AICache, cooldown time.Time, actor string) error {
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		// Retirement can begin during the remote request. Old generations may
		// remain as history, but Read never publishes them as current.
		srv, err := store.GetServer(ctx, tx, r.ServerID)
		if err != nil {
			return err
		}
		discarded := srv.State != "active" || srv.Retiring()
		if discarded && cache == nil && cooldown.IsZero() {
			return nil
		}
		if !discarded {
			if err := store.InsertAIResult(ctx, tx, r); err != nil {
				return err
			}
		}
		if cache != nil {
			if err := store.PutAICache(ctx, tx, *cache); err != nil {
				return err
			}
		}
		if !cooldown.IsZero() {
			if err := store.SetAIBackoff(ctx, tx, db.At(cooldown)); err != nil {
				return err
			}
		}
		_, err = s.Events.Record(ctx, tx, events.Event{Time: db.At(s.Now()), Type: "server.ai_check_completed", Subject: store.ServerSubject(r.ServerID), Actor: actor,
			Payload: map[string]any{"endpoint": r.EndpointKey, "service": r.Service, "reachable": r.Reachable, "score_available": r.RawScore != nil, "error": r.Error, "discarded": discarded}})
		return err
	})
}
