package store_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/storetest"
)

func TestLocationValidation(t *testing.T) {
	e := storetest.New(t)
	ctx := context.Background()
	for _, c := range []struct {
		code, country string
		field         string // the field refused, "" for accepted
	}{
		{"nl", "NL", ""},
		{"abcde", "DE", ""}, // five letters is the limit
		{"n", "NL", "code"},
		{"nlnlnl", "NL", "code"},
		{"NL", "NL", "code"},
		{"n1", "NL", "code"},
		{"nl", "XX", "country"}, // private-use
		{"nl", "nl", "country"}, // upper case only
		{"nl", "ZZ", "country"},
		{"nl", "", "country"},
		{"nl", "EU", "country"}, // a region, not a country
	} {
		_, err := e.Store.CreateLocation(ctx, c.code, "Name", c.country, "admin")
		var fe store.FieldErrors
		switch {
		case c.field == "" && c.code == "nl" && err != nil && errors.As(err, &fe) && fe["code"] == "locations.err.code_taken":
			// the first nl was made in an earlier row; a second one is refused as taken
		case c.field == "" && err != nil:
			t.Errorf("%s/%s refused: %v", c.code, c.country, err)
		case c.field != "" && (!errors.As(err, &fe) || fe[c.field] == ""):
			t.Errorf("%s/%s must be refused on %s, got %v", c.code, c.country, c.field, err)
		}
	}
	// the name: 1–40 characters, counted in characters
	for i, c := range []struct {
		name string
		ok   bool
	}{
		{"", false}, {"   ", false}, {"Ä", true}, {"Нидерланды", true},
		{"0123456789012345678901234567890123456789", true},
		{"01234567890123456789012345678901234567890", false},
	} {
		_, err := e.Store.CreateLocation(ctx, "n"+string(rune('a'+i))+"x", c.name, "NL", "admin")
		if (err == nil) != c.ok {
			t.Errorf("name %q: accepted=%v, want %v (%v)", c.name, err == nil, c.ok, err)
		}
	}
	// a duplicate code
	if _, err := e.Store.CreateLocation(ctx, "de", "Germany", "DE", "admin"); err != nil {
		t.Fatal(err)
	}
	_, err := e.Store.CreateLocation(ctx, "de", "Germany again", "DE", "admin")
	var fe store.FieldErrors
	if !errors.As(err, &fe) || fe["code"] != "locations.err.code_taken" {
		t.Errorf("a duplicate code: %v", err)
	}
	// a refusal records nothing
	before := e.AllRecorded()
	_, _ = e.Store.CreateLocation(ctx, "x", "Name", "NL", "admin")
	if e.AllRecorded() != before {
		t.Error("a refused create recorded an event")
	}
}

