//go:build mysql_integration

package mysql

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	driver "github.com/go-sql-driver/mysql"
	"net"
	"os"
	"testing"
	"time"
)

func TestPreparedSQLDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("disposable database required")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || cfg.Net != "tcp" || host != "127.0.0.1" || cfg.DBName != "" {
		t.Fatal("loopback without schema required")
	}
	name := fmt.Sprintf("prepared_ab_%d", time.Now().UnixNano())
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err = admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4"); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP DATABASE `" + name + "`")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, mode := range []string{"ordinary", "prepared", "interpolate"} {
		t.Run(mode, func(t *testing.T) {
			c := cfg.Clone()
			c.DBName = name
			c.ParseTime = true
			c.InterpolateParams = mode == "interpolate"
			db, err := sql.Open("mysql", c.FormatDSN())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
			table := "`" + mode + "`"
			if _, err = db.Exec("CREATE TABLE " + table + " (id INT PRIMARY KEY, txt TEXT, raw LONGBLOB, val BIGINT UNSIGNED, nullable TEXT NULL, stamp DATETIME(6))"); err != nil {
				t.Fatal(err)
			}
			var adapter SQLDB = SQLDBAdapter{db}
			var prepared *PreparedSQLDB
			if mode == "prepared" {
				prepared = NewPreparedSQLDB(db, 8)
				adapter = prepared
				defer prepared.Close()
			}
			text := "中文'\\?\x00结束"
			raw := []byte{0, 1, 39, 92, 63, 255}
			big := uint64(1<<63) + 17
			now := time.Date(2026, 10, 31, 0, 1, 2, 123456000, time.UTC)
			insert := "INSERT INTO " + table + " (id,txt,raw,val,nullable,stamp) VALUES (?,?,?,?,?,?)"
			selectQ := "SELECT txt,raw,val,nullable,stamp FROM " + table + " WHERE id=?"
			stat := func() int64 {
				var key string
				var n int64
				if err := db.QueryRow("SHOW SESSION STATUS LIKE 'Com_stmt_prepare'").Scan(&key, &n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			before := stat()
			start := time.Now()
			for i := 0; i < 100; i++ {
				if _, err := adapter.ExecContext(ctx, insert, i, text, raw, big, nil, now); err != nil {
					t.Fatal(err)
				}
				var got string
				var blob []byte
				var n uint64
				var null sql.NullString
				var stamp time.Time
				if err := adapter.QueryRowContext(ctx, selectQ, i).Scan(&got, &blob, &n, &null, &stamp); err != nil {
					t.Fatal(err)
				}
				if got != text || !bytes.Equal(blob, raw) || n != big || null.Valid || !stamp.Equal(now) {
					t.Fatalf("parameter roundtrip mismatch %q %v %d %v %v", got, blob, n, null, stamp)
				}
			}
			count := stat() - before
			t.Logf("mode=%s operations=200 prepares=%d elapsed=%s", mode, count, time.Since(start))
			if mode == "prepared" && count != 2 {
				t.Fatalf("prepared count=%d", count)
			}
			if mode == "interpolate" && count != 0 {
				t.Fatalf("interpolate count=%d", count)
			}
			if mode == "ordinary" && count != 200 {
				t.Fatalf("ordinary count=%d", count)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			txDB := newSQLTransactionDB(tx, adapter)
			for i := 100; i < 103; i++ {
				if _, err := txDB.ExecContext(ctx, insert, i, text, raw, big, nil, now); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
			}
			var got string
			var blob []byte
			var n uint64
			var null sql.NullString
			var stamp time.Time
			if err := txDB.QueryRowContext(ctx, selectQ, 100).Scan(&got, &blob, &n, &null, &stamp); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			var rows int
			if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&rows); err != nil || rows != 100 {
				t.Fatalf("rollback rows=%d err=%v", rows, err)
			}
			if prepared != nil {
				// Force physical reconnection; database/sql must prepare the retained
				// query on the replacement connection without losing result semantics.
				db.SetMaxIdleConns(0)
				db.SetMaxIdleConns(1)
				if err := adapter.QueryRowContext(ctx, selectQ, 0).Scan(&got, &blob, &n, &null, &stamp); err != nil {
					t.Fatal(err)
				}
				if got != text || !bytes.Equal(blob, raw) {
					t.Fatal("reconnect mismatch")
				}
			}
		})
	}
}
