package web

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/access"
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
			return Forbidden(area)
		}
	}
}

// ForbiddenError is a member's grant not reaching what they asked for in
// Area. The error handler answers it with a page naming the area, or for
// htmx (which swaps no 4xx) with a fragment the key line shows.
type ForbiddenError struct{ Area access.Area }

func (e *ForbiddenError) Error() string {
	return "web: access to " + string(e.Area) + " doesn't reach this"
}
func (e *ForbiddenError) StatusCode() int { return http.StatusForbidden }

// Forbidden is the error of a request the member's grant doesn't reach in
// area. Being an error, it stops a handler that checks more than its route.
func Forbidden(area access.Area) error { return &ForbiddenError{Area: area} }

// Can reports whether the signed-in member's grant reaches min in area: for
// pages that show more to a higher level.
func Can(ctx context.Context, area access.Area, min access.Level) bool {
	return FromContext(ctx).Grant.Can(area, min)
}
