package templates_test

import (
	"context"
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
