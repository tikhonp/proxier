package web

import (
	"context"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// AreaRoutes are the route groups of one area. A module registers what reads
// on View and what changes on Manage; At is for what needs a higher level
// still (publishing a router script needs manage_all).
type AreaRoutes struct {
	View   *echo.Group // the area's lowest level
	Manage *echo.Group // the lowest level that changes anything (own in the owned areas)
	at     func(access.Level) *echo.Group
}

// At is a group that needs at least l in the area.
func (r AreaRoutes) At(l access.Level) *echo.Group { return r.at(l) }

// Area returns the route groups of a: signed in, CSRF on unsafe methods,
// and the member's grant checked against the group's level.
func (r Routes) Area(a access.Area) AreaRoutes {
	if !a.Valid() {
		panic("web: routes of an unknown area " + string(a))
	}
	at := func(l access.Level) *echo.Group {
		if _, err := access.ParseLevel(a, string(l)); err != nil {
			panic("web: " + err.Error())
		}
		return r.group(Require(a, l))
	}
	return AreaRoutes{View: at(a.ViewLevel()), Manage: at(a.ManageLevel()), at: at}
}

// Require lets a request through when the member's grant reaches min in
// area; otherwise it fails with Forbidden.
func Require(area access.Area, min access.Level) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if current(c).Grant.Can(area, min) {
				return next(c)
			}
			return access.Forbidden(area)
		}
	}
}

// Can reports whether the signed-in member's grant reaches min in area: for
// pages that show more to a higher level.
func Can(ctx context.Context, area access.Area, min access.Level) bool {
	return FromContext(ctx).Grant.Can(area, min)
}

// Who is the signed-in member and their grant. It panics outside a signed-in
// request, like Actor.
func Who(ctx context.Context) access.Who {
	r := FromContext(ctx)
	if r.Member == nil {
		panic("web: Who outside a signed-in request")
	}
	return access.Who{Member: r.Member.ID, Grant: r.Grant}
}

// OwnerNote is " · owner anna" for a thing of another member, shown on the
// thing's page to whoever reads everyone's; "" for the member's own.
func OwnerNote(ctx context.Context, owner int64) string {
	if owner == Who(ctx).Member {
		return ""
	}
	name, ok := MemberName(ctx, owner)
	if !ok {
		name = "#" + strconv.FormatInt(owner, 10)
	}
	return " · " + i18n.T(ctx, "members.owner", i18n.Args{"name": name})
}

// Scope is whose things the signed-in member reaches in area: what every
// query of owned things in a handler takes.
func Scope(ctx context.Context, area access.Area) access.Scope { return Who(ctx).Scope(area) }
