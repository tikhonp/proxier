package links

import (
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
)

// ParseExpiry reads the expiry fields of a form in the display zone and
// returns the first moment the link is expired. "never" (or no mode) is zero.
// A date alone means the end of that day: 00:00 of the next one. Errors are
// store.FieldErrors on "expiry".
func ParseExpiry(mode, date, clock string, zone *time.Location, now time.Time) (time.Time, error) {
	if mode != "on" {
		return time.Time{}, nil
	}
	date, clock = strings.TrimSpace(date), strings.TrimSpace(clock)
	day, err := time.ParseInLocation("2006-01-02", date, zone)
	if err != nil {
		return time.Time{}, store.FieldErrors{"expiry": "links.err.expiry_date"}
	}
	var at time.Time
	if clock == "" {
		at = day.AddDate(0, 0, 1)
	} else {
		hm, err := time.Parse("15:04", clock)
		if err != nil {
			return time.Time{}, store.FieldErrors{"expiry": "links.err.expiry_time"}
		}
		at = time.Date(day.Year(), day.Month(), day.Day(), hm.Hour(), hm.Minute(), 0, 0, zone)
	}
	if !at.After(now) {
		return time.Time{}, store.FieldErrors{"expiry": "links.err.expiry_past"}
	}
	return at.UTC(), nil
}

// DateOnly reports whether an expiry falls on midnight in zone: it was set as
// a date, and the last day it works is the day before.
func DateOnly(expires time.Time, zone *time.Location) bool {
	t := expires.In(zone)
	return t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0
}
