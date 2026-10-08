package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Sample is a raw metric sample; the counters are what the next delta starts from.
type Sample struct {
	ServerID   int64           `db:"server_id"`
	At         db.Time         `db:"at"`
	Load1      float64         `db:"load1"`
	CPUPct     sql.NullFloat64 `db:"cpu_pct"`
	MemUsed    int64           `db:"mem_used"`
	MemTotal   int64           `db:"mem_total"`
	DiskUsed   int64           `db:"disk_used"`
	DiskTotal  int64           `db:"disk_total"`
	RXBytes    sql.NullInt64   `db:"rx_bytes"`
	TXBytes    sql.NullInt64   `db:"tx_bytes"`
	CPUBusy    int64           `db:"cpu_busy"`
	CPUTotal   int64           `db:"cpu_total"`
	RXCounter  int64           `db:"rx_counter"`
	TXCounter  int64           `db:"tx_counter"`
	Uptime     float64         `db:"uptime"`
	Containers string          `db:"containers"`
}

const sampleColumns = `server_id, at, load1, cpu_pct, mem_used, mem_total, disk_used, disk_total, rx_bytes, tx_bytes,
	cpu_busy, cpu_total, rx_counter, tx_counter, uptime, containers`

// LastSample is the server's newest raw sample.
func LastSample(ctx context.Context, q sqlx.QueryerContext, serverID int64) (Sample, bool, error) {
	var s Sample
	err := sqlx.GetContext(ctx, q, &s, `SELECT `+sampleColumns+` FROM servers_metric_samples WHERE server_id = ? ORDER BY at DESC LIMIT 1`, serverID)
	if errors.Is(err, sql.ErrNoRows) {
		return Sample{}, false, nil
	}
	return s, err == nil, err
}

// InsertSample stores a raw sample; a second one at the same instant replaces it.
func InsertSample(ctx context.Context, tx sqlx.ExtContext, s Sample) error {
	if s.Containers == "" {
		s.Containers = "[]"
	}
	_, err := sqlx.NamedExecContext(ctx, tx, `INSERT OR REPLACE INTO servers_metric_samples (`+sampleColumns+`)
		VALUES (:server_id, :at, :load1, :cpu_pct, :mem_used, :mem_total, :disk_used, :disk_total, :rx_bytes, :tx_bytes,
			:cpu_busy, :cpu_total, :rx_counter, :tx_counter, :uptime, :containers)`, sampleArgs(s))
	return err
}

func sampleArgs(s Sample) map[string]any {
	return map[string]any{
		"server_id": s.ServerID, "at": s.At, "load1": s.Load1, "cpu_pct": s.CPUPct, "mem_used": s.MemUsed, "mem_total": s.MemTotal,
		"disk_used": s.DiskUsed, "disk_total": s.DiskTotal, "rx_bytes": s.RXBytes, "tx_bytes": s.TXBytes, "cpu_busy": s.CPUBusy,
		"cpu_total": s.CPUTotal, "rx_counter": s.RXCounter, "tx_counter": s.TXCounter, "uptime": s.Uptime, "containers": s.Containers,
	}
}

// Samples returns the raw samples of a server in [from, to], oldest first.
func Samples(ctx context.Context, q sqlx.QueryerContext, serverID int64, from, to db.Time) ([]Sample, error) {
	var out []Sample
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+sampleColumns+` FROM servers_metric_samples
		WHERE server_id = ? AND at >= ? AND at <= ? ORDER BY at`, serverID, from, to)
	return out, err
}

// Hourly is a rolled-up hour.
type Hourly struct {
	ServerID  int64           `db:"server_id"`
	Hour      db.Time         `db:"hour"`
	Samples   int             `db:"samples"`
	Load1     float64         `db:"load1"`
	CPUPct    sql.NullFloat64 `db:"cpu_pct"`
	MemUsed   int64           `db:"mem_used"`
	MemTotal  int64           `db:"mem_total"`
	DiskUsed  int64           `db:"disk_used"`
	DiskTotal int64           `db:"disk_total"`
	RXBytes   int64           `db:"rx_bytes"`
	TXBytes   int64           `db:"tx_bytes"`
}

