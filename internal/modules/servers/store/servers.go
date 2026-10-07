package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// ErrIPInUse is returned when a non-retired server already has the IP.
var ErrIPInUse = errors.New("another server has this IP")

// Server is the row of a server, with its location and template names.
type Server struct {
	ID                 int64         `db:"id"`
	LocationID         int64         `db:"location_id"`
	Number             int           `db:"number"`
	Name               string        `db:"name"`
	IP                 string        `db:"ip"`
	SSHPort            int           `db:"ssh_port"`
	ManagementHostname string        `db:"management_hostname"`
	ProxyHostname      string        `db:"proxy_hostname"`
	State              string        `db:"state"`
	TemplateID         int64         `db:"template_id"`
	TemplateVersion    int           `db:"template_version"`
	Params             string        `db:"params"` // JSON of the non-secret parameters
	ParamsSecret       []byte        `db:"params_secret"`
	Notes              string        `db:"notes"`
	ProvisionJobID     sql.NullInt64 `db:"provision_job_id"`
	RetireJobID        sql.NullInt64 `db:"retire_job_id"`
	FailedStep         string        `db:"failed_step"`
	FailedError        string        `db:"failed_error"`
	Health             string        `db:"health"` // "" unless active
	HealthSince        db.Time       `db:"health_since"`
	HealthReason       string        `db:"health_reason"`
	CreatedAt          db.Time       `db:"created_at"`
	ActivatedAt        db.Time       `db:"activated_at"`
	RetiredAt          db.Time       `db:"retired_at"`

	LocationCode string `db:"location_code"`
	LocationName string `db:"location_name"`
	Country      string `db:"country"`
	TemplateName string `db:"template_name"`
}

const serverSelect = `SELECT s.id, s.location_id, s.number, s.name, s.ip, s.ssh_port, s.management_hostname, s.proxy_hostname,
	s.state, s.template_id, s.template_version, s.params, s.params_secret, s.notes, s.provision_job_id, s.retire_job_id,
	COALESCE(s.failed_step, '') AS failed_step, COALESCE(s.failed_error, '') AS failed_error,
	COALESCE(s.health, '') AS health, s.health_since, s.health_reason, s.created_at, s.activated_at, s.retired_at,
	l.code AS location_code, l.name AS location_name, l.country AS country, t.name AS template_name
	FROM servers_servers s
	JOIN servers_locations l ON l.id = s.location_id
	JOIN servers_templates t ON t.id = s.template_id`

// ServerSubject is the subject of a server's events and jobs.
func ServerSubject(id int64) events.Subject { return events.Subject{Type: "server", ID: itoa(id)} }

// GetServer returns one server.
func GetServer(ctx context.Context, q sqlx.QueryerContext, id int64) (Server, error) {
	var s Server
	return s, notFound(sqlx.GetContext(ctx, q, &s, serverSelect+` WHERE s.id = ?`, id))
}

// ListServers returns every server, newest first, retired ones included.
func ListServers(ctx context.Context, q sqlx.QueryerContext) ([]Server, error) {
	var out []Server
	err := sqlx.SelectContext(ctx, q, &out, serverSelect+` ORDER BY s.id DESC`)
	return out, err
}

// ServerByIP returns the non-retired server that has ip.
func ServerByIP(ctx context.Context, q sqlx.QueryerContext, ip string) (Server, bool, error) {
	var s Server
	err := sqlx.GetContext(ctx, q, &s, serverSelect+` WHERE s.ip = ? AND s.state <> 'retired'`, ip)
	if errors.Is(err, sql.ErrNoRows) {
		return Server{}, false, nil
	}
	return s, err == nil, err
}

// NewServer is what Create writes.
type NewServer struct {
	LocationID                        int64
	Number                            int
	Name, IP                          string
	SSHPort                           int
	ManagementHostname, ProxyHostname string
	TemplateID                        int64
	TemplateVersion                   int
	Params                            string // JSON
	ParamsSecret                      []byte // sealed under "server:<id>:params"; see SealParams
	Notes                             string
}

// InsertServer adds a server in state provisioning. The sealed parameters
// need the id for their AAD, so the caller passes a function that seals once
// the id is known; it may return nil for none.
func InsertServer(ctx context.Context, tx sqlx.ExtContext, n NewServer, at db.Time, sealParams func(id int64) []byte) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO servers_servers (location_id, number, name, ip, ssh_port, management_hostname, proxy_hostname, state,
			template_id, template_version, params, notes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'provisioning', ?, ?, ?, ?, ?)`,
		n.LocationID, n.Number, n.Name, n.IP, n.SSHPort, n.ManagementHostname, n.ProxyHostname,
		n.TemplateID, n.TemplateVersion, n.Params, n.Notes, at)
	if unique(err, "servers_servers.ip") {
		return 0, ErrIPInUse
	}
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if sealParams != nil {
		if blob := sealParams(id); blob != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE servers_servers SET params_secret = ? WHERE id = ?`, blob, id); err != nil {
				return 0, err
			}
		}
	}
	return id, nil
}

