package pages_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

type subsSettings = subs.Settings

// formValues is the settings form of id as the page fills it.
func formValues(h *substest.Harness, id int64) url.Values {
	s, err := h.Mod.Subs.Get(h.T.Context(), id)
	if err != nil {
		h.T.Fatal(err)
	}
	v := url.Values{"name": {s.Name}, "title": {s.Title}, "description": {s.Description}, "default_format": {s.DefaultFormat},
		"update_hours": {strconv.Itoa(s.UpdateHours)}, "grace": {strconv.Itoa(int(s.Hide.Grace / time.Minute))}}
	v["format"] = s.Formats
	v["hide_state"] = s.Hide.States
	if s.Hide.On {
		v.Set("hide", "1")
	}
	if s.AutoAdd {
		v.Set("auto_add", "1")
	}
	return v
}

// formSettings is the form after change, as the service's Settings.
func formSettings(h *substest.Harness, id int64, change func(url.Values)) subs.Settings {
	v := formValues(h, id)
	change(v)
	hours, _ := strconv.Atoi(v.Get("update_hours"))
	grace, _ := strconv.Atoi(v.Get("grace"))
	return subs.Settings{Name: v.Get("name"), Title: v.Get("title"), Description: v.Get("description"), Formats: v["format"],
		DefaultFormat: v.Get("default_format"), UpdateHours: hours, HideOn: v.Get("hide") == "1", HideStates: v["hide_state"],
		GraceMinutes: grace, AutoAdd: v.Get("auto_add") == "1"}
}

// post is an htmx POST of the signed-in admin.
func post(h *substest.Harness, path string, form url.Values) sitetest.Req {
	form.Set("_csrf", h.Login.CSRF)
	return sitetest.Req{Method: http.MethodPost, Path: path, Form: form, Cookies: []*http.Cookie{h.Login.Cookie}, Header: hx()}
}

func order(t *testing.T, h *substest.Harness) string {
	t.Helper()
	ms, err := h.Mod.Subs.Members(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return strings.Join(out, ",")
}
