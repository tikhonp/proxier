package mtvpn

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/shadowrocket"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Row statuses and outcomes.
const (
	StatusNew     = "new"
	StatusExists  = "exists"
	StatusInvalid = "invalid"

	Created   = "created"
	Reused    = "reused"
	Converted = "converted"
	Skipped   = "skipped"
	Failed    = "failed"
)

// FromServices is the From of a row of the file's own services.
const FromServices = "services"

// GuardHit is why a row was skipped for the server-hostname guard.
type GuardHit struct {
	Domain   string `json:"domain"`
	Server   string `json:"server"`
	Hostname string `json:"hostname"`
}

// Row is one selector of the import. Problem and Error are an i18n key
// (mtvpn., services., shadowrocket.) or an upstream's error text.
type Row struct {
	Selector   string    `json:"selector"`
	Tag        string    `json:"tag"`
	From       string    `json:"from"`   // services, or the list's file name
	Status     string    `json:"status"` // new, exists, invalid
	Problem    string    `json:"problem,omitempty"`
	ExistingAs string    `json:"existing_as,omitempty"` // the existing service's selector; "" for a custom one
	URL        bool      `json:"url"`
	Include    bool      `json:"include"`
	Convert    bool      `json:"convert"`
	Outcome    string    `json:"outcome,omitempty"` // created, reused, converted, skipped, failed
	Error      string    `json:"error,omitempty"`
	Guard      *GuardHit `json:"guard,omitempty"`
	ServiceID  int64     `json:"service_id,omitempty"`
}

// ListFailure is a service list that couldn't be read.
type ListFailure struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

// Shadowrocket is the config offered from shadowrocket_base, then its outcome.
type Shadowrocket struct {
	Offered bool   `json:"offered"`
	Lines   int    `json:"lines"`
	Problem string `json:"problem,omitempty"`
	Create  bool   `json:"create"`
	Name    string `json:"name"`
	ListID  int64  `json:"list_id"`
	Outcome string `json:"outcome,omitempty"` // created, failed
	Error   string `json:"error,omitempty"`
	ID      int64  `json:"id,omitempty"`
}

// Import is a stored preview, then its result.
type Import struct {
	ID           int64
	State        string // preview, running, done, failed
	ListID       int64  // 0 when the list was deleted meanwhile
	Rows         []Row
	ListFailures []ListFailure
	Ignored      []string
	BaseURL      string
	Shadowrocket Shadowrocket
	JobID        int64
	CreatedAt    time.Time
	FinishedAt   time.Time

	base string
}

// Importable counts the rows that can be ticked.
func (im Import) Importable() int {
	n := 0
	for _, r := range im.Rows {
		if r.Status != StatusInvalid {
			n++
		}
	}
	return n
}

// Deps are what the service uses.
type Deps struct {
	DB           *db.DB
	Events       *events.Catalog
	Jobs         *jobs.System
	Services     *services.Service
	Lists        *lists.Service
	Shadowrocket *shadowrocket.Service
	Resolver     *sources.Resolver // the job's: 30 s per request
	Fetch        *sources.Fetcher  // the preview's: 15 s per request
	Now          func() time.Time
	Log          *slog.Logger
}

// Service runs imports.
type Service struct{ d Deps }

// New returns the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Service{d: d}
}

// What a preview is refused for, besides *ParseError.
var (
	ErrNotFound   = errors.New("mtvpn: no such import")
	ErrNotPreview = errors.New("mtvpn: the import already ran")
)

// Interactive bounds the preview's fetches together.
const Interactive = 45 * time.Second

