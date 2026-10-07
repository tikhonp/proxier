package deploy

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func nullInt(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }
