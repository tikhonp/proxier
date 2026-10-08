package retire

import "context"

// Finish runs the last step of a retirement again for a server, as a resumed
// job would after a crash.
func (s *Service) Finish(ctx context.Context, serverID int64, actor string) error {
	return s.finishServer(ctx, actor, payload{ServerID: serverID})
}
