package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestMySQLCharacterSettlementLocksAccountBeforeCharacter(t *testing.T) {
	state := &raidLedgerSQLState{characterAccount: "a"}
	database := sql.OpenDB(raidLedgerSQLConnector{state: state})
	defer database.Close()
	settlement := &mysqlCharacterSettlementUnitOfWork{router: mysqlRouter{
		db:          SQLDBAdapter{DB: database},
		readDBs:     []string{"readonly"},
		writeDBs:    []string{"shard_a", "shard_b"},
		tablePrefix: "dnf",
		now:         time.Now,
	}}

	callbackRan := false
	if err := settlement.WithinCharacterSettlement(context.Background(), "19", func(tx repo.Group) error {
		callbackRan = true
		account, found, err := tx.Account.Load(context.Background(), "a")
		if err != nil {
			return err
		}
		if !found || account.AccountID != "a" {
			return fmt.Errorf("account=%+v found=%t", account, found)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !callbackRan || state.begins != 1 || state.commits != 1 || state.rollbacks != 0 || state.outsideTxOperations != 0 {
		t.Fatalf("settlement state=%+v callback=%t", state, callbackRan)
	}
	if len(state.queries) == 0 ||
		!strings.Contains(state.queries[0], "`dnf_"+mysqlCharTable+"`") ||
		strings.HasSuffix(state.queries[0], " FOR UPDATE") {
		t.Fatalf("first query must be the non-locking character ownership lookup: %v", state.queries)
	}
	firstLocked := -1
	characterLocked := -1
	for index, query := range state.queries {
		if !strings.HasSuffix(query, " FOR UPDATE") {
			continue
		}
		if firstLocked < 0 {
			firstLocked = index
		}
		if strings.Contains(query, "`dnf_"+mysqlCharTable+"`") {
			characterLocked = index
			break
		}
	}
	if firstLocked < 0 || !strings.Contains(state.queries[firstLocked], "`dnf_"+mysqlAccountTable+"`") {
		t.Fatalf("first locked query is not account: %v", state.queries)
	}
	if characterLocked <= firstLocked {
		t.Fatalf("character lock index=%d first=%d queries=%v", characterLocked, firstLocked, state.queries)
	}
}

func TestMySQLGuildSettlementLocksRegistryThenAccountThenCharacter(t *testing.T) {
	state := &raidLedgerSQLState{characterAccount: "a"}
	database := sql.OpenDB(raidLedgerSQLConnector{state: state})
	defer database.Close()
	settlement := &mysqlCharacterSettlementUnitOfWork{router: mysqlRouter{
		db:          SQLDBAdapter{DB: database},
		readDBs:     []string{"readonly"},
		writeDBs:    []string{"shard_a", "shard_b"},
		tablePrefix: "dnf",
		now:         time.Now,
	}}

	callbackRan := false
	owner := repo.GuildOwner{AccountID: "a", CharacterID: "19"}
	if err := settlement.WithinGuildSettlement(context.Background(), owner, "completion", func(tx repo.Group) error {
		callbackRan = true
		account, found, err := tx.Account.Load(context.Background(), owner.AccountID)
		if err != nil {
			return err
		}
		if !found || account.AccountID != owner.AccountID {
			return fmt.Errorf("account=%+v found=%t", account, found)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !callbackRan || state.begins != 1 || state.commits != 1 || state.rollbacks != 0 || state.outsideTxOperations != 0 {
		t.Fatalf("settlement state=%+v callback=%t", state, callbackRan)
	}
	if len(state.writeSQL) == 0 || !strings.Contains(state.writeSQL[0], "`dnf_"+mysqlSettingsTable+"`") {
		t.Fatalf("first write must lock the guild registry row: %v", state.writeSQL)
	}
	lockIndex := func(want string) int {
		for index, query := range state.queries {
			if strings.HasSuffix(query, " FOR UPDATE") && strings.Contains(query, "`dnf_"+want+"`") {
				return index
			}
		}
		return -1
	}
	registryLocked := lockIndex(mysqlSettingsTable)
	accountLocked := lockIndex(mysqlAccountTable)
	characterLocked := lockIndex(mysqlCharTable)
	if registryLocked != 0 || accountLocked <= registryLocked || characterLocked <= accountLocked {
		t.Fatalf("guild lock order registry=%d account=%d character=%d queries=%v", registryLocked, accountLocked, characterLocked, state.queries)
	}
}
