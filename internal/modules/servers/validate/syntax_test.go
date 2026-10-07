package validate_test

import (
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
)

func TestJSONAndYAMLErrors(t *testing.T) {
	// a validator "json" on a file with a trailing comma on line 3
	m := string(seedWith(nil)["manifest.yaml"])
	files := seedWith(map[string]string{
		"data.json":     "{\n  \"a\": 1,\n  \"b\": 2,\n}\n",
		"data.yaml":     "a: 1\nb: [1, 2\nc: 3\n",
		"manifest.yaml": m + "\n",
	})
	// declare both files in the manifest
	files["manifest.yaml"] = []byte(string(files["manifest.yaml"]) + "")
	manifestWith := replaceFiles(m, "  - { path: site/index.html }\n", "  - { path: site/index.html }\n  - { path: data.json, validate: json }\n  - { path: data.yaml, validate: yaml }\n")
	files["manifest.yaml"] = []byte(manifestWith)
	r := run(t, files)
	if !has(r, finding.Error, "json", "data.json", 4, "") && !has(r, finding.Error, "json", "data.json", 3, "") {
		t.Errorf("json error with a line expected: %v", r.Findings)
	}
	var yamlLine int
	for _, f := range r.Findings {
		if f.Check == "yaml" && f.Path == "data.yaml" {
			yamlLine = f.Line
		}
	}
	if yamlLine == 0 {
		t.Errorf("yaml error with a line expected: %v", r.Findings)
	}
}

func replaceFiles(s, old, new string) string {
	i := index(s, old)
	if i < 0 {
		panic("replaceFiles: not found")
	}
	return s[:i] + new + s[i+len(old):]
}
