package mtvpn

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/shadowrocket"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Choices are what the admin picked on the preview.
type Choices struct {
	Include      map[int]bool // by row index
	Convert      map[int]bool
	ListID       int64
	CreateConfig bool
	ConfigName   string
	ConfigList   int64
}

// Run saves the choices and queues the import job. Errors are
// store.FieldErrors (list, config_name, config_list), ErrNotFound or
// ErrNotPreview (it ran already).
func (s *Service) Run(ctx context.Context, id int64, c Choices, actor string) (int64, error) {
	var jobID int64
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		r, err := store.GetImport(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if r.State != "preview" {
			return ErrNotPreview
		}
		im, err := fromRow(r)
		if err != nil {
			return err
		}
		fe := store.FieldErrors{}
		if _, err := store.GetList(ctx, tx, c.ListID); errors.Is(err, store.ErrNotFound) {
			fe["list"] = "mtvpn.err.list"
		} else if err != nil {
			return err
		}
		for i := range im.Rows {
			row := &im.Rows[i]
			row.Include = row.Status != StatusInvalid && c.Include[i]
			row.Convert = row.URL && c.Convert[i]
		}
		sr := &im.Shadowrocket
		sr.Create = sr.Offered && c.CreateConfig
		if sr.Create {
			sr.Name = shadowrocket.CleanName(c.ConfigName)
			if err := shadowrocket.CheckName(ctx, tx, sr.Name, 0, "config_name", fe); err != nil {
				return err
			}
			sr.ListID = c.ConfigList
			if _, err := store.GetList(ctx, tx, c.ConfigList); errors.Is(err, store.ErrNotFound) {
				fe["config_list"] = "mtvpn.err.list"
			} else if err != nil {
				return err
			}
		}
		if len(fe) > 0 {
			return fe
		}
		e, err := s.d.Jobs.Enqueue(ctx, tx, jobs.Request{Type: JobImport, Payload: payload{ImportID: id}, CreatedBy: actor})
		if err != nil {
			return err
		}
		jobID = e.ID
		rows, err := json.Marshal(im.Rows)
		if err != nil {
			return err
		}
		srJSON, err := json.Marshal(sr)
		if err != nil {
			return err
		}
		return store.StartImport(ctx, tx, id, c.ListID, string(rows), string(srJSON), jobID)
	})
	if err != nil {
		return 0, err
	}
	s.d.Jobs.Kick()
	return jobID, nil
}
