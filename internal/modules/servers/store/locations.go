package store

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// ErrLocationInUse is returned when deleting a location that has servers,
// retired ones included: a server's name is its location and number for good.
var ErrLocationInUse = errors.New("the location has servers")

// Location is a place servers are in. Its code names them (nl-1, nl-2…).
type Location struct {
	ID        int64   `db:"id"`
	Code      string  `db:"code"`
	Name      string  `db:"name"`
	Country   string  `db:"country"`
	Servers   int     `db:"servers"` // any lifecycle state
	CreatedAt db.Time `db:"created_at"`
}

var codePattern = regexp.MustCompile(`^[a-z]{2,5}$`)

// ValidateLocation checks the fields of a location; the code only when
// checkCode (it is immutable after creation). Messages are i18n keys.
func ValidateLocation(code, name, countryCode string, checkCode bool) FieldErrors {
	fe := FieldErrors{}
	if checkCode && !codePattern.MatchString(code) {
		fe["code"] = "locations.err.code"
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > 40 {
		fe["name"] = "locations.err.name"
	}
	if !country.Valid(countryCode) {
		fe["country"] = "locations.err.country"
	}
	if len(fe) == 0 {
		return nil
	}
	return fe
}

const locationSelect = `SELECT l.id, l.code, l.name, l.country, l.created_at,
	(SELECT count(*) FROM servers_servers s WHERE s.location_id = l.id) AS servers
	FROM servers_locations l`

// Locations lists every location by code.
func (s *Store) Locations(ctx context.Context) ([]Location, error) {
	var out []Location
	err := s.DB.R.SelectContext(ctx, &out, locationSelect+` ORDER BY l.code`)
	return out, err
}

// Location returns one location.
func (s *Store) Location(ctx context.Context, id int64) (Location, error) {
	var l Location
	return l, notFound(s.DB.R.GetContext(ctx, &l, locationSelect+` WHERE l.id = ?`, id))
}

// CreateLocation adds a location and records location.created.
func (s *Store) CreateLocation(ctx context.Context, code, name, countryCode, actor string) (int64, error) {
	name = strings.TrimSpace(name)
	if fe := ValidateLocation(code, name, countryCode, true); fe != nil {
		return 0, fe
	}
	var id int64
	err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO servers_locations (code, name, country, created_at) VALUES (?, ?, ?, ?)`,
			code, name, countryCode, db.Now())
		if unique(err, "servers_locations.code") {
			return FieldErrors{"code": "locations.err.code_taken"}
		}
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		_, err = s.Events.Record(ctx, tx, events.Event{Type: "location.created", Subject: locationSubject(id), Actor: actor,
			Payload: map[string]any{"code": code, "name": name, "country": countryCode}})
		return err
	})
	return id, err
}

// UpdateLocation changes the name and country. It records location.changed
// with the fields that changed, and nothing when none did.
func (s *Store) UpdateLocation(ctx context.Context, id int64, name, countryCode, actor string) error {
	name = strings.TrimSpace(name)
	if fe := ValidateLocation("", name, countryCode, false); fe != nil {
		return fe
	}
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var cur Location
		if err := notFound(tx.GetContext(ctx, &cur, `SELECT id, code, name, country, created_at, 0 AS servers FROM servers_locations WHERE id = ?`, id)); err != nil {
			return err
		}
		var fields []string
		if cur.Name != name {
			fields = append(fields, "name")
		}
		if cur.Country != countryCode {
			fields = append(fields, "country")
		}
		if len(fields) == 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE servers_locations SET name = ?, country = ? WHERE id = ?`, name, countryCode, id); err != nil {
			return err
		}
		_, err := s.Events.Record(ctx, tx, events.Event{Type: "location.changed", Subject: locationSubject(id), Actor: actor,
			Payload: map[string]any{"fields": fields}})
		return err
	})
}

// DeleteLocation removes a location without servers and records location.deleted.
func (s *Store) DeleteLocation(ctx context.Context, id int64, actor string) error {
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var l Location
		if err := notFound(tx.GetContext(ctx, &l, locationSelect+` WHERE l.id = ?`, id)); err != nil {
			return err
		}
		if l.Servers > 0 {
			return ErrLocationInUse
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM servers_locations WHERE id = ?`, id); err != nil {
			return err
		}
		_, err := s.Events.Record(ctx, tx, events.Event{Type: "location.deleted", Subject: locationSubject(id), Actor: actor,
			Payload: map[string]any{"code": l.Code, "name": l.Name}})
		return err
	})
}

func locationSubject(id int64) events.Subject {
	return events.Subject{Type: "location", ID: itoa(id)}
}

// ReserveNumber takes the next server number of a location, inside the
// transaction that creates the server. Numbers are never reused, not after a
// failed provisioning or a retirement; the single write connection makes the
// increment race-free.
func ReserveNumber(ctx context.Context, tx *sqlx.Tx, locationID int64) (int, error) {
	var n int
	err := tx.GetContext(ctx, &n, `UPDATE servers_locations SET last_number = last_number + 1 WHERE id = ? RETURNING last_number`, locationID)
	return n, notFound(err)
}
