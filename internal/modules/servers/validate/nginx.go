package validate

import (
	"bytes"
	"io"
	"regexp"
	"strings"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
)

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// nginxFindings parses an nginx file as nginx would read it, with a pure Go
// parser, so nothing runs: syntax, unknown directives, directives in the
// wrong context and wrong argument counts. What only a running nginx finds
// (a missing certificate file, an unreachable upstream) is not checked.
//
// The nginx image renders *.template files with the service's environment
// before reading them; the same is done here. Where the file lands in the
// container decides its parse context: nginx.conf is the main context, a file
// under /etc/nginx/templates/ or /etc/nginx/conf.d/ sits inside http { }.
func nginxFindings(f render.RenderedFile, files map[string]render.RenderedFile, cp *composeCache) []finding.Finding {
	var composeFile render.RenderedFile
	for _, rf := range files {
		if rf.Path == "compose.yaml" || rf.Path == "compose.yml" || rf.Path == "docker-compose.yaml" {
			composeFile = rf
		}
	}
	if composeFile.Path != "" {
		cp.load(composeFile, files)
	}

	content := string(f.Content)
	target := ""
	if ms := cp.mountsOf(f.Path); len(ms) > 0 {
		target = ms[0].Target
		if strings.HasSuffix(f.Path, ".template") {
			env := ms[0].Service.Environment
			content = envRef.ReplaceAllStringFunc(content, func(m string) string {
				name := strings.Trim(m, "${}")
				if v, ok := env[name]; ok && v != nil {
					return *v
				}
				return m // undefined: left as written, as envsubst does for names it is not given
			})
		}
	}

	if !mainContext(f.Path, target) {
		// The opening brace shares the first line, so lines stay the file's own.
		content = "http { " + content + "\n}"
	}

	payload, err := crossplane.Parse("nginx.conf", &crossplane.ParseOptions{
		SingleFile:               true,
		ErrorOnUnknownDirectives: true,
		ParseComments:            false,
		Open: func(string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte(content))), nil
		},
		Glob: func(p string) ([]string, error) { return []string{p}, nil },
	})
	if err != nil {
		return []finding.Finding{errorf("nginx", f, 0, "%v", err)}
	}
	var out []finding.Finding
	for _, pe := range payload.Errors {
		line := 0
		if pe.Line != nil {
			line = *pe.Line
		}
		out = append(out, errorf("nginx", f, line, "%s", nginxMessage(pe.Error)))
	}
	return out
}

func mainContext(path, target string) bool {
	switch {
	case target == "/etc/nginx/nginx.conf":
		return true
	case strings.HasPrefix(target, "/etc/nginx/templates/"), strings.HasPrefix(target, "/etc/nginx/conf.d/"):
		return false
	}
	return path == "nginx.conf" || strings.HasSuffix(path, "/nginx.conf")
}

// nginxMessage drops the " in nginx.conf:N" tail crossplane adds: the finding
// carries the real file and line.
func nginxMessage(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, " in nginx.conf"); i >= 0 {
		msg = msg[:i]
	}
	return msg
}