// SetProvisionJob records the job that provisions (or retries) a server.
func SetProvisionJob(ctx context.Context, tx sqlx.ExtContext, id, jobID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_servers SET provision_job_id = ? WHERE id = ?`, jobID, id)
	return err
}

// MarkFailed puts a provisioning server into state failed.
func MarkFailed(ctx context.Context, tx sqlx.ExtContext, id int64, step, errText string) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_servers SET state = 'failed', failed_step = ?, failed_error = ?
		WHERE id = ? AND state = 'provisioning'`, step, errText, id)
	return err
}

// MarkProvisioning takes a failed server back to provisioning for a retry,
// clearing the failure and pointing it at the new job.
func MarkProvisioning(ctx context.Context, tx sqlx.ExtContext, id, jobID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_servers SET state = 'provisioning', failed_step = NULL, failed_error = NULL,
		provision_job_id = ? WHERE id = ? AND state = 'failed'`, jobID, id)
	return err
}

// MarkActive makes a provisioning or failed server active: health unknown
// since at, the version in force recorded.
func MarkActive(ctx context.Context, tx sqlx.ExtContext, id int64, version int, at db.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_servers SET state = 'active', health = 'unknown', health_since = ?, health_reason = '',
		activated_at = ?, template_version = ?, failed_step = NULL, failed_error = NULL
		WHERE id = ? AND state IN ('provisioning', 'failed')`, at, at, version, id)
	return err
}

// SetNotes replaces a server's notes and reports whether they changed.
func SetNotes(ctx context.Context, tx sqlx.ExtContext, id int64, notes string) (changed bool, err error) {
	res, err := tx.ExecContext(ctx, `UPDATE servers_servers SET notes = ? WHERE id = ? AND notes <> ?`, notes, id, notes)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// StateCount is how many servers are in a lifecycle state.
type StateCount struct {
	State string `db:"state"`
	N     int    `db:"n"`
}

// CountByState counts servers by lifecycle state.
func CountByState(ctx context.Context, q sqlx.QueryerContext) (map[string]int, error) {
	var rows []StateCount
	if err := sqlx.SelectContext(ctx, q, &rows, `SELECT state, count(*) AS n FROM servers_servers GROUP BY state`); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range rows {
		out[r.State] = r.N
	}
	return out, nil
}

// --- generated values

// GeneratedValue is a sealed generated value.
type GeneratedValue struct {
	Key       string  `db:"key"`
	Value     []byte  `db:"value"`
	Pending   []byte  `db:"pending"`
	CreatedAt db.Time `db:"created_at"`
	RotatedAt db.Time `db:"rotated_at"`
}

// GeneratedValues returns a server's sealed generated values.
func GeneratedValues(ctx context.Context, q sqlx.QueryerContext, serverID int64) ([]GeneratedValue, error) {
	var out []GeneratedValue
	err := sqlx.SelectContext(ctx, q, &out, `SELECT key, value, pending, created_at, rotated_at FROM servers_generated_values
		WHERE server_id = ? ORDER BY key`, serverID)
	return out, err
}

// PutGenerated stores a value that does not exist yet and leaves an existing
// one alone, so a repeated step never replaces what a deployment uses.
func PutGenerated(ctx context.Context, tx sqlx.ExtContext, serverID int64, key string, sealed []byte, at db.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO servers_generated_values (server_id, key, value, created_at) VALUES (?, ?, ?, ?)`,
		serverID, key, sealed, at)
	return err
}

// --- endpoints

// EndpointRow is a stored endpoint; Secret is sealed JSON {"credential","params"}.
type EndpointRow struct {
	ServerID    int64   `db:"server_id"`
	Key         string  `db:"key"`
	Type        string  `db:"type"`
	Host        string  `db:"host"`
	Port        int     `db:"port"`
	Secret      []byte  `db:"secret"`
	DisplayName string  `db:"display_name"`
	Position    int     `db:"position"`
	UpdatedAt   db.Time `db:"updated_at"`
}

// Endpoints returns a server's endpoints in manifest order.
func Endpoints(ctx context.Context, q sqlx.QueryerContext, serverID int64) ([]EndpointRow, error) {
	var out []EndpointRow
	err := sqlx.SelectContext(ctx, q, &out, `SELECT server_id, key, type, host, port, secret, display_name, position, updated_at
		FROM servers_endpoints WHERE server_id = ? ORDER BY position`, serverID)
	return out, err
}

// ReplaceEndpoints makes rows the server's endpoints.
func ReplaceEndpoints(ctx context.Context, tx sqlx.ExtContext, serverID int64, rows []EndpointRow, at db.Time) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM servers_endpoints WHERE server_id = ?`, serverID); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO servers_endpoints (server_id, key, type, host, port, secret, display_name, position, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, serverID, r.Key, r.Type, r.Host, r.Port, r.Secret, r.DisplayName, r.Position, at); err != nil {
			return err
		}
	}
	return nil
}

