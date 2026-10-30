package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/npcshop"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

type npcShopUnexpectedSettlement struct{}

func (npcShopUnexpectedSettlement) WithinCharacterSettlement(context.Context, string, func(repo.Group) error) error {
	return errors.New("npc shop selected legacy character settlement while ledger capability was available")
}

func TestMySQLNPCShopPurchaseLocksAccountFirstForOrdinaryAndLedger(t *testing.T) {
	type order struct {
		firstAccountTable bool
		characterIndex    int
	}
	orders := map[string]order{}
	for _, accountLedger := range []bool{false, true} {
		name := map[bool]string{false: "ordinary", true: "account_ledger"}[accountLedger]
		t.Run(name, func(t *testing.T) {
			state := &raidLedgerSQLState{characterAccount: "a"}
			db := sql.OpenDB(raidLedgerSQLConnector{state: state})
			defer db.Close()
			group := repo.Group{
				AccountAssets:       raidLedgerTestUnitOfWork(db),
				CharacterSettlement: npcShopUnexpectedSettlement{},
			}
			owner, err := npcshop.NewOwner(group)
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.Mutate(context.Background(), npcshop.Command{
				AccountID:     "a",
				CharacterID:   "19",
				UpdatedAt:     time.Unix(1793046000, 0).UTC(),
				AccountLedger: accountLedger,
				Project: func(*npcshop.Assets) (npcshop.Changes, error) {
					return npcshop.Changes{Account: true}, nil
				},
			}); err != nil {
				t.Fatal(err)
			}
			if state.begins != 1 || state.commits != 1 || state.rollbacks != 0 {
				t.Fatalf("transaction counts=%+v", state)
			}
			raidLedgerAssertFirstAccountLock(t, state)
			raidLedgerAssertOnlyTransaction(t, state)
			characterIndex := -1
			for i, query := range state.queries {
				if strings.HasPrefix(query, "SELECT character_id, account_id, slot, name, job, level") {
					characterIndex = i
					break
				}
			}
			if characterIndex <= 0 {
				t.Fatalf("character lock index=%d queries=%v", characterIndex, state.queries)
			}
			orders[name] = order{firstAccountTable: strings.HasPrefix(state.queries[0], "SELECT account_id,"), characterIndex: characterIndex}
		})
	}
	if orders["ordinary"].firstAccountTable != orders["account_ledger"].firstAccountTable ||
		orders["ordinary"].characterIndex != orders["account_ledger"].characterIndex {
		t.Fatalf("ordinary/ledger lock order diverged: %+v", orders)
	}
}
