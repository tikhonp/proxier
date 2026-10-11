// Package testwait sizes the real-time limits of the tests' waits for the
// machine they run on.
package testwait

import (
	"os"
	"strconv"
	"time"
)

// patience multiplies every limit. A wait's limit is sized for a laptop, where
// a stuck wait should fail within a minute; the CI runner, with every package
// under -race at once, is ten times slower or more, and sets
// PROXIER_TEST_PATIENCE so the same tests get the time they need there.
var patience = func() time.Duration {
	n, err := strconv.Atoi(os.Getenv("PROXIER_TEST_PATIENCE"))
	if err != nil || n < 1 {
		return 1
	}
	return time.Duration(n)
}()

// For returns d scaled by PROXIER_TEST_PATIENCE.
func For(d time.Duration) time.Duration { return d * patience }

// Deadline is now plus For(d).
func Deadline(d time.Duration) time.Time { return time.Now().Add(For(d)) }
