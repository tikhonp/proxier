package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunUsage(t *testing.T) {
	for args, want := range map[string]int{"": 2, "nope": 2, "help": 0, "version": 0} {
		var out, errOut bytes.Buffer
		var argv []string
		if args != "" {
			argv = strings.Fields(args)
		}
		if got := run(argv, &out, &errOut); got != want {
			t.Errorf("run(%q) = %d, want %d", args, got, want)
		}
	}
}
