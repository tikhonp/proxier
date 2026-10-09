package generations

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

// FetchURLLifetime is how long a fetch URL works: the process doc's security
// rule, not a setting.
const FetchURLLifetime = time.Hour

// FetchURL states.
const (
	FetchWaiting  = "waiting"
	FetchUsed     = "used"
	FetchExpired  = "expired"
	FetchReplaced = "replaced"
)

// FetchURL is one single-use URL of a generation, without its token.
type FetchURL struct {
	ID        int64
	State     string // waiting, used, expired, replaced
	CreatedAt time.Time
	CreatedBy string
	ExpiresAt time.Time
	EndedAt   time.Time
	IP        string
	UserAgent string
}

// Live reports whether the URL still works at now: waiting and within its
// hour (the scan may not have seen an expired one yet).
func (u FetchURL) Live(now time.Time) bool { return u.State == FetchWaiting && now.Before(u.ExpiresAt) }

func fetchURLOf(r store.FetchURL) FetchURL {
	return FetchURL{ID: r.ID, State: r.State, CreatedAt: r.CreatedAt.Time, CreatedBy: r.CreatedBy, ExpiresAt: r.ExpiresAt.Time,
		EndedAt: r.EndedAt.Time, IP: r.IP, UserAgent: r.UserAgent}
}

func tokenAAD(id int64) string { return "fetch_url:" + strconv.FormatInt(id, 10) + ":token" }

// URL is a fetch URL's address: <base>/f/<token>.
func (s *Service) URL(token string) string { return s.base() + "/f/" + token }

// MaskedURL is the address with its token hidden.
func (s *Service) MaskedURL() string { return s.base() + "/f/••••••••" }

func (s *Service) base() string {
	if s.d.BaseURL == nil {
		return ""
	}
	return s.d.BaseURL.String()
}

// CreateFetchURL gives a generation a new fetch URL, working once for
// FetchURLLifetime, and ends the one that waited: replaced while it still
// worked, expired (with its event) when its hour had passed unseen by the
// scan. It returns the URL.
func (s *Service) CreateFetchURL(ctx context.Context, generationID int64, actor string) (FetchURL, string, error) {
	if _, err := s.row(ctx, generationID); err != nil {
		return FetchURL{}, "", err
	}
	now := db.At(s.d.Now())
	token := vault.NewToken()
	out := FetchURL{State: FetchWaiting, CreatedAt: now.Time, CreatedBy: actor, ExpiresAt: now.Add(FetchURLLifetime)}
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		replaced := false
		old, err := store.WaitingFetchURL(ctx, tx, generationID)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return err
		case now.Before(old.ExpiresAt.Time):
			replaced = true
			if err := store.EndFetchURL(ctx, tx, old.ID, FetchReplaced, now); err != nil {
				return err
			}
		default:
			if err := s.expire(ctx, tx, old, now, actor); err != nil {
				return err
			}
		}
		id, err := store.InsertFetchURL(ctx, tx, store.FetchURL{GenerationID: generationID, CreatedAt: now, CreatedBy: actor,
			ExpiresAt: db.At(out.ExpiresAt)}, s.d.Vault.Lookup(token))
		if err != nil {
			return err
		}
		out.ID = id
		if err := store.SetFetchToken(ctx, tx, id, s.d.Vault.SealString(token, tokenAAD(id))); err != nil {
			return err
		}
		_, err = s.d.Events.Record(ctx, tx, events.Event{Time: now, Type: "routerscript.fetch_url_created", Subject: Subject(generationID),
			Actor: actor, Payload: map[string]any{"expires": timeText(db.At(out.ExpiresAt)), "replaced": replaced}})
		return err
	})
	if err != nil {
		return FetchURL{}, "", err
	}
	return out, s.URL(token), nil
}

// expire ends a URL whose hour passed unused and records it.
func (s *Service) expire(ctx context.Context, tx *sqlx.Tx, u store.FetchURL, now db.Time, actor string) error {
	if err := store.EndFetchURL(ctx, tx, u.ID, FetchExpired, now); err != nil {
		return err
	}
	_, err := s.d.Events.Record(ctx, tx, events.Event{Time: now, Type: "routerscript.fetch_url_expired", Subject: Subject(u.GenerationID),
		Actor: actor, Payload: map[string]any{"expired": timeText(u.ExpiresAt)}})
	return err
}

// Live is the generation's waiting URL with its address; false when none
// waits or its hour has passed.
func (s *Service) Live(ctx context.Context, generationID int64) (FetchURL, string, bool, error) {
	r, err := store.WaitingFetchURL(ctx, s.d.DB.R, generationID)
	if errors.Is(err, store.ErrNotFound) {
		return FetchURL{}, "", false, nil
	}
	if err != nil {
		return FetchURL{}, "", false, err
	}
	u := fetchURLOf(r)
	if !u.Live(s.d.Now()) {
		return FetchURL{}, "", false, nil
	}
	blob, err := store.FetchToken(ctx, s.d.DB.R, r.ID)
	if errors.Is(err, store.ErrNotFound) {
		return FetchURL{}, "", false, nil // used between the two reads
	}
	if err != nil {
		return FetchURL{}, "", false, err
	}
	token, err := s.d.Vault.OpenString(blob, tokenAAD(r.ID))
	if err != nil {
		return FetchURL{}, "", false, err
	}
	return u, s.URL(token), true, nil
}

// FetchURLs lists a generation's fetch URLs, newest first.
func (s *Service) FetchURLs(ctx context.Context, generationID int64) ([]FetchURL, error) {
	rows, err := store.FetchURLs(ctx, s.d.DB.R, generationID)
	if err != nil {
		return nil, err
	}
	out := make([]FetchURL, 0, len(rows))
	for _, r := range rows {
		out = append(out, fetchURLOf(r))
	}
	return out, nil
}

// timeText is a time as events carry it (Phase 2's rule): the stored form.
func timeText(t db.Time) string { return t.UTC().Format(db.TimeLayout) }
