package templates_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/storetest"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
)

type env struct {
	*storetest.Env
	Svc *templates.Service
	Now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{Env: storetest.New(t), Now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	e.Svc = templates.New(e.DB, e.Events, e.Log)
	e.Svc.Now = func() time.Time { return e.Now }
	return e
}

// create makes a template (its draft holds the skeleton).
func (e *env) create(slug string) int64 {
	e.T.Helper()
	id, err := e.Svc.Create(context.Background(), "Template "+slug, slug, "about "+slug, "admin")
	if err != nil {
		e.T.Fatal(err)
	}
	return id
}

// publish saves a visible change to the draft and publishes it.
func (e *env) publish(id int64, note string) int {
	e.T.Helper()
	ctx := context.Background()
	d, err := e.Svc.EditDraft(ctx, id, "admin")
	if err != nil {
		e.T.Fatal(err)
	}
	files := map[string][]byte{}
	for p, c := range d.Files {
		files[p] = c
	}
	files["compose.yaml"] = append(append([]byte(nil), files["compose.yaml"]...), []byte("# "+note+"\n")...)
	rev, err := e.Svc.SaveDraft(ctx, id, d.Revision, files, "admin", "admin")
	if err != nil {
		e.T.Fatal(err)
	}
	v, err := e.Svc.Publish(ctx, id, rev, note, false, "admin")
	if err != nil {
		e.T.Fatal(err)
	}
	return v
}

// addServer inserts a server row built from version v of template id.
func (e *env) addServer(id int64, v int, state string) {
	e.T.Helper()
	ctx := context.Background()
	if _, err := e.Store.CreateLocation(ctx, "nl", "Netherlands", "NL", "admin"); err != nil && !strings.Contains(err.Error(), "invalid") {
		e.T.Fatal(err)
	}
	var loc int64
	if err := e.DB.R.GetContext(ctx, &loc, `SELECT id FROM servers_locations WHERE code = 'nl'`); err != nil {
		e.T.Fatal(err)
	}
	var n int
	_ = e.DB.R.GetContext(ctx, &n, `SELECT count(*) FROM servers_servers`)
	n++
	retired := any(nil)
	var health any
	switch state {
	case "retired":
		retired = "2026-10-07T00:00:00.000Z"
	case "active":
		health = "healthy"
	}
	if _, err := e.DB.W.ExecContext(ctx, `INSERT INTO servers_servers (location_id, number, name, ip, management_hostname, proxy_hostname, state, health, template_id, template_version, created_at, retired_at)
		VALUES (?, ?, ?, ?, 'h', 'h', ?, ?, ?, ?, '2026-10-07T00:00:00.000Z', ?)`,
		loc, n, fmt.Sprintf("nl-%d", n), fmt.Sprintf("203.0.113.%d", n), state, health, id, v, retired); err != nil {
		e.T.Fatal(err)
	}
}
