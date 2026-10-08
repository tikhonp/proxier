package snapshot

import (
	"slices"
	"testing"
)

func TestCanonicalSetAndHash(t *testing.T) {
	s := New([]string{"b.com", "a.com", "b.com"}, []string{"a.com", "x.a.com", "c.com", "c.com"}, nil)
	if !slices.Equal(s.Suffix, []string{"a.com", "b.com"}) || !slices.Equal(s.Exact, []string{"c.com", "x.a.com"}) || s.Count() != 4 {
		t.Fatalf("%+v", s)
	}
	if !s.Has("a.com", false) || s.Has("a.com", true) || !s.Has("x.a.com", true) {
		t.Error("Has")
	}
	same := New([]string{"a.com", "b.com"}, []string{"x.a.com", "c.com"}, []Skipped{{Entry: "regexp:x", Reason: "services.skip.unsupported"}})
	if s.Hash() != same.Hash() {
		t.Error("the same names in another order hash differently")
	}
	moved := New([]string{"a.com", "b.com", "c.com"}, []string{"x.a.com"}, nil)
	if moved.Hash() == s.Hash() {
		t.Error("a name changing form keeps the hash")
	}
	if New(nil, nil, nil).Hash() == s.Hash() {
		t.Error("empty hashes like a full set")
	}
	if got := Decode(Encode(s.Suffix)); !slices.Equal(got, s.Suffix) || Decode("") != nil {
		t.Errorf("round trip: %v", got)
	}
}

func TestCompare(t *testing.T) {
	old := New([]string{"a.com", "b.com", "c.com"}, []string{"x.com"}, nil)
	new := New([]string{"b.com", "c.com", "d.com", "x.com"}, []string{"y.com"}, nil)
	d := Compare(old, new)
	if !slices.Equal(d.AddedSuffix, []string{"d.com", "x.com"}) || !slices.Equal(d.RemovedSuffix, []string{"a.com"}) ||
		!slices.Equal(d.AddedExact, []string{"y.com"}) || !slices.Equal(d.RemovedExact, []string{"x.com"}) {
		t.Fatalf("%+v", d)
	}
	if d.Added() != 3 || d.Removed() != 2 {
		t.Errorf("counts %d %d", d.Added(), d.Removed())
	}
	if d := Compare(Set{}, old); d.Added() != 4 || d.Removed() != 0 {
		t.Errorf("against nothing: %+v", d)
	}
}
