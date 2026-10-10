package store_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/storetest"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// A number is taken by the server that has it, whatever became of the server,
// so it is never given again; each prefix counts for itself, and a server
// named before servers had a country (ams-1 in NL) counts for its own prefix.
func TestNextNumberNeverReuses(t *testing.T) {
	e := storetest.New(t)
	ctx := context.Background()
	ips := 0
	add := func(country, prefix string) string {
		t.Helper()
		ips++
		var name string
		err := e.DB.Write(ctx, func(tx *sqlx.Tx) error {
			n, err := store.NextNumber(ctx, tx, prefix)
			if err != nil {
				return err
			}
			name = prefix + "-" + itoa(n)
			_, err = store.InsertServer(ctx, tx, store.NewServer{Country: country, Number: n, Name: name, IP: "203.0.113." + itoa(100+ips),
				ManagementHostname: name, ProxyHostname: name, TemplateID: 1, TemplateVersion: 1, Params: "{}"}, db.Now(), nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return name
	}
	e.AddServer("tpl-0", "retired") // makes the template; its name has no prefix
	for _, want := range []string{"nl-1", "nl-2"} {
		if got := add("NL", "nl"); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
	if _, err := e.DB.W.ExecContext(ctx, `UPDATE servers_servers SET state = 'retired', retired_at = ? WHERE name = 'nl-2'`, db.Now()); err != nil {
		t.Fatal(err)
	}
	if got := add("NL", "nl"); got != "nl-3" {
		t.Errorf("after nl-2 retired: %s, want nl-3", got)
	}
	if got := add("NL", "ams"); got != "ams-1" {
		t.Errorf("another prefix: %s, want ams-1", got)
	}
	if got := add("DE", "de"); got != "de-1" {
		t.Errorf("another country: %s, want de-1", got)
	}
	// a transaction that fails after reading the number never took it
	err := e.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := store.NextNumber(ctx, tx, "nl"); err != nil {
			return err
		}
		return errors.New("the server could not be created")
	})
	if err == nil {
		t.Fatal("the write should have failed")
	}
	if n, err := store.NextNumber(ctx, e.DB.R, "nl"); err != nil || n != 4 {
		t.Errorf("next nl: %d %v, want 4", n, err)
	}
}

func TestPrefixIsTheNameBeforeTheNumber(t *testing.T) {
	for _, s := range []store.Server{{Name: "nl-2", Number: 2}, {Name: "ams-14", Number: 14}} {
		if got := s.Prefix(); got != s.Name[:len(s.Name)-len(itoa(s.Number))-1] {
			t.Errorf("%s: %q", s.Name, got)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
