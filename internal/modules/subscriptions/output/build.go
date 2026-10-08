package output

import (
	"time"

	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// Link is what of a link decides its output.
type Link struct {
	State   string    // active, disabled, deleted
	Expires time.Time // the first moment it is expired; zero: never
}

// Request is everything a response is built from.
type Request struct {
	Link        Link
	Title       string // the subscription's title
	UpdateHours int
	Format      string   // already picked
	Servers     []Server // the members the catalog serves now, in order
	Hide        Hide
	Contact     string // general.admin_contact
	Now         time.Time
	Masked      bool
}

// Response is what a link gets, plus what the preview explains.
type Response struct {
	Outcome   string
	Headers   []Header
	Lines     []string // the URIs, or the one stub entry
	Body      []byte   // Lines in Format
	Served    []Server
	Hidden    []Hidden
	AllHidden bool
}

// Build decides deleted → disabled → expired → no servers → ok, and renders.
// loc is the link's language in the display zone (stub texts and dates).
func Build(loc *i18n.Localizer, r Request) (Response, error) {
	f, ok := Lookup(r.Format)
	if !ok {
		return Response{}, ErrBadFormat
	}
	resp := Response{Headers: Headers(r.Title, r.UpdateHours, r.Link.Expires)}
	switch {
	case r.Link.State == "deleted":
		resp.Outcome = StubDeleted
	case r.Link.State == "disabled":
		resp.Outcome = StubDisabled
	case !r.Link.Expires.IsZero() && !r.Now.Before(r.Link.Expires):
		resp.Outcome = StubExpired
	default:
		resp.Served, resp.Hidden, resp.AllHidden = Select(r.Servers, r.Hide, r.Now)
		lines, err := URIs(resp.Served, r.Masked)
		if err != nil {
			return Response{}, err
		}
		if len(lines) == 0 {
			resp.Outcome, resp.Served, resp.Hidden, resp.AllHidden = StubEmpty, nil, nil, false
		} else {
			resp.Outcome, resp.Lines = OK, lines
		}
	}
	if resp.Outcome != OK {
		resp.Lines = []string{StubURI(StubText(loc, resp.Outcome, r.Link.Expires, r.Contact))}
	}
	resp.Body = f.Render(resp.Lines)
	return resp, nil
}
