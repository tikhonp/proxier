package services_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

func TestPasteNormalises(t *testing.T) {
	h := routingtest.New(t)
	rows, rep := h.Mod.Services.Paste("https://www.Example.com/path\n*.cdn.example.net\nfull:api.example.org\n1.2.3.4\n\n# a comment\n", nil)
	want := []services.DomainRow{{Domain: "www.example.com"}, {Domain: "cdn.example.net"}, {Domain: "api.example.org", Exact: true}}
	if !slices.Equal(rows, want) {
		t.Errorf("rows: %+v", rows)
	}
	if len(rep.Added) != 3 || len(rep.Refused) != 1 || rep.Refused[0].Line != "1.2.3.4" || rep.Refused[0].Reason != "services.paste.ip" {
		t.Errorf("report: %+v", rep)
	}
	_, rep = h.Mod.Services.Paste("regexp:x\nexa mple.com", rows)
	if len(rep.Refused) != 2 || rep.Refused[0].Reason != "services.paste.unsupported" || rep.Refused[1].Reason != "services.paste.invalid" || len(rep.Added) != 0 {
		t.Errorf("refusals: %+v", rep)
	}
}

func TestSuffixAbsorbs(t *testing.T) {
	h := routingtest.New(t)
	rows, rep := h.Mod.Services.Paste("full:api.example.com\nexample.com\ncdn.example.com\nexample.com\nother.org", nil)
	if !slices.Equal(rows, []services.DomainRow{{Domain: "example.com"}, {Domain: "other.org"}}) {
		t.Errorf("rows: %+v", rows)
	}
	if !slices.Contains(rep.Merged, services.Merge{Name: "api.example.com", Under: "example.com"}) ||
		!slices.Contains(rep.Merged, services.Merge{Name: "cdn.example.com", Under: "example.com"}) ||
		!slices.Contains(rep.Merged, services.Merge{Name: "example.com", Under: "example.com"}) || len(rep.Merged) != 3 {
		t.Errorf("merged: %+v", rep.Merged)
	}
	// an exact row covers nothing; suffix wins over exact for the same name
	rows, _ = h.Mod.Services.Paste("full:example.net\napi.example.net\nexample.net", nil)
	if !slices.Equal(rows, []services.DomainRow{{Domain: "example.net"}}) {
		t.Errorf("suffix wins: %+v", rows)
	}
	id := h.Custom("mine")
	saved, err := h.Mod.Services.SaveCustom(context.Background(), id, services.Edit{Name: "mine", Tag: "mine", Rows: []services.DomainRow{
		{Domain: "example.com"}, {Domain: "api.example.com", Exact: true, Note: "the API"},
	}}, "admin")
	if err != nil || len(saved.Report.Merged) != 1 {
		t.Fatalf("%+v %v", saved, err)
	}
	stored, _ := h.Mod.Services.CustomRows(context.Background(), id)
	if !slices.Equal(stored, []services.DomainRow{{Domain: "example.com"}}) {
		t.Errorf("stored: %+v", stored)
	}
}

func TestCustomIDN(t *testing.T) {
	h := routingtest.New(t)
	rows, rep := h.Mod.Services.Paste("пример.рф", nil)
	if len(rows) != 1 || rows[0].Domain != "xn--e1afmkfd.xn--p1ai" || len(rep.Punycode) != 1 || rep.Punycode[0].Unicode != "пример.рф" {
		t.Fatalf("%+v %+v", rows, rep)
	}
	id := h.Custom("ru-sites", "пример.рф")
	acc, _ := h.Mod.Services.Accepted(context.Background(), id)
	if !slices.Equal(acc.Set.Suffix, []string{"xn--e1afmkfd.xn--p1ai"}) {
		t.Errorf("%+v", acc.Set)
	}
	body := h.Login.Get("/routing/services/1").Body.String()
	if !strings.Contains(body, "xn--e1afmkfd.xn--p1ai") || !strings.Contains(body, "пример.рф") {
		t.Error("the page doesn't show both forms")
	}
}

