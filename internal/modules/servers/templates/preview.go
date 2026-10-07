package templates

import (
	"context"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

// Preview is a file set rendered for the sample context.
type Preview struct {
	Server   render.Server
	Files    []render.RenderedFile
	Findings []finding.Finding // why a file is missing from Files
}

// PreviewFiles renders files (a draft as the editor holds it, unsaved edits
// included) for the sample server. Nothing is stored and no event recorded.
// Sample values, generated ones too, are shown plainly: they belong to no
// server.
func (s *Service) PreviewFiles(ctx context.Context, templateID int64, files map[string][]byte) (Preview, error) {
	t, err := store.GetTemplate(ctx, s.d.R, templateID)
	if err != nil {
		return Preview{}, err
	}
	m, fs := manifest.Parse(files[manifest.Name])
	if m == nil || !(finding.Report{Findings: fs}).OK() {
		if len(fs) == 0 {
			fs = append(fs, finding.Errorf("manifest", manifest.Name, 0, "%s is missing", manifest.Name))
		}
		return Preview{Findings: fs}, nil
	}
	c := render.SampleContext(m, t.Slug, nil)
	rendered, rf := render.Render(m, files, c)
	return Preview{Server: c.Server, Files: rendered.Files, Findings: append(fs, rf...)}, nil
}

// Report validates the saved draft like ValidateDraft but records nothing:
// the publish screen shows it on every visit.
func (s *Service) Report(ctx context.Context, templateID int64) (finding.Report, error) {
	d, err := s.Draft(ctx, templateID)
	if err != nil {
		return finding.Report{}, err
	}
	return s.validate(ctx, templateID, d.Files)
}
