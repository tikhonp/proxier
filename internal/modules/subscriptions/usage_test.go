package subscriptions_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
)

func TestUsage(t *testing.T) {
	h := substest.New(t)
	h.Catalog.Put(substest.Server(3, "fi-1", "🇫🇮", "Finland", 1))
	family := h.Subscription("Family", 1, 2)
	friends := h.Subscription("Friends", 1)
	h.Subscription("Alone", 2)
	ts := func(d time.Duration) string { return h.Now.Add(d).UTC().Format("2006-01-02T15:04:05.000Z") }
	ins := `INSERT INTO subs_links (subscription_id, name, state, language, created_at, expires_at, deleted_at) VALUES (?, ?, ?, 'ru', ?, ?, ?)`
	h.Exec(ins, family, "Mom", "active", ts(0), nil, nil)
	h.Exec(ins, family, "Dad", "active", ts(0), ts(time.Hour), nil)    // expires later: counts
	h.Exec(ins, family, "Old", "active", ts(0), ts(-time.Hour), nil)   // expired
	h.Exec(ins, friends, "Bob", "disabled", ts(0), nil, nil)           // disabled
	h.Exec(ins, friends, "Eve", "deleted", ts(0), nil, ts(-time.Hour)) // deleted
	h.Exec(ins, friends, "Ann", "active", ts(0), nil, nil)

	u := h.Mod.Usage()
	names, links, err := u.Usage(context.Background(), 1)
	if err != nil || strings.Join(names, ",") != "Family,Friends" || links != 3 {
		t.Fatalf("nl-1: %v %d %v", names, links, err)
	}
	names, links, err = u.Usage(context.Background(), 3)
	if err != nil || len(names) != 0 || links != 0 {
		t.Fatalf("fi-1: %v %d %v", names, links, err)
	}
	// the expiry moment itself is expired
	h.Advance(time.Hour)
	if _, links, _ := u.Usage(context.Background(), 1); links != 2 {
		t.Errorf("at Dad's expiry: %d links", links)
	}
}
