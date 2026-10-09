package catalog

import (
	"context"

	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

// Spell is the iplist spelling rule, pure: a row of portal with name is
// written iplist:<name> when no portal before it (in selector.Portals order)
// has that name as a site or a group, else pinned, iplist:<portal>:<name>,
// so the service resolves to what the admin saw.
func Spell(portal, name string, has func(portal, name string) bool) string {
	for _, p := range selector.Portals {
		if p == portal {
			break
		}
		if has(p, name) {
			return "iplist:" + portal + ":" + name
		}
	}
	return "iplist:" + name
}

// speller knows which iplist names each portal has in its current catalog.
type speller struct {
	svc   *Service
	kinds map[string]map[string]map[string]bool // portal → name → kind
}

func (s *Service) speller(ctx context.Context, names []string) (*speller, error) {
	sp := &speller{svc: s, kinds: map[string]map[string]map[string]bool{}}
	return sp, sp.load(ctx, names)
}

// load reads the portals' sites and groups with these names.
func (sp *speller) load(ctx context.Context, names []string) error {
	for i := 0; i < len(names); i += lookupChunk {
		rows, err := store.IplistNamed(ctx, sp.svc.d.DB.R, names[i:min(i+lookupChunk, len(names))])
		if err != nil {
			return err
		}
		for _, r := range rows {
			p := portalOf(r.Source)
			if sp.kinds[p] == nil {
				sp.kinds[p] = map[string]map[string]bool{}
			}
			if sp.kinds[p][r.Name] == nil {
				sp.kinds[p][r.Name] = map[string]bool{}
			}
			sp.kinds[p][r.Name][r.Kind] = true
		}
	}
	return nil
}

func (sp *speller) has(portal, name string) bool { return len(sp.kinds[portal][name]) > 0 }

func (sp *speller) spell(portal, name string) string { return Spell(portal, name, sp.has) }

// selectable: a group whose portal also has a site of that name can't be
// selected, because the site is tried first.
func (sp *speller) selectable(portal, kind, name string) bool {
	return kind != "group" || !sp.kinds[portal][name]["site"]
}

// Offers are the catalog's selectors whose tag is tag, spelled: v2fly:<tag>
// when v2fly has that list, and the iplist selector of the first portal with
// a selectable site or group of that name. Adopting an unmanaged router tag
// offers them (3f).
func (s *Service) Offers(ctx context.Context, tag string) ([]string, error) {
	var out []string
	ok, err := store.V2flyHas(ctx, s.d.DB.R, tag)
	if err != nil {
		return nil, err
	}
	if ok {
		out = append(out, "v2fly:"+tag)
	}
	sp, err := s.speller(ctx, []string{tag})
	if err != nil {
		return nil, err
	}
	for _, p := range selector.Portals {
		for kind := range sp.kinds[p][tag] {
			if sp.selectable(p, kind, tag) {
				return append(out, sp.spell(p, tag)), nil
			}
		}
	}
	return out, nil
}
