package routing_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestImportsOnlyServersPorts: the module knows the servers module only by
// its ports, the root package (build README, Phase 3). The test harness
// (routingtest) may build the real servers module.
func TestImportsOnlyServersPorts(t *testing.T) {
	const servers = "github.com/tikhonp/proxier/internal/modules/servers"
	fset := token.NewFileSet()
	n := 0
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || strings.HasPrefix(p, "routingtest/") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		n++
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(path, servers+"/") {
				t.Errorf("%s imports %s", p, path)
			}
		}
		return nil
	})
	if err != nil || n < 30 {
		t.Fatalf("%d files: %v", n, err)
	}
}
