// Package jobs is the platform's job system (docs/processes/platform/jobs.md):
// every long or remote piece of work is a stored job that runs in a worker
// pool of its queue, step by step, with a live log. Jobs survive a restart,
// can be cancelled and retried, and never overlap on one resource key.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

// Queue is a worker pool's name.
type Queue string

const (
	Provisioning Queue = "provisioning"
	Checks       Queue = "checks"
	Routers      Queue = "routers"
	Refresh      Queue = "refresh"
	Discovery    Queue = "discovery"
	Notify       Queue = "notify"
	Maintenance  Queue = "maintenance"
)

// Concurrency is how many jobs each queue runs at once (architecture.md#jobs).
var Concurrency = map[Queue]int{
	Provisioning: 2, Checks: 8, Routers: 2, Refresh: 2, Discovery: 1, Notify: 1, Maintenance: 1,
}

// State is where a job is in its life.
type State string

const (
	Queued      State = "queued"
	Running     State = "running"
	Succeeded   State = "succeeded"
	Failed      State = "failed"
	Cancelled   State = "cancelled"
	Interrupted State = "interrupted"
)

// Type is a kind of job a module registers at startup.
type Type struct {
	// Name is "<module>.<what>", e.g. "servers.provision"; the prefix must be
	// the registering module's name.
	Name        string
	Queue       Queue
	MaxAttempts int             // at least 1
	Backoff     []time.Duration // wait before attempt 2, 3, …; the last repeats
	Steps       []Step          // at least one, unique names
	// Merge combines a coalesced request's payload into the queued job's. Nil
	// is a shallow JSON-object merge where the incoming keys win.
	Merge func(queued, incoming json.RawMessage) (json.RawMessage, error)
	// OnFailed records the module's own failure event, in the transaction that
	// marks the job failed. Nil: the platform records job.failed.
	OnFailed func(ctx context.Context, tx *sqlx.Tx, j Info, err error) error
	// OnCancelled updates the module's state in the transaction that marks the
	// job cancelled (a provisioning server becomes failed).
	OnCancelled func(ctx context.Context, tx *sqlx.Tx, j Info, by string) error
	// NoResume fails the job after a restart instead of resuming it.
	NoResume bool
	// Quiet marks high-frequency work (check rounds): a clean success is
	// deleted after QuietRetention.
	Quiet bool
}

// Step is one named, idempotent part of a job. Its title is the message
// "job.<type>.step.<name>".
type Step struct {
	Name string // stable; stored and matched on resume, e.g. "install-docker"
	Run  func(ctx context.Context, r *Run) error
}

// Info is what a job's callbacks learn about it.
type Info struct {
	ID          int64
	Type        string
	Queue       Queue
	Attempt     int // 1-based while running
	MaxAttempts int
	ResourceKey string
	Subject     events.Subject
	CreatedBy   string
	RetryOf     int64
}

// Actor is the event actor for work done by this job: "job:<id>".
func (i Info) Actor() string { return events.JobActor(i.ID) }

// Request asks for a job.
type Request struct {
	Type          string
	Payload       any               // marshalled to a JSON object; never secrets
	Secrets       map[string]string // sealed; registered for redaction
	ResourceKey   string
	CoalescingKey string // unique within the type; any string
	Delay         time.Duration
	Subject       events.Subject // default: parsed from ResourceKey ("server:12")
	CreatedBy     string         // required
	SkipIfBusy    bool
}

// QuietRetention is how long a clean quiet job is kept.
const QuietRetention = 24 * time.Hour

// Enqueued is the answer to a request.
type Enqueued struct {
	ID     int64
	Merged bool // merged into an existing queued job
}

var (
	ErrSkipped         = errors.New("jobs: skipped, the resource is busy")
	ErrCancelled       = errors.New("jobs: cancelled")
	ErrUnknownType     = errors.New("jobs: unknown job type")
	ErrPayloadTooLarge = errors.New("jobs: payload over 64 KiB")
	ErrNotRetryable    = errors.New("jobs: only failed or cancelled jobs can be retried")
	ErrNotActive       = errors.New("jobs: the job has already finished")
	ErrNotFound        = errors.New("jobs: no such job")
)

// MaxPayload is the largest payload, in bytes.
const MaxPayload = 64 << 10

type permanent struct{ err error }

func (p permanent) Error() string { return p.err.Error() }
func (p permanent) Unwrap() error { return p.err }

// Permanent marks err as not worth another attempt.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanent{err}
}

