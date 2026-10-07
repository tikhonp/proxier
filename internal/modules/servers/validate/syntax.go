package validate

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"go.yaml.in/yaml/v3"
)

// jsonFindings reports a syntax error with its line.
func jsonFindings(check string, f render.RenderedFile) []finding.Finding {
	if json.Valid(f.Content) {
		return nil
	}
	var v any
	err := json.Unmarshal(f.Content, &v)
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return []finding.Finding{errorf(check, f, lineAt(f.Content, se.Offset), "%s", se.Error())}
	}
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		return []finding.Finding{errorf(check, f, lineAt(f.Content, te.Offset), "%s", te.Error())}
	}
	return []finding.Finding{errorf(check, f, 0, "%v", err)}
}

var yamlErrLine = regexp.MustCompile(`line (\d+)`)

func yamlFindings(f render.RenderedFile) []finding.Finding {
	var n yaml.Node
	err := yaml.Unmarshal(f.Content, &n)
	if err == nil {
		return nil
	}
	line := 0
	if m := yamlErrLine.FindStringSubmatch(err.Error()); m != nil {
		line, _ = strconv.Atoi(m[1])
	}
	return []finding.Finding{errorf("yaml", f, line, "%s", strings.TrimPrefix(err.Error(), "yaml: "))}
}
