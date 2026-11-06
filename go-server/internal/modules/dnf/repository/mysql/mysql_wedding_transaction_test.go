package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestMySQLWeddingAssetsUseOneWriteTransactionAndExactPairScope(t *testing.T) {
	for _, scenario := range []string{"success", "callback_failure", "commit_failure", "settings_escape", "panic", "wrong_pair"} {
		t.Run(scenario, func(t *testing.T) {
			state := &inventoryGroupSQLState{}
			if scenario == "commit_failure" {
				state.commitErr = errors.New("commit failed")
			}
			db := sql.OpenDB(inventoryGroupConnector{state})
			defer db.Close()
			u := &mysqlCharacterTradeUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"read_only"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_"}}
			ctx := context.Background()
			scope, err := r.WeddingPairScope([]string{"2", "10"})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "wrong_pair" {
				scope, _ = r.WeddingPairScope([]string{"2", "3"})
			}
			called := false
			invoke := func() error {
				return u.WithinWeddingAssets(ctx, []string{"2", "10"}, scope, func(chars r.CharacterRepository, bags r.InventoryRepository, settings r.SettingsRepository) error {
					called = true
					for _, id := range []string{"10", "2"} {
						if _, _, err := chars.Load(ctx, id); err != nil {
							return err
						}
						if _, _, err := bags.Load(ctx, id); err != nil {
							return err
						}
					}
					if _, _, err := chars.Load(ctx, "3"); err == nil {
						t.Fatal("unrelated character reached SQL")
					}
					if scenario == "settings_escape" {
						return settings.Save(ctx, r.SettingsRecord{Scope: "character-pair:wedding:v1:2:3", Values: map[string]string{"room": "bad"}})
					}
					if err := r.SaveCharacterFields(ctx, chars, r.CharacterRecord{CharacterID: "10", Stats: map[string]int64{"gold": 7}}, r.CharacterFieldStats); err != nil {
						return err
					}
					if err := settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"room": "owned"}}); err != nil {
						return err
					}
					if scenario == "callback_failure" {
						return errors.New("second owner rejected")
					}
					if scenario == "panic" {
						panic("callback panic")
					}
					return nil
				})
			}
			if scenario == "panic" {
				func() {
					defer func() {
						if recover() == nil {
							t.Error("expected callback panic")
						}
					}()
					_ = invoke()
				}()
			} else {
				err = invoke()
				if (err == nil) != (scenario == "success") {
					t.Fatalf("scenario %s err=%v", scenario, err)
				}
			}
			if scenario == "wrong_pair" {
				if called || state.begins != 0 || state.writes != 0 {
					t.Fatal("mismatched pair reached transaction")
				}
				return
			}
			wantCommit, wantRollback := 0, 1
			if scenario == "success" || scenario == "commit_failure" {
				wantCommit, wantRollback = 1, 0
			}
			if state.begins != 1 || state.commits != wantCommit || state.rollbacks != wantRollback {
				t.Fatalf("transaction counts %+v", state)
			}
			if scenario == "settings_escape" && state.writes != 1 {
				t.Fatal("out of scope room write reached SQL")
			}
			if len(state.queries) == 0 {
				t.Fatal("missing settings row lock")
			}
			for _, query := range state.queries {
				if strings.Contains(query, "read_only") || !strings.HasSuffix(query, "FOR UPDATE") {
					t.Fatalf("replica or unlocked read: %s", query)
				}
			}
		})
	}
}