// IsPermanent reports whether err, or an error it wraps, was marked Permanent.
func IsPermanent(err error) bool {
	var p permanent
	return errors.As(err, &p)
}

type deferral struct {
	d      time.Duration
	reason string
}

func (d deferral) Error() string { return fmt.Sprintf("deferred for %s: %s", d.d, d.reason) }

// Defer queues the job again after d without counting the attempt.
func Defer(d time.Duration, reason string) error { return deferral{d, reason} }

// System owns the queues, workers and scheduler of one process.
type System struct {
	// Now is the clock; tests replace it.
	Now func() time.Time
	// Grace is how long running steps get to finish on shutdown.
	Grace time.Duration
	// Poll is how often an idle pool looks for work.
	Poll time.Duration
	// SchedulerPoll is how often the scheduler looks for due schedules.
	SchedulerPoll time.Duration

	d      *db.DB
	v      *vault.Vault
	ev     *events.Catalog
	st     *settings.Store
	log    *slog.Logger
	bootID string
	hub    *hub
	kickc  chan struct{}

	mu        sync.Mutex
	types     map[string]Type
	schedules map[string]Schedule
	schedList []string
	running   map[int64]*Run // jobs this boot runs now
	ticks     map[string]time.Time
	started   bool
	jitter    func(max time.Duration) time.Duration
}

// New builds a System. Register types before Start.
func New(d *db.DB, v *vault.Vault, ev *events.Catalog, st *settings.Store, log *slog.Logger) *System {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return &System{
		Now: time.Now, Grace: 10 * time.Second, Poll: 500 * time.Millisecond, SchedulerPoll: 5 * time.Second,
		d: d, v: v, ev: ev, st: st, log: log, bootID: hex.EncodeToString(b), hub: newHub(), kickc: make(chan struct{}, 1),
		types: map[string]Type{}, schedules: map[string]Schedule{}, running: map[int64]*Run{},
		ticks: map[string]time.Time{},
		jitter: func(max time.Duration) time.Duration {
			if max <= 0 {
				return 0
			}
			var b [8]byte
			_, _ = rand.Read(b[:])
			n := int64(0)
			for _, x := range b {
				n = n<<8 | int64(x)
			}
			if n < 0 {
				n = -n
			}
			return time.Duration(n % int64(max))
		},
	}
}

func (s *System) now() db.Time { return db.At(s.Now()) }

// Register adds a module's job types.
func (s *System) Register(module string, types ...Type) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, t := range types {
		names := map[string]bool{}
		switch {
		case !strings.HasPrefix(t.Name, module+"."):
			errs = append(errs, fmt.Errorf("job type %q must start with %q", t.Name, module+"."))
		case s.types[t.Name].Name != "":
			errs = append(errs, fmt.Errorf("job type %q registered twice", t.Name))
		case Concurrency[t.Queue] == 0:
			errs = append(errs, fmt.Errorf("job type %q: unknown queue %q", t.Name, t.Queue))
		case t.MaxAttempts < 1:
			errs = append(errs, fmt.Errorf("job type %q: MaxAttempts must be at least 1", t.Name))
		case len(t.Steps) == 0:
			errs = append(errs, fmt.Errorf("job type %q has no steps", t.Name))
		default:
			bad := false
			for _, st := range t.Steps {
				if st.Name == "" || st.Run == nil || names[st.Name] {
					errs = append(errs, fmt.Errorf("job type %q: step %q is empty or repeated", t.Name, st.Name))
					bad = true
					break
				}
				names[st.Name] = true
			}
			if !bad {
				s.types[t.Name] = t
			}
		}
	}
	return errors.Join(errs...)
}

func (s *System) typeOf(name string) (Type, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.types[name]
	return t, ok
}

// Actor names used in logs and Created-by columns.
func scheduleActor(name string) string { return "schedule:" + name }

func parseResourceKey(k string) events.Subject {
	typ, id, ok := strings.Cut(k, ":")
	if !ok {
		return events.Subject{}
	}
	return events.Subject{Type: typ, ID: id}
}

func secretAAD(id int64) string { return fmt.Sprintf("job:%d:payload", id) }

func (s *System) sealSecrets(id int64, m map[string]string) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return s.v.Seal(b, secretAAD(id)), nil
}

func (s *System) openSecrets(id int64, blob []byte) (map[string]string, error) {
	m := map[string]string{}
	if len(blob) == 0 {
		return m, nil
	}
	b, err := s.v.Open(blob, secretAAD(id))
	if err != nil {
		return nil, fmt.Errorf("jobs: open secrets of job %d: %w", id, err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
