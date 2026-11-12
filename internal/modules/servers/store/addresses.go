package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Address is an additional address of a server: another IP on the same
// machine, with its own client-facing hostname. The primary address lives on
// the server row; Position 1 is it, so an address's Position is also its
// 1-based index in what templates see as .Addresses.
type Address struct {
	ID        int64   `db:"id"`
	ServerID  int64   `db:"server_id"`
	Position  int     `db:"position"`
	IP        string  `db:"ip"`
	Hostname  string  `db:"hostname"`
	CreatedAt db.Time `db:"created_at"`
}

const addressSelect = `SELECT id, server_id, position, ip, hostname, created_at FROM servers_addresses`

// ServerAddresses returns the additional addresses of a server, in the order
// templates see them.
func ServerAddresses(ctx context.Context, q sqlx.QueryerContext, serverID int64) ([]Address, error) {
	var out []Address
	err := sqlx.SelectContext(ctx, q, &out, addressSelect+` WHERE server_id = ? ORDER BY position`, serverID)
	return out, err
}

// GetAddress returns one additional address of a server.
func GetAddress(ctx context.Context, q sqlx.QueryerContext, serverID, id int64) (Address, error) {
	var a Address
	err := sqlx.GetContext(ctx, q, &a, addressSelect+` WHERE server_id = ? AND id = ?`, serverID, id)
	return a, notFound(err)
}

// InsertAddress appends an address with the next position. The caller has
// checked the IP and hostname against the server and every other server.
func InsertAddress(ctx context.Context, tx sqlx.ExtContext, serverID int64, ip, hostname string, at db.Time) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO servers_addresses (server_id, position, ip, hostname, created_at)
		VALUES (?, (SELECT COALESCE(MAX(position), 1) + 1 FROM servers_addresses WHERE server_id = ?), ?, ?, ?)`,
		serverID, serverID, ip, hostname, at)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteAddress removes one additional address of a server.
func DeleteAddress(ctx context.Context, tx sqlx.ExtContext, serverID, id int64) (bool, error) {
	res, err := tx.ExecContext(ctx, `DELETE FROM servers_addresses WHERE server_id = ? AND id = ?`, serverID, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// AddressHolder names the non-retired server that has ip as an additional
// address.
func AddressHolder(ctx context.Context, q sqlx.QueryerContext, ip string) (string, bool, error) {
	var name string
	err := sqlx.GetContext(ctx, q, &name, `
		SELECT s.name FROM servers_addresses a JOIN servers_servers s ON s.id = a.server_id
		WHERE a.ip = ? AND s.state <> 'retired' LIMIT 1`, ip)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}

// AddressHostnameHolder names the non-retired server that has host as an
// additional address.
func AddressHostnameHolder(ctx context.Context, q sqlx.QueryerContext, host string) (string, bool, error) {
	var name string
	err := sqlx.GetContext(ctx, q, &name, `
		SELECT s.name FROM servers_addresses a JOIN servers_servers s ON s.id = a.server_id
		WHERE a.hostname = ? AND s.state <> 'retired' LIMIT 1`, host)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}
