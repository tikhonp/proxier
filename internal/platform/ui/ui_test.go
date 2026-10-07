package ui_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

func TestStaticHashIsTwelveHexDigits(t *testing.T) {
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(ui.StaticHash) {
		t.Errorf("hash %q", ui.StaticHash)
	}
	if got := ui.Asset("css/app.css"); got != "/static/"+ui.StaticHash+"/css/app.css" {
		t.Errorf("asset %q", got)
	}
}

func TestActionButtonCarriesDataActionAndCSRF(t *testing.T) {
	cat := i18n.NewCatalog()
	_ = cat.Add("t", i18n.Messages{"t.go": {EN: "Go", RU: "Вперёд"}, "t.drop": {EN: "Drop", RU: "Удалить"}})
	ctx := ui.WithCSRF(i18n.WithLocalizer(t.Context(), cat.Localizer(i18n.EN, nil)), "tok123")

	var b bytes.Buffer
	if err := ui.ActionButton(ui.Action{ID: "t.go", Label: "t.go", Method: "POST", Href: "/go", Key: "r", Fields: map[string]string{"x": "1"}}).Render(ctx, &b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{`data-action="t.go"`, `data-key="r"`, `name="_csrf" value="tok123"`, `name="x" value="1"`, `action="/go"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}

	b.Reset()
	err := ui.ActionButton(ui.Action{ID: "t.drop", Label: "t.drop", Method: "POST", Href: "/drop", Kind: ui.Danger,
		Confirm: &ui.Confirm{Strength: 3, Title: "Drop it", Body: "Gone for good.", Name: "de-1"}}).Render(ctx, &b)
	if err != nil {
		t.Fatal(err)
	}
	out = b.String()
	if !strings.Contains(out, `data-dialog-open="dlg-t.drop"`) || !strings.Contains(out, `data-confirm-name="de-1"`) || !strings.Contains(out, "data-confirm-submit") || !strings.Contains(out, "disabled") {
		t.Errorf("strength 3 dialog:\n%s", out)
	}
}
