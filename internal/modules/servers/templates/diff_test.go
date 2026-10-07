package templates_test

import (
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/templates"
)

func TestDiffFiles(t *testing.T) {
	a := map[string][]byte{
		"same.txt":    []byte("one\n"),
		"changed.txt": []byte("one\ntwo\nthree\n"),
		"removed.txt": []byte("gone\n"),
		"blob.bin":    {0, 1, 2, 3},
	}
	b := map[string][]byte{
		"same.txt":    []byte("one\n"),
		"changed.txt": []byte("one\n2\nthree\n"),
		"added.txt":   []byte("new\n"),
		"blob.bin":    {0, 1, 2, 3, 4, 5},
	}
	got := map[string]templates.FileDiff{}
	var order []string
	for _, d := range templates.Diff(a, b) {
		got[d.Path] = d
		order = append(order, d.Path)
	}
	if strings.Join(order, " ") != "added.txt blob.bin changed.txt removed.txt same.txt" {
		t.Errorf("order: %v", order)
	}
	if d := got["same.txt"]; d.Change != templates.Unchanged || d.Unified != "" {
		t.Errorf("unchanged: %+v", d)
	}
	if d := got["added.txt"]; d.Change != templates.Added || !strings.Contains(d.Unified, "+new") {
		t.Errorf("added: %+v", d)
	}
	if d := got["removed.txt"]; d.Change != templates.Removed || !strings.Contains(d.Unified, "-gone") {
		t.Errorf("removed: %+v", d)
	}
	d := got["changed.txt"]
	if d.Change != templates.Changed || !strings.Contains(d.Unified, "-two") || !strings.Contains(d.Unified, "+2") || !strings.Contains(d.Unified, "@@") {
		t.Errorf("changed: %+v", d)
	}
	if d := got["blob.bin"]; d.Change != templates.Changed || !d.Binary || d.Unified != "" || d.OldSize != 4 || d.NewSize != 6 {
		t.Errorf("binary: %+v", d)
	}
}
