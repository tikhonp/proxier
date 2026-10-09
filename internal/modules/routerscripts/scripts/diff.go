package scripts

import (
	"context"
	"strconv"

	"github.com/aymanbagabas/go-udiff"
)

// DraftRef is the name Diff takes for the draft.
const DraftRef = "draft"

// Body reads what a diff compares: a version number or "draft".
func (s *Service) Body(ctx context.Context, id int64, ref string) (string, error) {
	if ref == DraftRef {
		d, err := s.Draft(ctx, id)
		return d.Body, err
	}
	n, err := strconv.Atoi(ref)
	if err != nil || n < 1 {
		return "", ErrNoVersion
	}
	v, err := s.Version(ctx, id, n)
	return v.Body, err
}

// Diff is the unified diff of from and to (version numbers or "draft");
// "" when the bodies are equal.
func (s *Service) Diff(ctx context.Context, id int64, from, to string) (string, error) {
	sc, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}
	a, err := s.Body(ctx, id, from)
	if err != nil {
		return "", err
	}
	b, err := s.Body(ctx, id, to)
	if err != nil {
		return "", err
	}
	if a == b {
		return "", nil
	}
	return udiff.Unified(fileName(sc.Slug, from), fileName(sc.Slug, to), a, b), nil
}

func fileName(slug, ref string) string {
	if ref == DraftRef {
		return slug + "-draft.rsc"
	}
	return slug + "-v" + ref + ".rsc"
}
