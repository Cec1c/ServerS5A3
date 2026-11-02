package mysql

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestGuildReadCharactersBatchBoundedByShards(t *testing.T) {
	for _, n := range []int{191, 1000} {
		for _, mode := range []string{"on", "off"} {
			t.Run(fmt.Sprintf("%d/%s", n, mode), func(t *testing.T) {
				t.Setenv("DNFBRIDGE_CHARACTER_MIRROR_STATS_AUTHORITATIVE", mode)
				db := &fakeSQLDB{}
				reader := &mysqlGuildCharacters{router: mysqlRouter{db: db, writeDBs: []string{"writer_a", "writer_b"}, readDBs: []string{"wrong_replica"}}}
				ids := make([]string, n)
				rows := make([]fakeSQLRows, 2)
				for i := range ids {
					id := fmt.Sprint(i + 1)
					ids[i] = id
					shard := pickDatabase(id, 2)
					rows[shard].rows = append(rows[shard].rows, fakeSQLRow{values: []any{id, "account" + id, "member" + id, "1", 90, int64(33), int64(0)}})
				}
				db.rowsets = rows
				got, err := reader.LoadGuildCharacters(context.Background(), ids)
				if err != nil || len(got) != n || len(db.execs) != 2 {
					t.Fatalf("members=%d queries=%d err=%v", len(got), len(db.execs), err)
				}
				args := 0
				for _, call := range db.execs {
					args += len(call.Args)
					if !strings.HasPrefix(call.Query, "SELECT c.character_id, c.account_id, c.name, c.job, c.level,") || strings.Contains(call.Query, "FOR UPDATE") || strings.Contains(call.Query, "wrong_replica") || strings.Contains(call.Query, "inventory") {
						t.Fatal("non-projection query", call.Query)
					}
					if strings.Contains(call.Query, "LEFT JOIN") != (mode == "off") {
						t.Fatal("mirror compatibility mode lost", call.Query)
					}
				}
				if args != n || got["1"].GrowType != 33 || got["1"].Level != 90 {
					t.Fatal("identity/fields lost")
				}
				t.Logf("members=%d queries=%d bound_parameters=%d", n, len(db.execs), args)
			})
		}
	}
}

func TestGuildReadCharactersRejectsBadIdentityAndDeduplicates(t *testing.T) {
	for _, ids := range [][]string{{""}, {" 1"}, make([]string, 1001)} {
		db := &fakeSQLDB{}
		r := &mysqlGuildCharacters{router: mysqlRouter{db: db, writeDBs: []string{"a"}}}
		if _, err := r.LoadGuildCharacters(context.Background(), ids); err == nil || len(db.execs) != 0 {
			t.Fatal("invalid batch reached SQL")
		}
	}
	for _, returned := range [][]string{{"2"}, {"1", "1"}, {"1"}} {
		db := &fakeSQLDB{}
		rows := fakeSQLRows{}
		for _, id := range returned {
			rows.rows = append(rows.rows, fakeSQLRow{values: []any{id, "a", "n", "1", 90, int64(0), int64(0)}})
		}
		db.rowsets = []fakeSQLRows{rows}
		r := &mysqlGuildCharacters{router: mysqlRouter{db: db, writeDBs: []string{"a"}}}
		_, err := r.LoadGuildCharacters(context.Background(), []string{"1", "1"})
		valid := len(returned) == 1 && returned[0] == "1"
		if (err == nil) != valid || len(db.execs) != 1 || len(db.execs[0].Args) != 1 {
			t.Fatalf("returned=%v err=%v queries=%v", returned, err, db.execs)
		}
	}
}
