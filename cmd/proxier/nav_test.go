package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

var sidebarLink = regexp.MustCompile(`<a href="([^"]+)" data-goto="([^"]*)"`)

// TestEverySidebarPageHasItsOwnGoKey renders the sidebar of every module for
// the owner, who sees every page: each page is reachable by g and a letter of
// its own, and none takes g, which goes to the top of the list.
func TestEverySidebarPageHasItsOwnGoKey(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{Modules: modules()})
	body := s.SignIn("").Get("/").Body.String()
	start := strings.Index(body, `<nav class="side pc-side"`)
	if start < 0 {
		t.Fatal("no sidebar on the dashboard")
	}
	side := body[start : start+strings.Index(body[start:], "</nav>")]

	links := sidebarLink.FindAllStringSubmatch(side, -1)
	if len(links) < 15 {
		t.Fatalf("the sidebar has %d pages, want every module's 15", len(links))
	}
	taken := map[string]string{}
	for _, l := range links {
		href, key := l[1], l[2]
		switch {
		case key == "":
			t.Errorf("%s has no g letter", href)
		case key == "g":
			t.Errorf("%s takes g, which is top of the list", href)
		case taken[key] != "":
			t.Errorf("%s and %s both take g %s", taken[key], href, key)
		default:
			taken[key] = href
		}
	}
}