// Preview parses the file, fetches its service lists and its base, builds
// the rows and stores them as a preview. The text itself is never stored,
// logged or put in a job. An empty or too big file is store.FieldErrors
// {"file": …}; a parse error is a *ParseError.
func (s *Service) Preview(ctx context.Context, text string, actor string) (int64, error) {
	if strings.TrimSpace(text) == "" {
		return 0, store.FieldErrors{"file": "mtvpn.err.empty"}
	}
	f, err := Parse(text)
	if errors.Is(err, ErrTooBig) {
		return 0, store.FieldErrors{"file": "mtvpn.err.too_big"}
	}
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, Interactive)
	defer cancel()

	var rows []Row
	seen := map[string]bool{}
	addRow := func(entry, from string) error {
		r, key := s.row(ctx, entry, from)
		if seen[key] {
			return nil
		}
		seen[key] = true
		if r.Status == StatusExists || r.Status == StatusNew {
			ex, err := store.ServiceByTag(ctx, s.d.DB.R, r.Tag)
			switch {
			case err == nil:
				r.Status, r.ExistingAs = StatusExists, ex.Selector
			case !errors.Is(err, store.ErrNotFound):
				return err
			}
		}
		rows = append(rows, r)
		return nil
	}
	for _, e := range f.Services {
		if err := addRow(e, FromServices); err != nil {
			return 0, err
		}
	}
	var failures []ListFailure
	for _, raw := range f.ServiceLists {
		entries, from, err := s.readList(ctx, raw)
		if err != nil {
			if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return 0, ctx.Err()
			}
			failures = append(failures, ListFailure{URL: raw, Error: listError(err)})
			continue
		}
		for _, e := range entries {
			if err := addRow(e, from); err != nil {
				return 0, err
			}
		}
	}
	def, err := store.DefaultList(ctx, s.d.DB.R)
	if err != nil {
		return 0, err
	}
	sr, base, err := s.base(ctx, f.Base, def.ID)
	if err != nil {
		return 0, err
	}
	baseURL := ""
	if isURL(f.Base) {
		baseURL = f.Base
	}
	rowsJSON, err := marshal(rows, "[]")
	if err != nil {
		return 0, err
	}
	failJSON, err := marshal(failures, "[]")
	if err != nil {
		return 0, err
	}
	srJSON, err := json.Marshal(sr)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		id, err = store.InsertImport(ctx, tx, store.Import{
			ListID: sql.NullInt64{Int64: def.ID, Valid: true}, Rows: rowsJSON, Ignored: strings.Join(f.Ignored, ","),
			Base: base, BaseURL: baseURL, Shadowrocket: string(srJSON), ListFailures: failJSON, CreatedAt: db.At(s.d.Now()),
		})
		return err
	})
	if err == nil {
		s.d.Log.Info("routing: mtvpn import previewed", "import", id, "rows", len(rows), "actor", actor)
	}
	return id, err
}

func marshal(v any, empty string) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if string(b) == "null" {
		return empty, nil
	}
	return string(b), nil
}

// row reads one selector; key is what deduplicates it (the tag, or the text
// of a selector that can't be read).
func (s *Service) row(_ context.Context, entry, from string) (Row, string) {
	r := Row{Selector: entry, From: from}
	sel, err := selector.Parse(entry)
	if err == nil {
		r.Tag, err = sel.Tag()
	}
	if err != nil {
		r.Status, r.Problem = StatusInvalid, selectorProblem(err)
		return r, "\x00" + entry
	}
	r.Selector, r.Status, r.URL, r.Include = sel.String(), StatusNew, sel.Source == selector.URL, true
	return r, r.Tag
}

func selectorProblem(err error) string {
	switch {
	case errors.Is(err, selector.ErrBadURL):
		return "services.err.bad_url"
	case errors.Is(err, selector.ErrBadName):
		return "services.err.bad_name"
	case errors.Is(err, selector.ErrBadTag):
		return "services.err.bad_tag"
	}
	return "mtvpn.err.selector"
}

func isURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// errLocalPath: a service list or base that is a path on the admin's computer.
var errLocalPath = errors.New("mtvpn: a local path")