// --- deployments

// Deployment is one deployment of a server.
type Deployment struct {
	ID              int64         `db:"id"`
	ServerID        int64         `db:"server_id"`
	Kind            string        `db:"kind"`
	TemplateVersion int           `db:"template_version"`
	Params          string        `db:"params"`
	ParamsSecret    []byte        `db:"params_secret"`
	JobID           sql.NullInt64 `db:"job_id"`
	State           string        `db:"state"`
	Uploaded        bool          `db:"uploaded"`
	FilesChanged    int           `db:"files_changed"`
	StartedAt       db.Time       `db:"started_at"`
	FinishedAt      db.Time       `db:"finished_at"`
	Error           string        `db:"error"`
}

const deploymentSelect = `SELECT id, server_id, kind, template_version, params, params_secret, job_id, state, uploaded, files_changed,
	started_at, finished_at, COALESCE(error, '') AS error FROM servers_deployments`

// InsertDeployment adds a running deployment and returns its id.
func InsertDeployment(ctx context.Context, tx sqlx.ExtContext, d Deployment, at db.Time) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO servers_deployments (server_id, kind, template_version, params, job_id, state, uploaded, files_changed, started_at)
		VALUES (?, ?, ?, ?, ?, 'running', ?, ?, ?)`, d.ServerID, d.Kind, d.TemplateVersion, d.Params, d.JobID, d.Uploaded, d.FilesChanged, at)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RunningDeployment is the server's running deployment of a kind, if any.
func RunningDeployment(ctx context.Context, q sqlx.QueryerContext, serverID int64, kind string) (Deployment, bool, error) {
	var d Deployment
	err := sqlx.GetContext(ctx, q, &d, deploymentSelect+` WHERE server_id = ? AND kind = ? AND state = 'running' ORDER BY id DESC LIMIT 1`, serverID, kind)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, false, nil
	}
	return d, err == nil, err
}

// CurrentDeployment is the server's latest succeeded deployment that uploaded
// files: its current files (README of Phase 1, "Current files"). Never just
// the latest one: restart, images and reboot upload nothing.
func CurrentDeployment(ctx context.Context, q sqlx.QueryerContext, serverID int64) (Deployment, bool, error) {
	var d Deployment
	err := sqlx.GetContext(ctx, q, &d, deploymentSelect+` WHERE server_id = ? AND state = 'succeeded' AND uploaded = 1 ORDER BY id DESC LIMIT 1`, serverID)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, false, nil
	}
	return d, err == nil, err
}

// SetDeploymentUploaded marks that the deployment's files are stored.
func SetDeploymentUploaded(ctx context.Context, tx sqlx.ExtContext, id int64, filesChanged int) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_deployments SET uploaded = 1, files_changed = ? WHERE id = ?`, filesChanged, id)
	return err
}

// FinishDeployment closes a running deployment as succeeded or failed.
func FinishDeployment(ctx context.Context, tx sqlx.ExtContext, id int64, state, errText string, at db.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_deployments SET state = ?, finished_at = ?, error = NULLIF(?, '') WHERE id = ? AND state = 'running'`,
		state, at, errText, id)
	return err
}

// FailRunningDeployments fails the server's running deployments of the job.
func FailRunningDeployments(ctx context.Context, tx sqlx.ExtContext, serverID int64, errText string, at db.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_deployments SET state = 'failed', finished_at = ?, error = ?
		WHERE server_id = ? AND state = 'running'`, at, errText, serverID)
	return err
}

