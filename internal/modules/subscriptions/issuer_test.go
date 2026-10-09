package subscriptions_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/subscriptions"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
)

var errRollback = errors.New("the caller rolls back")

func TestIssueRunsInTheCallersTransaction(t *testing.T) {
	h := substest.New(t)
	ctx := context.Background()
	fam := h.Subscription("Family", 1, 2)
	port := h.Mod.LinkIssuer()

	// the caller rolls back: no link, no event
	err := h.App.DB.Write(ctx, func(tx *sqlx.Tx) error {
		l, err := port.Issue(ctx, tx, "Router — Dacha", fam, "admin")
		if err != nil || l.ID == 0 || !strings.HasPrefix(l.URL, "http") {
			t.Fatalf("issue: %+v %v", l, err)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatal(err)
	}
	if rows, _ := port.Links(ctx); len(rows) != 0 {
		t.Fatalf("a rolled-back link: %+v", rows)
	}
	if ev := h.Events("link.created"); len(ev) != 0 {
		t.Fatalf("a rolled-back event: %+v", ev)
	}

	// committed: active, no expiry, the link language, the admin as actor
	var got subscriptions.IssuedLink
	if err := h.App.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		got, err = port.Issue(ctx, tx, " Router — Dacha ", fam, "admin")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Router — Dacha" || got.Subscription != "Family" || got.State != "active" {
		t.Fatalf("issued: %+v", got)
	}
	l, err := h.Mod.Links.Get(ctx, got.ID)
	if err != nil || l.State != "active" || !l.Expires.IsZero() || l.Lang != "ru" || l.SubscriptionID != fam {
		t.Fatalf("link: %+v %v", l, err)
	}
	token, _ := h.Mod.Links.Token(ctx, got.ID)
	if got.URL != h.Mod.Links.URL(token) {
		t.Fatalf("URL %q", got.URL)
	}
	ev := h.Events("link.created")
	if len(ev) != 1 || ev[0].Actor != "admin" || ev[0].Payload["subscription"] != "Family" || ev[0].Payload["expiry"] != "" {
		t.Fatalf("link.created: %+v", ev)
	}

	// a taken name and a missing subscription: both errors, nothing written
	err = h.App.DB.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := port.Issue(ctx, tx, "Router — Dacha", 999, "admin")
		return err
	})
	var fe subscriptions.FieldErrors
	if !errors.As(err, &fe) || fe["name"] != "links.err.name_taken" || fe["subscription"] != "links.err.subscription" {
		t.Fatalf("field errors: %v", err)
	}
	if rows, _ := port.Links(ctx); len(rows) != 1 || len(h.Events("link.created")) != 1 {
		t.Fatalf("something was written: %+v", rows)
	}
}

func TestIssuerReads(t *testing.T) {
	h := substest.New(t)
	ctx := context.Background()
	me := h.Subscription("Me", 1, 2)
	fam := h.Subscription("Family", 1)
	port := h.Mod.LinkIssuer()

	subs, err := port.Subscriptions(ctx)
	if err != nil || len(subs) != 2 || subs[0].Name != "Family" || subs[0].Servers != 1 || subs[1].Name != "Me" || subs[1].Servers != 2 || subs[1].ID != me {
		t.Fatalf("subscriptions: %+v %v", subs, err)
	}

	parents, token := h.Link(fam, "Router — Parents")
	dacha, _ := h.Link(me, "Router — Dacha")
	gone, _ := h.Link(me, "Old")
	if err := h.Mod.Links.Disable(ctx, dacha, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Links.Delete(ctx, gone, "admin"); err != nil {
		t.Fatal(err)
	}
	soon, _ := h.Link(me, "Soon")
	if err := h.Mod.Links.SetExpiry(ctx, soon, h.Now.Add(time.Hour), "admin"); err != nil {
		t.Fatal(err)
	}
	h.Advance(2 * time.Hour)

	list, err := port.Links(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("links: %+v %v", list, err)
	}
	want := []subscriptions.IssuedLink{
		{ID: dacha, Name: "Router — Dacha", Subscription: "Me", State: "disabled"},
		{ID: parents, Name: "Router — Parents", Subscription: "Family", State: "active"},
		{ID: soon, Name: "Soon", Subscription: "Me", State: "expired"},
	}
	for i := range want {
		if list[i] != want[i] {
			t.Errorf("link %d: %+v, want %+v", i, list[i], want[i])
		}
	}

	l, err := port.Link(ctx, parents)
	if err != nil || l.URL != h.Mod.Links.URL(token) || l.State != "active" || l.Subscription != "Family" {
		t.Fatalf("link: %+v %v", l, err)
	}
	if l, err := port.Link(ctx, gone); err != nil || l.URL != "" || l.State != "deleted" || l.Name != "Old" {
		t.Fatalf("deleted: %+v %v", l, err)
	}
	if _, err := port.Link(ctx, 999); !errors.Is(err, subscriptions.ErrLinkNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}
