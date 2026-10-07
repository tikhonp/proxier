package pages

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// stateOrder is how lifecycle states are listed.
var stateOrder = []string{"active", "provisioning", "failed", "retired"}

// serverStates writes "5 active, 1 failed" for one version's servers.
func serverStates(ctx context.Context, by map[string]int) string {
	var parts []string
	for _, st := range stateOrder {
		if n := by[st]; n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+i18n.T(ctx, "templates.state."+st))
		}
	}
	return strings.Join(parts, ", ")
}

// serversByVersion writes the list's cell: "v3 · 5 active, 1 failed   v2 · 1 retired".
func serversByVersion(ctx context.Context, by map[int]map[string]int) string {
	if len(by) == 0 {
		return i18n.T(ctx, "templates.servers.none")
	}
	nums := make([]int, 0, len(by))
	for n := range by {
		nums = append(nums, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(nums)))
	parts := make([]string, 0, len(nums))
	for _, n := range nums {
		parts = append(parts, fmt.Sprintf("v%d · %s", n, serverStates(ctx, by[n])))
	}
	return strings.Join(parts, "   ")
}

// draftLine says what a draft is based on.
func draftBasedOn(ctx context.Context, basedOn int) string {
	if basedOn == 0 {
		return i18n.T(ctx, "templates.draft.new")
	}
	return i18n.T(ctx, "templates.draft.based", i18n.Args{"v": basedOn})
}

// sourceText says where a draft or version came from.
func sourceText(ctx context.Context, s templates.Source) string {
	switch s.Kind {
	case "zip":
		return i18n.T(ctx, "templates.source.zip", i18n.Args{"name": s.Name})
	case "git":
		at := s.Commit
		if len(at) > 8 {
			at = at[:8]
		}
		p := s.URL
		if s.Path != "" {
			p += " (" + s.Path + ")"
		}
		return i18n.T(ctx, "templates.source.git", i18n.Args{"url": p, "ref": s.Ref, "commit": at})
	case "version":
		return i18n.T(ctx, "templates.source.version", i18n.Args{"v": s.Version})
	}
	return ""
}

func itoa(n int) string       { return strconv.Itoa(n) }
func i64(n int64) string      { return strconv.FormatInt(n, 10) }
func vlabel(n int) string     { return "v" + strconv.Itoa(n) }
func tplHref(id int64) string { return "/templates/" + i64(id) }

func depthClass(d int) string {
	if d > 8 {
		d = 8
	}
	return "d" + strconv.Itoa(d)
}

func sevClass(sev string) string {
	if sev == "error" {
		return "st-broken"
	}
	return "st-look"
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func diffHref(v diffPageView, split bool) string {
	h := "/templates/" + i64(v.T.ID) + "/diff?a=" + itoa(v.A) + "&b=" + itoa(v.B)
	if split {
		h += "&view=split"
	}
	return h
}

// versionFileHref links to a file of a version, at a line when given.
func versionFileHref(id int64, version int, path string, line int) string {
	h := "/templates/" + i64(id) + "/versions/" + itoa(version) + "?file=" + urlQuery(path)
	if line > 0 {
		h += "#code-L" + itoa(line)
	}
	return h
}

// findingWhere is "path:line".
func findingWhere(path string, line int) string {
	if path == "" {
		return ""
	}
	if line > 0 {
		return path + ":" + itoa(line)
	}
	return path
}
