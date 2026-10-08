package output

import (
	"encoding/base64"
	"strconv"
	"time"
)

// Header is one response header. Names are lower case, as apps read them;
// set them through the header map, not Set, which would capitalise them.
type Header struct{ Name, Value string }

// Headers are the subscription headers, in order. expires is the first moment
// the link is expired; zero means never (expire=0).
func Headers(title string, updateHours int, expires time.Time) []Header {
	exp := int64(0)
	if !expires.IsZero() {
		exp = expires.Unix() - 1
	}
	return []Header{
		{"profile-title", "base64:" + base64.StdEncoding.EncodeToString([]byte(title))},
		{"profile-update-interval", strconv.Itoa(updateHours)},
		{"subscription-userinfo", "upload=0; download=0; total=0; expire=" + strconv.FormatInt(exp, 10)},
		{"content-disposition", "inline; filename*=UTF-8''" + Escape(title) + ".txt"},
	}
}
