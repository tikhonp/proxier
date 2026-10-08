package output

import (
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// The outcomes of a response, as the fetch log stores them.
const (
	OK           = "ok"
	StubDisabled = "stub-disabled"
	StubExpired  = "stub-expired"
	StubDeleted  = "stub-deleted"
	StubEmpty    = "stub-empty"
)

// stubPrefix is a VLESS URI that can't connect; the fragment carries the text.
const stubPrefix = "vless://00000000-0000-0000-0000-000000000000@127.0.0.1:1?encryption=none&type=tcp&security=none#"

// Escape percent-encodes every byte but A–Z a–z 0–9 - . _ ~, so a space is
// %20 and "@" is %40 (docs/integrations/subscription-format.md).
func Escape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// StubURI is the stub entry named text.
func StubURI(text string) string { return stubPrefix + Escape(text) }

// StubText is the stub's name in loc's language; outcome is one of the Stub*
// constants; expires matters only for StubExpired, whose text names the last
// day the link worked.
func StubText(loc *i18n.Localizer, outcome string, expires time.Time, contact string) string {
	var key string
	args := i18n.Args{"contact": contact}
	switch outcome {
	case StubDisabled:
		key = "links.stub.disabled"
	case StubExpired:
		key = "links.stub.expired"
		args["date"] = loc.Date(expires.Add(-time.Second))
	case StubDeleted:
		return loc.T("links.stub.deleted")
	default:
		return loc.T("links.stub.empty")
	}
	if contact != "" {
		key += ".contact"
	}
	return loc.T(key, args)
}

// Messages are the stub texts; the module adds them to its catalog.
var Messages = i18n.Messages{
	"links.stub.disabled":         {EN: "⛔ Link disabled", RU: "⛔ Ссылка отключена"},
	"links.stub.disabled.contact": {EN: "⛔ Link disabled · contact {contact}", RU: "⛔ Ссылка отключена · пишите {contact}"},
	"links.stub.expired":          {EN: "⏳ Expired on {date}", RU: "⏳ Срок истёк {date}"},
	"links.stub.expired.contact":  {EN: "⏳ Expired on {date} · contact {contact}", RU: "⏳ Срок истёк {date} · пишите {contact}"},
	"links.stub.deleted":          {EN: "⛔ Link removed", RU: "⛔ Ссылка удалена"},
	"links.stub.empty":            {EN: "⚠️ No servers yet", RU: "⚠️ Серверов пока нет"},
}
