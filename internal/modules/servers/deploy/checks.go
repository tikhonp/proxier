package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

// CheckSetup is what health checks need of a server's template version: the
// stack's directory, the checks to run there and the proxy test object.
type CheckSetup struct {
	Server       store.Server
	Dir          string
	Checks       []manifest.Check
	ProxyTestURL string
	// Uninstall are the version's uninstall steps (retirement), rendered.
	Uninstall []manifest.Step
}

// CheckSetup renders the version in force for the server with its stored
// parameters. It writes nothing.
func (s *Service) CheckSetup(ctx context.Context, serverID int64) (CheckSetup, error) {
	srv, err := store.GetServer(ctx, s.DB.R, serverID)
	if err != nil {
		return CheckSetup{}, err
	}
	c, err := s.compute(ctx, srv, Target{}, nil)
	if err != nil {
		return CheckSetup{Server: srv}, err
	}
	return CheckSetup{Server: srv, Dir: c.Man.Dir, Checks: c.Rendered.Checks, ProxyTestURL: c.Rendered.ProxyTestURL, Uninstall: c.Rendered.Uninstall}, nil
}

// StackNamesIP says whether the version in force renders anything else for
// the server at ip than at its IP: its files, steps, endpoints or checks use
// .Server.IP, so an IP change needs a redeploy to reach the stack.
func (s *Service) StackNamesIP(ctx context.Context, serverID int64, ip string) (bool, error) {
	srv, err := store.GetServer(ctx, s.DB.R, serverID)
	if err != nil {
		return false, err
	}
	now, err := s.compute(ctx, srv, Target{}, nil)
	if err != nil {
		return false, err
	}
	moved := srv
	moved.IP = ip
	then, err := s.compute(ctx, moved, Target{}, nil)
	if err != nil {
		return false, err
	}
	a, err := json.Marshal(now.Rendered)
	if err != nil {
		return false, err
	}
	b, err := json.Marshal(then.Rendered)
	return !bytes.Equal(a, b), err
}

// RetirementSetup renders only the uninstall steps. A broken endpoint or file
// must not prevent cleanup of a stack that provisioning already uploaded.
func (s *Service) RetirementSetup(ctx context.Context, serverID int64) (CheckSetup, error) {
	srv, err := store.GetServer(ctx, s.DB.R, serverID)
	if err != nil {
		return CheckSetup{}, err
	}
	tpl, err := store.GetTemplate(ctx, s.DB.R, srv.TemplateID)
	if err != nil {
		return CheckSetup{}, err
	}
	m, _, err := s.parseVersion(ctx, srv.TemplateID, srv.TemplateVersion)
	if err != nil {
		return CheckSetup{}, err
	}
	c := &computed{Srv: srv, Slug: tpl.Slug, Man: m, CurMan: m, Errs: FieldErrors{}}
	c.Plan.ToVersion = srv.TemplateVersion
	// Uninstall steps see the same .Addresses as the stack they remove, and
	// address-bound endpoints still render for .Clients.
	extra, err := store.ServerAddresses(ctx, s.DB.R, srv.ID)
	if err != nil {
		return CheckSetup{}, err
	}
	c.Addrs = provision.RenderAddresses(srv, extra)
	if err := s.computeParams(ctx, c); err != nil {
		return CheckSetup{}, err
	}
	rows, err := store.GeneratedValues(ctx, s.DB.R, srv.ID)
	if err != nil {
		return CheckSetup{}, err
	}
	if c.Gen, err = sealed.OpenGenerated(s.Vault, srv.ID, rows); err != nil {
		return CheckSetup{}, err
	}
	renderCtx := c.renderContext()
	// Preserve .Clients for uninstall scripts when an endpoint still renders,
	// without requiring endpoints to be valid or even present for retirement.
	endpoints, _ := render.Render(&manifest.Manifest{Parameters: m.Parameters, Endpoints: m.Endpoints}, nil, renderCtx)
	renderCtx.Clients = render.ClientsOf(endpoints.Endpoints)
	cleanup := &manifest.Manifest{Parameters: m.Parameters, Steps: manifest.Steps{Uninstall: m.Steps.Uninstall}}
	rendered, findings := render.Render(cleanup, nil, renderCtx)
	var problems []string
	for _, f := range findings {
		if f.Severity == "error" {
			problems = append(problems, f.String())
		}
	}
	if len(problems) > 0 {
		return CheckSetup{}, fmt.Errorf("render uninstall steps: %s", strings.Join(problems, "; "))
	}
	return CheckSetup{Server: srv, Dir: m.Dir, Uninstall: rendered.Uninstall}, nil
}
