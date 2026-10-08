package deploy

import (
	"context"

	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

// CheckSetup is what health checks need of a server's template version: the
// stack's directory, the checks to run there and the proxy test object.
type CheckSetup struct {
	Server       store.Server
	Dir          string
	Checks       []manifest.Check
	ProxyTestURL string
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
	return CheckSetup{Server: srv, Dir: c.Man.Dir, Checks: c.Rendered.Checks, ProxyTestURL: c.Rendered.ProxyTestURL}, nil
}
