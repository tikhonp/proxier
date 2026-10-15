package pages_test

import (
	"context"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

// thingModule owns things: member 2 owns thing:5, member 1 thing:6.
type thingModule struct{}

func (thingModule) Name() string      { return "things" }
func (thingModule) Migrations() fs.FS { return nil }
func (thingModule) EventTypes() []events.Type {
	return []events.Type{{Name: "thing.made", Module: "things", Description: "A thing was made."}}
}
func (thingModule) OwnedSubjects(_ context.Context, member int64) ([]events.Subject, error) {
	switch member {
	case 2:
		return []events.Subject{{Type: "thing", ID: "5"}}, nil
	case 1:
		return []events.Subject{{Type: "thing", ID: "6"}}, nil
	}
	return nil, nil
}

// At activity: own a member sees the events and jobs they caused or whose
// subject is theirs, and nobody else's; another member's job by URL is 404.
// At all, everything.
func TestActivityAndJobsAtOwnShowOnlyTheMembers(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{Modules: []module.Module{thingModule{}}})
	anna := s.Join("anna", access.Grant{}.With(access.Activity, access.LevelOwn), "") // member 2
	ctx := t.Context()
	record := func(actor, subject string, id int) {
		t.Helper()
		typ, sid, _ := strings.Cut(subject, ":")
		err := s.App.DB.Write(ctx, func(tx *sqlx.Tx) error {
			_, err := s.App.Events.Record(ctx, tx, events.Event{Time: db.At(s.Now), Type: "thing.made", Actor: actor,
				Subject: events.Subject{Type: typ, ID: sid}, Payload: map[string]any{"n": id}})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	record("member:2", "server:1", 101) // anna did it
	record("system", "thing:5", 102)    // anna's thing
	record("member:1", "thing:6", 103)  // neither
	record("admin", "thing:7", 104)     // member 1 before members

	enqueue := func(by, subject string) int64 {
		t.Helper()
		typ, id, _ := strings.Cut(subject, ":")
		e, err := s.App.Jobs.EnqueueNow(ctx, jobs.Request{Type: "platform.demo", CreatedBy: by, Delay: time.Hour,
			Subject: events.Subject{Type: typ, ID: id}})
		if err != nil {
			t.Fatal(err)
		}
		return e.ID
	}
	mine := enqueue("member:2", "server:1")
	ofMyThing := enqueue("system", "thing:5")
	notMine := enqueue("member:1", "thing:6")

	page := anna.Get("/activity").Body.String()
	for _, want := range []string{"server:1", "thing:5"} {
		if !strings.Contains(page, want) {
			t.Errorf("anna's Activity lacks %s", want)
		}
	}
	for _, other := range []string{"thing:6", "thing:7"} {
		if strings.Contains(page, other) {
			t.Errorf("anna's Activity shows %s", other)
		}
	}
	jobsPage := anna.Get("/jobs").Body.String()
	for _, id := range []int64{mine, ofMyThing} {
		if !strings.Contains(jobsPage, `href="/jobs/`+strconv.FormatInt(id, 10)+`"`) {
			t.Errorf("anna's Jobs lacks #%d", id)
		}
	}
	if strings.Contains(jobsPage, `href="/jobs/`+strconv.FormatInt(notMine, 10)+`"`) {
		t.Error("anna's Jobs shows another member's job")
	}
	for _, path := range []string{"", "/log.txt"} {
		if rec := anna.Get("/jobs/" + strconv.FormatInt(notMine, 10) + path); rec.Code != http.StatusNotFound {
			t.Errorf("another member's job%s by URL: %d, want 404", path, rec.Code)
		}
	}
	if rec := anna.Get("/jobs/" + strconv.FormatInt(ofMyThing, 10)); rec.Code != http.StatusOK {
		t.Errorf("the job of anna's thing: %d", rec.Code)
	}

	owner := s.SignIn("")
	all := owner.Get("/activity").Body.String()
	for _, want := range []string{"server:1", "thing:5", "thing:6", "thing:7"} {
		if !strings.Contains(all, want) {
			t.Errorf("activity: all lacks %s", want)
		}
	}
	if !strings.Contains(owner.Get("/jobs").Body.String(), `href="/jobs/`+strconv.FormatInt(notMine, 10)+`"`) {
		t.Error("activity: all lacks a job")
	}
}
