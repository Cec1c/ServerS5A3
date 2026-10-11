package mysql

import (
	"context"
	"database/sql"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestMailboxSQLPhysicalRollbackAndUnknownCommit(t *testing.T) {
	for _, mode := range []string{"success", "error", "panic", "cancel", "commit-unknown"} {
		t.Run(mode, func(t *testing.T) {
			state := &semesterSQLState{scenario: mode}
			db := sql.OpenDB(semesterSQLConnector{state})
			defer db.Close()
			u := &mysqlMailboxAssetUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			token := new(int)
			var caught any
			var err error
			func() {
				defer func() { caught = recover() }()
				err = u.WithinMailboxAssets(ctx, "1", "1", func(c repo.CharacterRepository, i repo.InventoryRepository, m repo.MailboxRepository) error {
					ch, found, e := c.Load(ctx, "1")
					if e != nil || !found {
						return errors.New("locked load failed")
					}
					ch.Stats = map[string]int64{"changed": 1}
					if e = c.Save(ctx, ch); e != nil {
						return e
					}
					if e = m.Save(ctx, repo.MailboxRecord{CharacterID: "1", Mails: map[string]repo.MailRecord{"one": {MailID: "one", Title: "one"}}}); e != nil {
						return e
					}
					switch mode {
					case "panic":
						panic(token)
					case "error":
						return errors.New("after physical writes")
					case "cancel":
						cancel()
					}
					return nil
				})
			}()
			if state.begins != 1 || state.writes < 2 {
				t.Fatalf("physical writes not exercised: %+v err=%v", state, err)
			}
			switch mode {
			case "success":
				if err != nil || caught != nil || state.commits != 1 || state.rollbacks != 0 {
					t.Fatal(err, caught, state)
				}
			case "commit-unknown":
				if err == nil || caught != nil || state.commits != 1 || state.rollbacks != 0 {
					t.Fatal("unknown outcome misrepresented", err, caught, state)
				}
			default:
				if mode == "panic" {
					if caught != token {
						t.Fatal("original panic changed", caught)
					}
				} else if err == nil || caught != nil {
					t.Fatal(err, caught)
				}
				if state.commits != 0 || state.rollbacks != 1 {
					t.Fatal("physical transaction leaked", state)
				}
			}
		})
	}
}