// readList fetches and reads one service list; from is its file name.
func (s *Service) readList(ctx context.Context, raw string) ([]string, string, error) {
	if !isURL(raw) {
		return nil, "", errLocalPath
	}
	u, _ := url.Parse(raw)
	from := path.Base(u.Path)
	if from == "." || from == "/" {
		from = u.Host
	}
	status, body, err := s.d.Fetch.Get(ctx, raw, sources.MaxListBytes)
	if err != nil {
		return nil, from, err
	}
	if status != 200 {
		return nil, from, &sources.HTTPError{URL: raw, Status: status}
	}
	entries, err := ReadServiceList(string(body))
	return entries, from, err
}

// listError is a failing list's reason, as stored: a key or upstream text.
func listError(err error) string {
	var le *ListLineError
	switch {
	case errors.Is(err, errLocalPath):
		return "mtvpn.err.list_path"
	case errors.Is(err, ErrEmptyList):
		return "mtvpn.err.list_empty"
	case errors.As(err, &le):
		return "mtvpn.err.list_space:" + strconv.Itoa(le.Line)
	}
	return sources.ErrorText(err)
}

// base reads shadowrocket_base and offers a config when it has a [Rule]
// section: named iphone (iphone-2, … when taken), unticked when a config
// already serves the same base.
func (s *Service) base(ctx context.Context, raw string, listID int64) (Shadowrocket, string, error) {
	var sr Shadowrocket
	if raw == "" {
		return sr, "", nil
	}
	if !isURL(raw) {
		sr.Problem = "mtvpn.base.path"
		return sr, "", nil
	}
	text, err := s.d.Shadowrocket.ImportBase(ctx, raw)
	if err == nil {
		err = shadowrocket.Validate([]byte(text))
	}
	var he *shadowrocket.HTTPError
	switch {
	case errors.Is(err, shadowrocket.ErrNoRule):
		sr.Problem = "mtvpn.base.no_rule"
	case errors.Is(err, shadowrocket.ErrTooBig):
		sr.Problem = "mtvpn.base.too_big"
	case errors.Is(err, shadowrocket.ErrNotUTF8):
		sr.Problem = "mtvpn.base.utf8"
	case errors.As(err, &he):
		sr.Problem = "HTTP " + strconv.Itoa(he.Status)
	case err != nil:
		sr.Problem = sources.ErrorText(err)
	}
	if err != nil {
		return sr, "", nil
	}
	sr.Offered, sr.Lines, sr.ListID = true, lineCount(text), listID
	same, err := store.ShadowrocketWithBase(ctx, s.d.DB.R, text)
	if err != nil {
		return sr, "", err
	}
	sr.Create = len(same) == 0
	for n := 1; ; n++ {
		name := "iphone"
		if n > 1 {
			name += "-" + strconv.Itoa(n)
		}
		taken, err := store.ShadowrocketNameTaken(ctx, s.d.DB.R, name, 0)
		if err != nil {
			return sr, "", err
		}
		if !taken {
			sr.Name = name
			break
		}
	}
	return sr, text, nil
}

func lineCount(text string) int {
	n := strings.Count(text, "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		n++
	}
	return n
}

// Get reads an import.
func (s *Service) Get(ctx context.Context, id int64) (Import, error) {
	r, err := store.GetImport(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return Import{}, ErrNotFound
	}
	if err != nil {
		return Import{}, err
	}
	return fromRow(r)
}

func fromRow(r store.Import) (Import, error) {
	im := Import{
		ID: r.ID, State: r.State, ListID: r.ListID.Int64, BaseURL: r.BaseURL, JobID: r.JobID.Int64,
		CreatedAt: r.CreatedAt.Time, FinishedAt: r.FinishedAt.Time, base: r.Base,
	}
	if r.Ignored != "" {
		im.Ignored = strings.Split(r.Ignored, ",")
	}
	if err := json.Unmarshal([]byte(r.Rows), &im.Rows); err != nil {
		return im, err
	}
	if err := json.Unmarshal([]byte(r.ListFailures), &im.ListFailures); err != nil {
		return im, err
	}
	return im, json.Unmarshal([]byte(r.Shadowrocket), &im.Shadowrocket)
}
