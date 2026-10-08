package provision

import (
	"context"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// ErrNotAllowed is returned for an action the server's state does not allow.
var ErrNotAllowed = errors.New("provision: not allowed in this state")

// NeedsPassword reports whether Retry of a server that failed at step needs
// the root password again: before Proxier's key works there is nothing else
// to log in with.
func NeedsPassword(failedStep string) bool {
	return failedStep == StepPreflight || failedStep == StepInstallAccess
}

// Retry resumes provisioning of a failed server at the step that failed, as a
// new job linked to the failed one. After a failure before Proxier's key
// worked, rootPassword is required and replaces any kept one. overwriteDNS
// lets the DNS step replace a record Proxier did not create (the admin's
// explicit choice after a conflict). The server keeps the template version it
// was created with, whatever the default is now.
func (s *Service) Retry(ctx context.Context, serverID int64, rootPassword string, overwriteDNS bool, actor string) (int64, error) {
	srv, err := store.GetServer(ctx, s.DB.R, serverID)
	if err != nil {
		return 0, err
	}
	if srv.State != "failed" || !srv.ProvisionJobID.Valid || srv.Retiring() {
		return 0, ErrNotAllowed
	}
	opts := jobs.RetryOptions{}
	if NeedsPassword(srv.FailedStep) {
		if rootPassword == "" {
			return 0, FieldErrors{"root_password": {Key: "servers.err.password_required"}}
		}
		opts.Secrets = map[string]string{rootPasswordSecret: rootPassword}
	}
	opts.Payload = map[string]any{"overwrite_dns": overwriteDNS}

	var newID int64
	err = s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		if newID, err = s.Jobs.RetryWithTx(ctx, tx, srv.ProvisionJobID.Int64, actor, opts); err != nil {
			return err
		}
		if err := store.MarkProvisioning(ctx, tx, serverID, newID); err != nil {
			return err
		}
		// The deployment the failure closed is reopened for the new job: a
		// retry that carries upload-files over reaches activate with it.
		return store.ReopenDeployment(ctx, tx, serverID, newID, "provision")
	})
	if err != nil {
		return 0, err
	}
	s.Jobs.Kick()
	return newID, nil
}

// ActivateAnyway makes a server active whose provisioning stopped only at the
// smoke test: a hosting network blocked in Russia can still serve users
// abroad. Its health then comes from the checks, never healthy by default.
func (s *Service) ActivateAnyway(ctx context.Context, serverID int64, actor string) error {
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		srv, err := store.GetServer(ctx, tx, serverID)
		if err != nil {
			return err
		}
		if srv.State != "failed" || srv.FailedStep != StepSmokeTest || srv.Retiring() {
			return ErrNotAllowed
		}
		return s.activateTx(ctx, tx, srv, actor, map[string]any{
			"forced": true, "proxy_test": map[string]any{"error": srv.FailedError},
		})
	})
}