// ReopenDeployment takes a failed deployment of a retried job back to running.
func ReopenDeployment(ctx context.Context, tx sqlx.ExtContext, serverID, jobID int64, kind string) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_deployments SET state = 'running', job_id = ?, error = NULL, finished_at = NULL
		WHERE id = (SELECT id FROM servers_deployments WHERE server_id = ? AND kind = ? AND state = 'failed' ORDER BY id DESC LIMIT 1)`,
		jobID, serverID, kind)
	return err
}

// FailedDeployment is the server's latest failed deployment of a kind.
func FailedDeployment(ctx context.Context, q sqlx.QueryerContext, serverID int64, kind string) (Deployment, bool, error) {
	var d Deployment
	err := sqlx.GetContext(ctx, q, &d, deploymentSelect+` WHERE server_id = ? AND kind = ? AND state = 'failed' ORDER BY id DESC LIMIT 1`, serverID, kind)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, false, nil
	}
	return d, err == nil, err
}

// DeployedFile is a file a deployment rendered; Content is sealed.
type DeployedFile struct {
	DeploymentID int64  `db:"deployment_id"`
	Path         string `db:"path"`
	Mode         int    `db:"mode"`
	SHA256       string `db:"sha256"`
	Content      []byte `db:"content"`
}

// DeployedFiles returns a deployment's files.
func DeployedFiles(ctx context.Context, q sqlx.QueryerContext, deploymentID int64) ([]DeployedFile, error) {
	var out []DeployedFile
	err := sqlx.SelectContext(ctx, q, &out, `SELECT deployment_id, path, mode, sha256, content FROM servers_deployed_files
		WHERE deployment_id = ? ORDER BY path`, deploymentID)
	return out, err
}

// ReplaceDeployedFiles makes files the deployment's files.
func ReplaceDeployedFiles(ctx context.Context, tx sqlx.ExtContext, deploymentID int64, files []DeployedFile) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM servers_deployed_files WHERE deployment_id = ?`, deploymentID); err != nil {
		return err
	}
	for _, f := range files {
		if _, err := tx.ExecContext(ctx, `INSERT INTO servers_deployed_files (deployment_id, path, mode, sha256, content) VALUES (?, ?, ?, ?, ?)`,
			deploymentID, f.Path, f.Mode, f.SHA256, f.Content); err != nil {
			return err
		}
	}
	return nil
}

// --- DNS records

// DNSRecord is a record Proxier made for a server.
type DNSRecord struct {
	ID        int64          `db:"id"`
	ServerID  int64          `db:"server_id"`
	Provider  string         `db:"provider"`
	ZoneID    string         `db:"zone_id"`
	Zone      string         `db:"zone"`
	Name      string         `db:"name"`
	Type      string         `db:"type"`
	Content   string         `db:"content"`
	RecordID  string         `db:"record_id"`
	CreatedAt db.Time        `db:"created_at"`
	DeletedAt db.Time        `db:"deleted_at"`
	Kept      sql.NullString `db:"kept"`
}

// DNSRecords returns the server's records that are not deleted.
func DNSRecords(ctx context.Context, q sqlx.QueryerContext, serverID int64) ([]DNSRecord, error) {
	var out []DNSRecord
	err := sqlx.SelectContext(ctx, q, &out, `SELECT id, server_id, provider, zone_id, zone, name, type, content, record_id, created_at, deleted_at, kept
		FROM servers_dns_records WHERE server_id = ? AND deleted_at IS NULL ORDER BY id`, serverID)
	return out, err
}

// UpsertDNSRecord stores the record of a name: a retry updates the row, never
// adds a second record for the same name.
func UpsertDNSRecord(ctx context.Context, tx sqlx.ExtContext, r DNSRecord, at db.Time) error {
	res, err := tx.ExecContext(ctx, `UPDATE servers_dns_records SET provider = ?, zone_id = ?, zone = ?, content = ?, record_id = ?
		WHERE server_id = ? AND name = ? AND type = ? AND deleted_at IS NULL`,
		r.Provider, r.ZoneID, r.Zone, r.Content, r.RecordID, r.ServerID, r.Name, r.Type)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO servers_dns_records (server_id, provider, zone_id, zone, name, type, content, record_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.ServerID, r.Provider, r.ZoneID, r.Zone, r.Name, r.Type, r.Content, r.RecordID, at)
	return err
}

// NextNumber is the number the next server of a location would get, without
// reserving it ("if created now" in the form's summary).
func NextNumber(ctx context.Context, q sqlx.QueryerContext, locationID int64) (int, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT last_number + 1 FROM servers_locations WHERE id = ?`, locationID)
	return n, notFound(err)
}

// SetDeploymentParamsSecret stores a deployment's sealed secret parameters.
func SetDeploymentParamsSecret(ctx context.Context, tx sqlx.ExtContext, id int64, blob []byte) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_deployments SET params_secret = ? WHERE id = ?`, blob, id)
	return err
}

// ParamsOf is a deployment's or server's non-secret parameters.
func ParseParams(raw string) map[string]string {
	out := map[string]string{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// MaxNotes is the longest note, in characters.
const MaxNotes = 10000

// SetServerNotes saves a server's notes and records server.notes_changed, in
// one transaction, only when they changed.
func (s *Store) SetServerNotes(ctx context.Context, id int64, notes, actor string) error {
	if utf8.RuneCountInString(notes) > MaxNotes {
		return FieldErrors{"notes": "servers.err.notes_long"}
	}
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := GetServer(ctx, tx, id); err != nil {
			return err
		}
		changed, err := SetNotes(ctx, tx, id, notes)
		if err != nil || !changed {
			return err
		}
		_, err = s.Events.Record(ctx, tx, events.Event{Type: "server.notes_changed", Subject: ServerSubject(id), Actor: actor})
		return err
	})
}
