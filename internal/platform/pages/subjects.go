package pages

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

// Namer is module.SubjectNamer: it names the subjects of its types.
type Namer interface {
	SubjectTypes() []string
	NameSubjects(ctx context.Context, typ string, ids []string) (map[string]ui.SubjectRef, error)
}

// subjectNames resolves the subjects of events and jobs to a label and a
// link. The platform names admin, job, settings and schedule; modules name
// theirs. An unnamed subject shows as "type:id" without a link.
type subjectNames struct {
	by       map[string]Namer
	log      *slog.Logger
	settings *settings.Store
}

func newSubjectNames(log *slog.Logger, st *settings.Store, namers []Namer) *subjectNames {
	s := &subjectNames{by: map[string]Namer{}, log: log, settings: st}
	for _, n := range namers {
		for _, t := range n.SubjectTypes() {
			s.by[t] = n
		}
	}
	return s
}

func (s *subjectNames) resolve(ctx context.Context, subs []events.Subject) map[events.Subject]ui.SubjectRef {
	ids := map[string][]string{}
	seen := map[events.Subject]bool{}
	for _, sub := range subs {
		if sub.Type == "" || seen[sub] {
			continue
		}
		seen[sub] = true
		ids[sub.Type] = append(ids[sub.Type], sub.ID)
	}
	out := map[events.Subject]ui.SubjectRef{}
	types := make([]string, 0, len(ids))
	for t := range ids {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		var named map[string]ui.SubjectRef
		var err error
		if n, ok := s.by[t]; ok {
			named, err = n.NameSubjects(ctx, t, ids[t])
		} else {
			named, err = s.platform(ctx, t, ids[t])
		}
		if err != nil {
			s.log.Error("pages: naming subjects", "type", t, "error", err)
		}
		for _, id := range ids[t] {
			sub := events.Subject{Type: t, ID: id}
			if ref, ok := named[id]; ok {
				out[sub] = ref
			} else {
				out[sub] = ui.SubjectRef{Label: sub.String()}
			}
		}
	}
	return out
}

// platform names the platform's own subject types.
func (s *subjectNames) platform(ctx context.Context, typ string, ids []string) (map[string]ui.SubjectRef, error) {
	out := map[string]ui.SubjectRef{}
	for _, id := range ids {
		switch typ {
		case "admin":
			out[id] = ui.SubjectRef{Label: i18n.T(ctx, "subject.admin"), Href: "/settings/security"}
		case "job":
			out[id] = ui.SubjectRef{Label: "#" + id, Href: "/jobs/" + id}
		case "settings":
			label := id
			if i18n.From(ctx).Has("settings." + id) {
				label = i18n.T(ctx, "settings."+id)
			}
			out[id] = ui.SubjectRef{Label: label, Href: "/settings/" + id}
		case "schedule":
			out[id] = ui.SubjectRef{Label: id, Href: "/jobs?type=" + typeOfSchedule(id)}
		default:
			return nil, fmt.Errorf("no namer for subject type %q", typ)
		}
	}
	return out, nil
}

// typeOfSchedule is the job type a platform schedule runs: its own name.
func typeOfSchedule(name string) string { return name }

func sortStrings(s []string) { sort.Strings(s) }
