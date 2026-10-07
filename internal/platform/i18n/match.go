package i18n

import "golang.org/x/text/language"

var matcher = language.NewMatcher([]language.Tag{language.English, language.Russian})

// Match picks en or ru from an Accept-Language header; ok is false when
// neither is acceptable.
func Match(acceptLanguage string) (l Lang, ok bool) {
	if acceptLanguage == "" {
		return "", false
	}
	tags, _, err := language.ParseAcceptLanguage(acceptLanguage)
	if err != nil || len(tags) == 0 {
		return "", false
	}
	_, i, conf := matcher.Match(tags...)
	if conf == language.No {
		return "", false
	}
	return Langs[i], true
}