func TestLocationEvents(t *testing.T) {
	e := storetest.New(t)
	ctx := context.Background()
	id, err := e.Store.CreateLocation(ctx, "nl", "Netherlands", "NL", "admin")
	if err != nil {
		t.Fatal(err)
	}
	created := e.Recorded("location.created")
	if len(created) != 1 || created[0].Subject.Type != "location" || created[0].Subject.ID != strconv.FormatInt(id, 10) ||
		created[0].Actor != "admin" || created[0].Payload["code"] != "nl" {
		t.Fatalf("created: %+v", created)
	}
	// an edit that changes nothing records nothing
	if err := e.Store.UpdateLocation(ctx, id, "Netherlands", "NL", "admin"); err != nil {
		t.Fatal(err)
	}
	if n := len(e.Recorded("location.changed")); n != 0 {
		t.Fatalf("a no-op edit recorded %d events", n)
	}
	// only the name
	if err := e.Store.UpdateLocation(ctx, id, "  Holland ", "NL", "admin"); err != nil {
		t.Fatal(err)
	}
	// both
	if err := e.Store.UpdateLocation(ctx, id, "Germany", "DE", "admin"); err != nil {
		t.Fatal(err)
	}
	ch := e.Recorded("location.changed")
	if len(ch) != 2 {
		t.Fatalf("changed events: %+v", ch)
	}
	if f := ch[1].Payload["fields"]; len(f.([]any)) != 1 || f.([]any)[0] != "name" {
		t.Errorf("first edit fields: %v", f)
	}
	if f := ch[0].Payload["fields"]; len(f.([]any)) != 2 {
		t.Errorf("second edit fields: %v", f)
	}
	l, _ := e.Store.Location(ctx, id)
	if l.Name != "Germany" || l.Country != "DE" || l.Code != "nl" {
		t.Errorf("stored: %+v (the code never changes)", l)
	}
	// a refused edit changes and records nothing
	before := e.AllRecorded()
	if err := e.Store.UpdateLocation(ctx, id, "", "DE", "admin"); err == nil {
		t.Error("an empty name must be refused")
	}
	if e.AllRecorded() != before {
		t.Error("a refused edit recorded an event")
	}
	if err := e.Store.UpdateLocation(ctx, 999, "A", "DE", "admin"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	// delete
	if err := e.Store.DeleteLocation(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if d := e.Recorded("location.deleted"); len(d) != 1 || d[0].Payload["code"] != "nl" {
		t.Errorf("deleted: %+v", d)
	}
	if err := e.Store.DeleteLocation(ctx, id, "admin"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

// addServer inserts a server row directly: servers are built in 1d.
func addServer(t *testing.T, e *storetest.Env, locationID int64, number int, state string) {
	t.Helper()
	ctx := context.Background()
	_, err := e.DB.W.ExecContext(ctx, `INSERT OR IGNORE INTO servers_templates (id, slug, name, created_at) VALUES (1, 't', 'T', '2026-10-07T00:00:00.000Z')`)
	if err != nil {
		t.Fatal(err)
	}
	retired := any(nil)
	if state == "retired" {
		retired = "2026-10-07T00:00:00.000Z"
	}
	_, err = e.DB.W.ExecContext(ctx, `INSERT INTO servers_servers (location_id, number, name, ip, management_hostname, proxy_hostname, state, template_id, template_version, created_at, retired_at)
		VALUES (?, ?, ?, ?, 'h', 'h', ?, 1, 1, '2026-10-07T00:00:00.000Z', ?)`,
		locationID, number, "s"+string(rune('0'+number)), "203.0.113."+string(rune('0'+number)), state, retired)
	if err != nil {
		t.Fatal(err)
	}
}

func TestLocationWithServersCannotBeDeleted(t *testing.T) {
	e := storetest.New(t)
	ctx := context.Background()
	id, _ := e.Store.CreateLocation(ctx, "nl", "Netherlands", "NL", "admin")
	addServer(t, e, id, 1, "retired")
	if err := e.Store.DeleteLocation(ctx, id, "admin"); !errors.Is(err, store.ErrLocationInUse) {
		t.Fatalf("a location with a retired server: %v", err)
	}
	l, err := e.Store.Location(ctx, id)
	if err != nil || l.Servers != 1 {
		t.Errorf("the location must stay with its server: %+v %v", l, err)
	}
	if n := len(e.Recorded("location.deleted")); n != 0 {
		t.Errorf("a refused delete recorded %d events", n)
	}
	// another location without servers can go
	other, _ := e.Store.CreateLocation(ctx, "de", "Germany", "DE", "admin")
	if err := e.Store.DeleteLocation(ctx, other, "admin"); err != nil {
		t.Errorf("an empty location: %v", err)
	}
}

func TestReserveNumberNeverReuses(t *testing.T) {
	e := storetest.New(t)
	ctx := context.Background()
	a, _ := e.Store.CreateLocation(ctx, "nl", "Netherlands", "NL", "admin")
	b, _ := e.Store.CreateLocation(ctx, "de", "Germany", "DE", "admin")
	reserve := func(loc int64) (n int) {
		t.Helper()
		err := e.DB.Write(ctx, func(tx *sqlx.Tx) (err error) {
			n, err = store.ReserveNumber(ctx, tx, loc)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for want := 1; want <= 3; want++ {
		if got := reserve(a); got != want {
			t.Errorf("nl number %d, want %d", got, want)
		}
	}
	// another location counts for itself
	if got := reserve(b); got != 1 {
		t.Errorf("de number %d, want 1", got)
	}
	// a transaction that fails after reserving gives the number back (it was
	// never used), but one that commits keeps it for good, even if its server
	// never came to be
	err := e.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := store.ReserveNumber(ctx, tx, a); err != nil {
			return err
		}
		return errors.New("the server could not be created")
	})
	if err == nil {
		t.Fatal("the write should have failed")
	}
	if got := reserve(a); got != 4 {
		t.Errorf("after a rolled-back reservation: %d, want 4", got)
	}
	if err := e.DB.Write(ctx, func(tx *sqlx.Tx) error { _, err := store.ReserveNumber(ctx, tx, 999); return err }); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown location: %v", err)
	}
}