const hourlyColumns = `server_id, hour, samples, load1, cpu_pct, mem_used, mem_total, disk_used, disk_total, rx_bytes, tx_bytes`

// HourlyRows returns a server's hourly rows with hour in [from, to], oldest first.
func HourlyRows(ctx context.Context, q sqlx.QueryerContext, serverID int64, from, to db.Time) ([]Hourly, error) {
	var out []Hourly
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+hourlyColumns+` FROM servers_metric_hourly
		WHERE server_id = ? AND hour >= ? AND hour <= ? ORDER BY hour`, serverID, from, to)
	return out, err
}

// GetHourly reads one hourly row.
func GetHourly(ctx context.Context, q sqlx.QueryerContext, serverID int64, hour db.Time) (Hourly, bool, error) {
	var h Hourly
	err := sqlx.GetContext(ctx, q, &h, `SELECT `+hourlyColumns+` FROM servers_metric_hourly WHERE server_id = ? AND hour = ?`, serverID, hour)
	if errors.Is(err, sql.ErrNoRows) {
		return Hourly{}, false, nil
	}
	return h, err == nil, err
}

// PutHourly stores an hourly row, replacing one of the same hour.
func PutHourly(ctx context.Context, tx sqlx.ExtContext, h Hourly) error {
	_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO servers_metric_hourly (`+hourlyColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, h.ServerID, h.Hour, h.Samples, h.Load1, h.CPUPct, h.MemUsed, h.MemTotal,
		h.DiskUsed, h.DiskTotal, h.RXBytes, h.TXBytes)
	return err
}

// SamplesBefore returns up to limit raw samples older than a time, oldest first.
func SamplesBefore(ctx context.Context, tx sqlx.ExtContext, before db.Time, limit int) ([]Sample, error) {
	var out []Sample
	err := sqlx.SelectContext(ctx, tx, &out, `SELECT `+sampleColumns+` FROM servers_metric_samples WHERE at < ? ORDER BY at, server_id LIMIT ?`, before, limit)
	return out, err
}

// DeleteSamples removes the given raw samples.
func DeleteSamples(ctx context.Context, tx sqlx.ExtContext, keys []Sample) error {
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM servers_metric_samples WHERE server_id = ? AND at = ?`, k.ServerID, k.At); err != nil {
			return err
		}
	}
	return nil
}

// PruneHourly deletes hourly rows older than a time.
func PruneHourly(ctx context.Context, tx sqlx.ExtContext, before db.Time) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM servers_metric_hourly WHERE hour < ?`, before)
	return err
}

// Traffic is received and sent bytes.
type Traffic struct {
	RX, TX int64
}

// TrafficSince sums a server's traffic from a time on, raw samples and hourly
// rows together (they never overlap: a rollup moves whole hours).
func TrafficSince(ctx context.Context, q sqlx.QueryerContext, serverID int64, from db.Time) (Traffic, error) {
	var t Traffic
	err := sqlx.GetContext(ctx, q, &t, `SELECT
		COALESCE((SELECT sum(rx_bytes) FROM servers_metric_samples WHERE server_id = ?1 AND at >= ?2), 0) +
		COALESCE((SELECT sum(rx_bytes) FROM servers_metric_hourly WHERE server_id = ?1 AND hour >= ?2), 0) AS rx,
		COALESCE((SELECT sum(tx_bytes) FROM servers_metric_samples WHERE server_id = ?1 AND at >= ?2), 0) +
		COALESCE((SELECT sum(tx_bytes) FROM servers_metric_hourly WHERE server_id = ?1 AND hour >= ?2), 0) AS tx`, serverID, from)
	return t, err
}

// LatestSamples returns the newest raw sample of every server that has one.
func LatestSamples(ctx context.Context, q sqlx.QueryerContext) ([]Sample, error) {
	var out []Sample
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+sampleColumns+` FROM servers_metric_samples s
		WHERE at = (SELECT max(at) FROM servers_metric_samples s2 WHERE s2.server_id = s.server_id)`)
	return out, err
}
