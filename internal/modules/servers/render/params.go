package render

import (
	"net/mail"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
)

// ParamProblem returns the i18n key of why raw is not a valid value of p.
func ParamProblem(p manifest.Parameter, raw string) string {
	switch p.Type {
	case "email":
		// One plain address. The seed's script quotes the email in single
		// quotes, so nothing that could end the quote or expand may pass.
		if strings.ContainsAny(raw, `'"$`+"`"+`\ `) || strings.ContainsFunc(raw, func(r rune) bool { return r < 0x20 }) {
			return "servers.err.param_email"
		}
		a, err := mail.ParseAddress(raw)
		if err != nil || a.Address != raw || a.Name != "" {
			return "servers.err.param_email"
		}
	case "int":
		if _, err := strconv.ParseInt(raw, 10, 64); err != nil {
			return "servers.err.param_int"
		}
	case "bool":
		if raw != "true" && raw != "false" {
			return "servers.err.param_bool"
		}
	case "choice":
		ok := false
		for _, o := range p.Options {
			ok = ok || o == raw
		}
		if !ok {
			return "servers.err.param_choice"
		}
	case "text":
		if utf8.RuneCountInString(raw) > 10000 {
			return "servers.err.param_long"
		}
	default: // string
		if utf8.RuneCountInString(raw) > 1000 {
			return "servers.err.param_long"
		}
	}
	return ""
}
