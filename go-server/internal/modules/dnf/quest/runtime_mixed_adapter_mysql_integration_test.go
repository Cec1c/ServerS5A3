//go:build mysql_integration

package quest

import (
	"database/sql"
	dbrepo "longheng.io/server/internal/modules/dnf/repository/mysql"
)

func mixedSQLAdapter(db *sql.DB) (dbrepo.SQLDB, func()) {
	a := dbrepo.NewPreparedSQLDB(db, 64)
	return a, func() { _ = a.Close() }
}
