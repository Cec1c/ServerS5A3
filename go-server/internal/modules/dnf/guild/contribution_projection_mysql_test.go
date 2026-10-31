package guild

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

func TestHongbaoContributionProjectionMySQLWriterTransaction(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid disposable DSN")
	}
	cfg.DBName, cfg.ParseTime = "", true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open disposable DB")
	}
	defer db.Close()
	ctx := context.Background()
	prefix := fmt.Sprintf("hongbao_projection_test_%d", time.Now().UnixNano())
	plan := r.DatabasePlan{ReadDatabases: []string{prefix + "_missing_replica"}, WriteDatabases: []string{prefix + "_a", prefix + "_b"}}
	if err := repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: r.DatabasePlan{WriteDatabases: plan.WriteDatabases, ReadDatabases: plan.WriteDatabases}}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, name := range plan.WriteDatabases {
			if _, err := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
				t.Error(err)
			}
		}
	}()
	group, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	testHongbaoContributionProjection(t, group)
}
