package generations

import (
	"regexp"
	"strconv"
	"strings"
)

var fileUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
var dashes = regexp.MustCompile(`-{2,}`)

// FileName is a generation's file: "<slug>-<router>-v<n>.rsc", the router's
// name kept to characters every file system and RouterOS take ("router" when
// none is left).
func FileName(slug, router string, version int) string {
	r := dashes.ReplaceAllString(fileUnsafe.ReplaceAllString(router, "-"), "-")
	r = strings.Trim(r, "-")
	if r == "" {
		r = "router"
	}
	return slug + "-" + r + "-v" + strconv.Itoa(version) + ".rsc"
}
