package store

import (
	"strconv"
	"time"

	"github.com/tikhonp/proxier/internal/platform/db"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// mustTime parses a stored time literal at start-up.
func mustTime(s string) time.Time {
	t, err := time.Parse(db.TimeLayout, s)
	if err != nil {
		panic(err)
	}
	return t
}
