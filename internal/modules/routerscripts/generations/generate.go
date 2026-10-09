package generations

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/modules/subscriptions"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// The ports' field names, as the form's.
var (
	linkFields   = map[string]string{"name": "link.name", "subscription": "link.subscription"}
	routerFields = map[string]string{
		"name": "router_name", "list": "router.list", "host": "router.host", "port": "router.port", "user": "router.user",
		"jump_host": "router.jump_host", "jump_port": "router.jump_port", "jump_user": "router.jump_user",
	}
)

// merge adds a port's field errors to fe under the form's names; a field the
// form doesn't have lands on fallback. Anything else is returned.
func merge(fe store.FieldErrors, err error, names map[string]string, fallback string) error {
	var pe map[string]string
	var le subscriptions.FieldErrors
	var re routing.FieldErrors
	switch {
	case errors.As(err, &le):
		pe = le
	case errors.As(err, &re):
		pe = re
	default:
		return err
	}
	for k, v := range pe {
		name, ok := names[k]
		if !ok {
			name = fallback
		}
		if fe[name] == "" {
			fe[name] = v
		}
	}
	return nil
}

// Generate checks the form, then creates the link, registers the router and
// saves the generation in one transaction, or nothing at all. Field errors
// (store.FieldErrors, the form's keys) gather the check's and both ports'
// refusals.
func (s *Service) Generate(ctx context.Context, scriptID int64, f Form, actor string) (int64, error) {
	v, err := s.Check(ctx, scriptID, f)
	if err != nil {
		return 0, err
	}
	fe := store.FieldErrors{}
	maps.Copy(fe, v.Plan.Errors)
	values := maps.Clone(v.values)
	var gid int64
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		now := db.At(s.d.Now())
		var err error
		gid, err = store.InsertGeneration(ctx, tx, store.Generation{
			ScriptID: scriptID, Version: v.Form.Version, RouterName: v.Form.RouterName, FileName: v.Plan.FileName, CreatedAt: now, CreatedBy: actor,
		})
		if err != nil {
			return err
		}
		row := store.Generation{ID: gid}

		// both ports are asked even when the first refuses (each writes
		// nothing then), so every refusal shows at once
		var link *subscriptions.IssuedLink
		switch v.Form.Link.Mode {
		case LinkCreate:
			if v.linkSub != nil {
				l, err := s.d.Links.Issue(ctx, tx, v.linkNm, v.linkSub.ID, actor)
				if err := merge(fe, err, linkFields, "link.name"); err != nil {
					return err
				}
				if err == nil {
					link, row.LinkCreated = &l, true
				}
			}
		case LinkExisting:
			link = v.link
		}
		var router *routing.RegisteredRouter
		switch v.Form.Router.Mode {
		case RouterNew:
			if v.reg.Host != "" {
				r, err := s.d.Routers.Register(ctx, tx, v.reg, actor)
				if err := merge(fe, err, routerFields, "router.host"); err != nil {
					return err
				}
				if err == nil {
					router, row.RouterRegistered = &r, true
				}
			}
		case RouterExisting:
			router = v.router
		}
		if len(fe) > 0 {
			return fe
		}

		// the final values: what the link, the router and the key are now
		for _, p := range v.Parsed.Params {
			switch lock(p, v.Form) {
			case "link":
				values[p.Name] = link.URL
			case "router":
				values[p.Name] = router.AddressList
				if p.Annotations.Fill == params.FillForwarder {
					values[p.Name] = router.Forwarder
				}
			case "key":
				row.KeyFingerprint = v.keyFP
			}
		}
		plain, secrets := map[string]string{}, map[string]string{}
		for _, p := range v.Parsed.Params {
			val := values[p.Name]
			if val != p.Default {
				if _, err := params.Literal(p, val); err != nil {
					fe["param."+p.Name] = err.Error()
				}
			}
			if secret(p) {
				secrets[p.Name] = val
			} else {
				plain[p.Name] = val
			}
		}
		if len(fe) > 0 {
			return fe
		}
		b, err := json.Marshal(plain)
		if err != nil {
			return err
		}
		row.Vals = string(b)
		if len(secrets) > 0 {
			sb, err := json.Marshal(secrets)
			if err != nil {
				return err
			}
			row.Secrets = s.d.Vault.Seal(sb, secretsAAD(gid))
		}
		row.Changed = strings.Join(params.Changed(v.Parsed, values), ",")
		if link != nil {
			row.LinkID, row.LinkName = link.ID, link.Name
		}
		if router != nil {
			row.RouterID = router.ID
		}
		if err := store.UpdateGeneration(ctx, tx, row); err != nil {
			return err
		}
		_, err = s.d.Events.Record(ctx, tx, events.Event{Time: now, Type: "routerscript.generated", Subject: Subject(gid), Actor: actor, Payload: map[string]any{
			"script": v.Script.Name, "version": v.Form.Version, "router": v.Form.RouterName, "link": row.LinkName,
			"registered": row.RouterRegistered, "link_created": row.LinkCreated,
		}})
		return err
	})
	if err != nil {
		return 0, err
	}
	return gid, nil
}
