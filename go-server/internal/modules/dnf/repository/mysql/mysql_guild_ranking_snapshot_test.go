package mysql

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestGuildRankingNarrowSnapshotPreservesRosterAndDeletedProof(t *testing.T) {
	db := &fakeSQLDB{rowsets: []fakeSQLRows{
		{rows: []fakeSQLRow{{values: []any{"1", "a", 0, 90, int64(0), int64(8)}}, {values: []any{"2", "a", 5, 80, int64(0), int64(7)}}}},
		{rows: []fakeSQLRow{{values: []any{"3", "a", 7, 90, int64(1), int64(9)}}}},
		{rows: []fakeSQLRow{{values: []any{"1", "raid_clear_v1_1_01234567890123456789012345678901", int64(100)}}}},
	}}
	got, err := readGuildRankingSnapshot(context.Background(), mysqlRouter{db: db, readDBs: []string{"primary"}}, []string{"a"}, []string{"1", "3"}, 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Accounts["a"]) != 2 || got.Accounts["a"][1].CharacterID != "2" || got.Members["3"].DeleteFlag != 1 || len(got.Members["1"].RaidClears) != 1 {
		t.Fatalf("snapshot=%+v", got)
	}
	if len(db.execs) != 3 {
		t.Fatalf("queries=%d", len(db.execs))
	}
	for _, call := range db.execs {
		for _, unrelated := range []string{"locations", "roster", "equipment", "FOR UPDATE"} {
			if strings.Contains(call.Query, unrelated) {
				t.Fatal("unrelated/locking read", call.Query)
			}
		}
	}
}

func TestGuildRankingReadsBoundedAccountBatches(t *testing.T) {
	db := &fakeSQLDB{}
	var accounts []string
	for i := 0; i < 1000; i++ {
		accounts = append(accounts, fmt.Sprint("account-", i))
	}
	if _, err := readGuildRankingSnapshot(context.Background(), mysqlRouter{db: db, readDBs: []string{"primary", "second"}}, accounts, nil, 32); err != nil {
		t.Fatal(err)
	}
	if len(db.execs) != 16 {
		t.Fatalf("1000 accounts on 2 shards queries=%d", len(db.execs))
	}
	for _, call := range db.execs {
		if len(call.Args) > guildRankingReadBatch {
			t.Fatal("unbounded parameters")
		}
	}
}