func TestSaveCustom(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	id := h.Custom("mine")
	if acc, _ := h.Mod.Services.Accepted(ctx, id); acc.Count() != 0 {
		t.Fatal("a new custom service has names")
	}
	edit := services.Edit{Name: "mine", Tag: "mine", Rows: []services.DomainRow{{Domain: "a.com"}, {Domain: "b.com"}, {Domain: "full:c.com"}}}
	saved, err := h.Mod.Services.SaveCustom(ctx, id, edit, "admin")
	if err != nil || !saved.Changed {
		t.Fatal(saved, err)
	}
	acc, _ := h.Mod.Services.Accepted(ctx, id)
	if !slices.Equal(acc.Set.Suffix, []string{"a.com", "b.com"}) || !slices.Equal(acc.Set.Exact, []string{"c.com"}) || acc.Added != 3 {
		t.Errorf("%+v", acc)
	}
	ev := h.Events("routing.service_updated")
	if len(ev) != 1 || ev[0].Payload["changes"] != "domains" || ev[0].Payload["added"] != float64(3) || ev[0].Payload["removed"] != float64(0) {
		t.Errorf("event: %+v", ev)
	}
	if m := h.Marks.Changes(); len(m) != 1 || !slices.Equal(m[0].Services, []int64{id}) {
		t.Errorf("marks: %+v", m)
	}

	saved, err = h.Mod.Services.SaveCustom(ctx, id, edit, "admin")
	if err != nil || saved.Changed {
		t.Fatal(saved, err)
	}
	if hist, _ := h.Mod.Services.Snapshots(ctx, id); len(hist) != 2 || len(h.Events("routing.service_updated")) != 1 || len(h.Marks.Changes()) != 1 {
		t.Errorf("an unchanged save recorded something: %d snapshots", len(hist))
	}

	// a note alone changes the rows, not the snapshot: recorded, not marked
	edit.Rows[0].Note = "the first"
	if saved, _ = h.Mod.Services.SaveCustom(ctx, id, edit, "admin"); !saved.Changed {
		t.Error("a note change was not saved")
	}
	if hist, _ := h.Mod.Services.Snapshots(ctx, id); len(hist) != 2 || len(h.Marks.Changes()) != 1 {
		t.Error("a note made a snapshot or a mark")
	}

	// errors name their row; nothing is written
	_, err = h.Mod.Services.SaveCustom(ctx, id, services.Edit{Name: "mine", Tag: "mine", Rows: []services.DomainRow{{Domain: "ok.com"}, {Domain: "1.2.3.4"}}}, "admin")
	var fe store.FieldErrors
	if !errors.As(err, &fe) || fe["row.2"] != "services.paste.ip" || len(fe) != 1 {
		t.Errorf("%v", err)
	}
	if rows, _ := h.Mod.Services.CustomRows(ctx, id); len(rows) != 3 {
		t.Errorf("a refused save wrote rows: %+v", rows)
	}
	if _, err := h.Mod.Services.SaveCustom(ctx, 999, edit, "admin"); !errors.Is(err, services.ErrNotFound) {
		t.Error(err)
	}
	h.Up.V2fly("x", "x.com")
	up := h.Upstream("x")
	if _, err := h.Mod.Services.SaveCustom(ctx, up, edit, "admin"); !errors.Is(err, services.ErrNotCustom) {
		t.Error(err)
	}
}

func TestRenameTag(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	id := h.Custom("mine", "example.com")
	marks := len(h.Marks.Changes())
	saved, err := h.Mod.Services.SaveCustom(ctx, id, services.Edit{Name: "mine", Tag: "personal", Rows: []services.DomainRow{{Domain: "example.com"}}}, "admin")
	if err != nil || !saved.Changed {
		t.Fatal(saved, err)
	}
	it, _ := h.Mod.Services.Get(ctx, id)
	if it.Tag != "personal" {
		t.Errorf("%+v", it)
	}
	ev := h.Events("routing.service_updated")
	last := ev[len(ev)-1]
	if last.Payload["changes"] != "tag" || last.Payload["from"] != "mine" || last.Payload["to"] != "personal" {
		t.Errorf("event: %+v", last.Payload)
	}
	if m := h.Marks.Changes(); len(m) != marks+1 || !slices.Equal(m[len(m)-1].Services, []int64{id}) {
		t.Errorf("marks: %+v", m)
	}

	h.Custom("other")
	_, err = h.Mod.Services.SaveCustom(ctx, id, services.Edit{Name: "mine", Tag: "other"}, "admin")
	var fe store.FieldErrors
	if !errors.As(err, &fe) || fe["tag"] != "services.err.tag_taken" {
		t.Errorf("a taken tag: %v", err)
	}
	for tag, key := range map[string]string{"telegram-cidr": "services.err.tag", "a b": "services.err.tag"} {
		_, err = h.Mod.Services.SaveCustom(ctx, id, services.Edit{Name: "mine", Tag: tag}, "admin")
		if !errors.As(err, &fe) || fe["tag"] != key {
			t.Errorf("%s: %v", tag, err)
		}
	}
	_, err = h.Mod.Services.CreateCustom(ctx, services.Custom{Name: "Мои сайты"}, "admin")
	if !errors.As(err, &fe) || fe["tag"] != "services.err.tag_empty" {
		t.Errorf("no tag from a Cyrillic name: %v", err)
	}
	cid, err := h.Mod.Services.CreateCustom(ctx, services.Custom{Name: "My sites"}, "admin")
	if c, _ := h.Mod.Services.Get(ctx, cid); err != nil || c.Tag != "my-sites" {
		t.Errorf("slug: %+v %v", c, err)
	}
	if _, err := h.Mod.Services.CreateCustom(ctx, services.Custom{Name: "My sites", Tag: "x"}, "admin"); !errors.As(err, &fe) || fe["name"] != "services.err.name_taken" {
		t.Errorf("a taken name: %v", err)
	}
}
