package provision

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"

	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/gen"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func nullInt(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }

func flagOf(cc string) string { return country.Flag(cc) }

// ensureGenerated makes the generated values the version declares and the
// server lacks; existing ones are returned as they are.
func ensureGenerated(d *data) (all map[string]string, created []string, err error) {
	return gen.Ensure(d.Man.Generated, d.Gen)
}
