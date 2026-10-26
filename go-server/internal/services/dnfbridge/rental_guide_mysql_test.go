package dnfbridge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

// Uses a dedicated disposable instance; each invocation creates and drops its
// own database. The test never reads an existing player database.
func TestRentalGuideMySQLAtomicSettlement(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid disposable MySQL DSN")
	}
	cfg.DBName, cfg.ParseTime = "", true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open disposable MySQL failed")
	}
	defer db.Close()
	ctx := context.Background()
	name := fmt.Sprintf("rental_guide_test_%d", time.Now().UnixNano())
	plan := dnfrepo.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	}()
	repos, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	catalog := rentalGuideQuestTestCatalog(t)
	definition := dungeonDropItemDefinition{ItemID: 416000000, Kind: dungeonDropItemEquipment, PVFPath: "equipment/rental.equ", SlotStart: 9, SlotEnd: 64, Durability: 45}
	now := time.Unix(1_800_000_000, 0).UTC()
	seed := func(t *testing.T) {
		t.Helper()
		for _, err := range []error{
			repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{currentRentalPointMetadataKey: "20", "keep": "account"}}),
			repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 20, Job: "0", Stats: map[string]int64{"gold": 1000000}}),
			repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}),
			repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{}}),
			repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19"}),
		} {
			if err != nil {
				t.Fatal(err)
			}
		}
		owner, err := dnfquest.NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		accepted, err := owner.ApplyAccept(
			ctx,
			catalog,
			dnfquest.CharacterEligibility{Level: 20, Job: 0, GrowType: 0},
			dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: "account-1", SelectedCharacterID: 19}, "accept_quest", dnfquest.QuestIDRequest{QuestID: 8581}),
		)
		if err != nil || accepted.InitTrigger != 1 {
			t.Fatalf("accept guide: %+v, %v", accepted, err)
		}
	}
	t.Run("accepted_then_rent", func(t *testing.T) {
		seed(t)
		owner, err := currentRentalAssetOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		result, err := rentCurrentEquipment(ctx, owner, "account-1", "19", 416000000, 3, definition, now, catalog)
		if err != nil || !result.QuestAdvanced {
			t.Fatalf("rent result=%+v error=%v", result, err)
		}
		account, _, err := repos.Account.Load(ctx, "account-1")
		if err != nil {
			t.Fatal(err)
		}
		inventory, _, err := repos.Inventory.Load(ctx, "19")
		if err != nil {
			t.Fatal(err)
		}
		quests, _, err := repos.Quest.Load(ctx, "19")
		if err != nil {
			t.Fatal(err)
		}
		if account.Metadata[currentRentalPointMetadataKey] != "17" || account.Metadata["keep"] != "account" || inventory.Slots["0:9"].ItemID != 416000000 || quests.States[8581].ProgressValue != 0 || quests.States[8581].Status != "active" {
			t.Fatal("committed wallet, item and guide do not match one successful rental")
		}
	})
	t.Run("late_failure_rolls_back_all_rows", func(t *testing.T) {
		seed(t)
		before := rentalGuideSQLSnapshot(t, repos)
		want := errors.New("rental guide late transaction failure")
		failed := repos
		failed.CharacterSettlement = rentalGuideSQLFailAfterApply{base: repos.CharacterSettlement, err: want}
		owner, err := currentRentalAssetOwner(failed)
		if err != nil {
			t.Fatal(err)
		}
		result, err := rentCurrentEquipment(ctx, owner, "account-1", "19", 416000000, 3, definition, now, catalog)
		if !errors.Is(err, want) || result.QuestAdvanced {
			t.Fatalf("late error=%v result=%+v", err, result)
		}
		if !reflect.DeepEqual(before, rentalGuideSQLSnapshot(t, repos)) {
			t.Fatal("late failure retained an asset or quest write")
		}
	})
	t.Run("concurrent_renewals_no_lost_points_or_duplicate_item", func(t *testing.T) {
		seed(t)
		owner, err := currentRentalAssetOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errorsCh := make(chan error, 3)
		advanced := make(chan bool, 3)
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, e := rentCurrentEquipment(ctx, owner, "account-1", "19", 416000000, 3, definition, now, catalog)
				errorsCh <- e
				advanced <- r.QuestAdvanced
			}()
		}
		wg.Wait()
		close(errorsCh)
		close(advanced)
		for e := range errorsCh {
			if e != nil {
				t.Fatal(e)
			}
		}
		changes := 0
		for b := range advanced {
			if b {
				changes++
			}
		}
		account, _, err := repos.Account.Load(ctx, "account-1")
		if err != nil {
			t.Fatal(err)
		}
		inventory, _, err := repos.Inventory.Load(ctx, "19")
		if err != nil {
			t.Fatal(err)
		}
		quests, _, err := repos.Quest.Load(ctx, "19")
		if err != nil {
			t.Fatal(err)
		}
		if changes != 1 || account.Metadata[currentRentalPointMetadataKey] != "11" || len(inventory.Slots) != 1 || quests.States[8581].ProgressValue != 0 {
			t.Fatal("concurrent rental lost a debit or repeated guide progress")
		}
	})
}

func rentalGuideSQLSnapshot(t *testing.T, repos dnfrepo.Group) []any {
	t.Helper()
	ctx := context.Background()
	a, _, ea := repos.Account.Load(ctx, "account-1")
	c, _, ec := repos.Character.Load(ctx, "19")
	i, _, ei := repos.Inventory.Load(ctx, "19")
	e, _, ee := repos.Equipment.Load(ctx, "19")
	q, _, eq := repos.Quest.Load(ctx, "19")
	for _, err := range []error{ea, ec, ei, ee, eq} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return []any{a, c, i, e, q}
}

type rentalGuideSQLFailAfterApply struct {
	base dnfrepo.CharacterSettlementUnitOfWork
	err  error
}

func (u rentalGuideSQLFailAfterApply) WithinCharacterSettlement(ctx context.Context, characterID string, apply func(dnfrepo.Group) error) error {
	return u.base.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		if err := apply(tx); err != nil {
			return err
		}
		return u.err
	})
}
