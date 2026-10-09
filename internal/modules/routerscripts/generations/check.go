package generations

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/modules/subscriptions"
)

// MaxName is the longest router name and created link name.
const MaxName = 60

// LanNet is the parameter a new router's host defaults from: <lanNet>.1.
const LanNet = "lanNet"

var threeOctets = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,3}$`)

// hostFrom is "<lanNet>.1" for a lanNet value of three dotted numbers.
func hostFrom(lanNet string) string {
	if !threeOctets.MatchString(lanNet) || net.ParseIP(lanNet+".1") == nil {
		return ""
	}
	return lanNet + ".1"
}

// pendingLink stands in for a link's URL until Generate creates it: it
// differs from every default, so the summary counts the line as changed.
const pendingLink = "\x00link"

func nameLen(s string) bool {
	n := utf8.RuneCountInString(s)
	return n >= 1 && n <= MaxName
}

// lock is where a parameter's value comes from under the form's choices
// ("" for a field the admin types).
func lock(p params.Param, f Form) string {
	switch p.Annotations.Fill {
	case params.FillKey:
		return "key"
	case params.FillLink:
		if f.Link.Mode == LinkCreate || f.Link.Mode == LinkExisting {
			return "link"
		}
	case params.FillList, params.FillForwarder:
		if f.Router.Mode == RouterNew || f.Router.Mode == RouterExisting {
			return "router"
		}
	}
	return ""
}

// plan checks every value and choice of v.Form and says what Generate would
// do. Reads go through the ports' read paths; nothing is written.
func (s *Service) plan(ctx context.Context, v *View, version int, keep map[string]string) error {
	f := v.Form
	fe := store.FieldErrors{}
	pl := Plan{Computed: map[string]string{}}
	v.Locked, v.LockedValues, v.Kept = map[string]string{}, map[string]string{}, map[string]bool{}

	// the router first: an existing one names the file and fills the names
	routerName := strings.TrimSpace(f.RouterName)
	if f.Router.Mode == RouterExisting {
		for i := range v.Routers {
			if v.Routers[i].ID == f.Router.RouterID {
				v.router = &v.Routers[i]
			}
		}
		if v.router == nil {
			fe["router.id"] = "generations.err.router"
		} else {
			routerName = v.router.Name
		}
	}
	v.Form.RouterName = routerName
	switch {
	case !nameLen(routerName):
		fe["router_name"] = "generations.err.router_name"
	case f.Router.Mode == RouterNew:
		for _, r := range v.Routers {
			if r.Name == routerName {
				fe["router_name"] = "generations.err.router_taken"
			}
		}
	}

	// the link
	if f.Link.Mode == LinkExisting {
		for _, l := range v.Links {
			if l.ID == f.Link.LinkID {
				full, err := s.d.Links.Link(ctx, l.ID)
				if err != nil && !errors.Is(err, subscriptions.ErrLinkNotFound) {
					return err
				}
				if err == nil && full.State != "deleted" {
					v.link = &full
				}
			}
		}
		if v.link == nil {
			fe["link.id"] = "generations.err.link"
		}
	}

	// the values
	values := map[string]string{}
	for _, p := range v.Parsed.Params {
		src := lock(p, f)
		var val string
		switch src {
		case "key":
			val = v.keyLine
			v.LockedValues[p.Name] = v.keyLine
		case "link":
			val = pendingLink
			if v.link != nil {
				val = v.link.URL
			}
		case "router":
			list, fwd := routing.DefaultAddressList, routing.DefaultForwarder
			if f.Router.Mode == RouterExisting && v.router != nil {
				list, fwd = v.router.AddressList, v.router.Forwarder
			}
			val = list
			if p.Annotations.Fill == params.FillForwarder {
				val = fwd
			}
			v.LockedValues[p.Name] = val
		default:
			posted, ok := f.Values[p.Name]
			val = strings.TrimSpace(posted)
			switch {
			case secret(p) && val == "":
				val = p.Default
				if kept, ok := keep[p.Name]; ok {
					val, v.Kept[p.Name] = kept, true
				}
			case !ok:
				val = p.Default
			}
			if k := params.Check(p, val); k != "" {
				fe["param."+p.Name] = k
			}
		}
		if src != "" {
			v.Locked[p.Name] = src
		}
		values[p.Name] = val
	}
	pl.Computed = params.Eval(v.Parsed, values)
	pl.Changed = params.Changed(v.Parsed, values)
	for name, val := range pl.Computed {
		if strings.Contains(val, pendingLink) {
			delete(pl.Computed, name)
		}
	}

	// step 1: the link
	switch f.Link.Mode {
	case LinkCreate:
		name := strings.TrimSpace(f.Link.Name)
		if name == "" {
			name = DefaultLinkName(routerName)
		}
		v.linkNm = name
		if !nameLen(name) {
			fe["link.name"] = "generations.err.link_name"
		}
		for _, l := range v.Links {
			if l.Name == name {
				fe["link.name"] = "generations.err.link_taken"
			}
		}
		for i := range v.Subscriptions {
			if v.Subscriptions[i].ID == f.Link.SubscriptionID {
				v.linkSub = &v.Subscriptions[i]
			}
		}
		sub := ""
		if v.linkSub == nil {
			fe["link.subscription"] = "generations.err.link_subscription"
		} else {
			sub = v.linkSub.Name
		}
		pl.Steps = append(pl.Steps, Step{Key: "generations.sum.create_link", Args: map[string]any{"name": name, "subscription": sub}})
	case LinkExisting:
		if v.link != nil {
			pl.Steps = append(pl.Steps, Step{Key: "generations.sum.use_link", Args: map[string]any{"name": v.link.Name}})
			switch v.link.State {
			case "disabled":
				pl.Warnings = append(pl.Warnings, Step{Key: "generations.warn.link_disabled"})
			case "expired":
				pl.Warnings = append(pl.Warnings, Step{Key: "generations.warn.link_expired"})
			}
		}
	}

	// step 2: the router
	switch f.Router.Mode {
	case RouterNew:
		s.planNewRouter(v, &pl, fe, routerName, values)
	case RouterExisting:
		if v.router != nil {
			pl.Steps = append(pl.Steps, Step{Key: "generations.sum.use_router", Args: map[string]any{
				"name": v.router.Name, "list": v.router.AddressList, "forwarder": v.router.Forwarder}})
		}
	case RouterNone:
		pl.Steps = append(pl.Steps, Step{Key: "generations.sum.not_registered"})
	}

	// step 3: the file
	pl.FileName = FileName(v.Script.Slug, routerName, version)
	pl.Steps = append(pl.Steps, Step{Key: "generations.sum.write", Args: map[string]any{
		"file": pl.FileName, "version": version, "n": len(pl.Changed), "names": strings.Join(pl.Changed, ", ")}})

	v.values = values
	if len(fe) > 0 {
		pl.Errors = fe
	}
	v.Plan = pl
	return nil
}

// planNewRouter checks a new router's connection and says how it is
// registered.
func (s *Service) planNewRouter(v *View, pl *Plan, fe store.FieldErrors, name string, values map[string]string) {
	rc := v.Form.Router
	if _, ok := v.Parsed.Param(LanNet); ok {
		pl.HostAuto = hostFrom(values[LanNet])
	}
	host := strings.TrimSpace(rc.Host)
	if host == "" {
		host = pl.HostAuto
	}
	pl.Host = host
	if host == "" {
		fe["router.host"] = "generations.err.host"
	}
	port := rc.Port
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		fe["router.port"] = "generations.err.port"
	}
	var list *routing.RegistrarList
	for i := range v.Lists {
		if rc.ListID == 0 && v.Lists[i].Default || v.Lists[i].ID == rc.ListID {
			list = &v.Lists[i]
			break
		}
	}
	if list == nil {
		fe["router.list"] = "generations.err.list"
	}
	reg := routing.RouterRegistration{Name: name, Host: host, Port: port, User: strings.TrimSpace(rc.User), Tailnet: rc.Tailnet}
	if list != nil {
		reg.ListID = list.ID
	}
	jumpWord := ""
	switch rc.Jump {
	case "":
	case JumpOther:
		reg.JumpHost, reg.JumpPort, reg.JumpUser = strings.TrimSpace(rc.JumpHost), rc.JumpPort, strings.TrimSpace(rc.JumpUser)
		if reg.JumpHost == "" {
			fe["router.jump_host"] = "generations.err.jump_host"
		}
		if reg.JumpPort == 0 {
			reg.JumpPort = 22
		}
		jumpWord = reg.JumpHost
		pl.Warnings = append(pl.Warnings, Step{Key: "generations.warn.jump_unpinned"})
	default:
		var known *routing.JumpHost
		for i, j := range v.JumpHosts {
			if net.JoinHostPort(j.Host, strconv.Itoa(j.Port)) == rc.Jump {
				known = &v.JumpHosts[i]
			}
		}
		if known == nil {
			fe["router.jump_host"] = "generations.err.jump_host"
			break
		}
		reg.JumpHost, reg.JumpPort, reg.JumpUser, reg.Tailnet = known.Host, known.Port, known.User, known.Tailnet
		jumpWord = known.Host
		if !known.Pinned {
			pl.Warnings = append(pl.Warnings, Step{Key: "generations.warn.jump_unpinned"})
		}
	}
	v.reg = reg
	listName := ""
	if list != nil {
		listName = list.Name
	}
	key := "generations.sum.register"
	if jumpWord != "" {
		key = "generations.sum.register_jump"
	}
	pl.Steps = append(pl.Steps, Step{Key: key, Args: map[string]any{"name": name, "list": listName, "host": host, "jump": jumpWord}})
}
