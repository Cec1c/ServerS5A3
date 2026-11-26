package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestP4BuffOwnedSnapshotTracksSlotDeltaAndSkipsWarehouseOnSlotsRead(t *testing.T) {
	ctx := context.Background()
	s, _, router := newDeathDeltaSQL(t)
	g := newMySQLGroupFromRouter(router, false)

	acct := repository.AccountRecord{AccountID: "acct-77"}
	char := repository.CharacterRecord{CharacterID: "77", AccountID: "acct-77", Name: "BuffTest", Job: "0", Level: 90}
	slots := make(map[string]repository.ItemStack, 8)
	for i := 0; i < 8; i++ {
		slots[fmt.Sprintf("0:%d", 50+i)] = repository.ItemStack{
			ItemID: int64(10000 + i),
			Count:  1,
			Extra:  map[string]string{"seed": fmt.Sprintf("v%d", i)},
		}
	}
	warehouse := map[string]repository.ItemStack{
		"0:1": {ItemID: 99001, Count: 5, Extra: map[string]string{"keep": "yes"}},
	}
	if err := g.Account.Save(ctx, acct); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, char); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, repository.InventoryRecord{
		CharacterID: "77",
		Slots:       slots,
		Warehouse:   warehouse,
	}); err != nil {
		t.Fatal(err)
	}
	if err := g.Settings.Save(ctx, repository.SettingsRecord{
		Scope:  "character:77:buff_swap_native",
		Values: map[string]string{"state": "{}"},
	}); err != nil {
		t.Fatal(err)
	}

	uow := &mysqlCeraShopAssetUnitOfWork{router: router}

	// Part A: Read-only snapshot calling LoadInventorySlots must skip warehouse queries completely.
	s.calls = nil
	err := uow.WithinBuffOwnedReadSnapshot(ctx, "acct-77", "77", "character:77:buff_swap_native",
		func(_ repository.CharacterRepository, inv repository.InventoryRepository, _ repository.EquipmentRepository, _ repository.SettingsRepository, _ repository.BuffPetMetadataReader, _ repository.OwnedLegacyInventoryRepository) error {
			scoped, ok := inv.(*rentalScopedInventoryRepository)
			if !ok {
				t.Fatalf("unexpected inventory wrapper %T", inv)
			}
			buffStore, ok := scoped.base.(*mysqlBuffInventoryStore)
			if !ok {
				t.Fatalf("unexpected buff inventory base %T", scoped.base)
			}
			if buffStore.trackSlotChanges {
				t.Fatal("read-only buff inventory should not enable trackSlotChanges")
			}
			got, found, loadErr := repository.LoadInventorySlots(ctx, inv, "77")
			if loadErr != nil || !found {
				t.Fatalf("LoadInventorySlots found=%t err=%v", found, loadErr)
			}
			if len(got.Slots) != 8 || len(got.Warehouse) != 0 {
				t.Fatalf("unexpected slots/warehouse counts: slots=%d warehouse=%d", len(got.Slots), len(got.Warehouse))
			}
			if buffStore.loadedSnapshot != nil {
				t.Fatal("LoadInventorySlots must never seed loadedSnapshot")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("WithinBuffOwnedReadSnapshot: %v", err)
	}
	for _, call := range s.calls {
		for _, arg := range call.Args {
			if str, ok := arg.(string); ok && str == "warehouse" {
				t.Fatalf("LoadInventorySlots unexpectedly queried warehouse: %+v", call)
			}
		}
	}

	// Part B: Writable snapshot must enable trackSlotChanges, populate loadedSnapshot on full Load,
	// and write only the single changed slot delta without collection-wide delete-all or touching warehouse.
	s.calls = nil
	err = uow.WithinBuffOwnedSnapshot(ctx, "acct-77", "77", "character:77:buff_swap_native",
		func(_ repository.CharacterRepository, inv repository.InventoryRepository, _ repository.EquipmentRepository, _ repository.SettingsRepository, _ repository.BuffPetMetadataReader, _ repository.OwnedLegacyInventoryRepository) error {
			scoped := inv.(*rentalScopedInventoryRepository)
			buffStore := scoped.base.(*mysqlBuffInventoryStore)
			if !buffStore.trackSlotChanges {
				t.Fatal("writable buff inventory must enable trackSlotChanges")
			}
			// Partial read still does not seed loadedSnapshot.
			if _, found, loadErr := repository.LoadInventorySlots(ctx, inv, "77"); loadErr != nil || !found {
				t.Fatalf("writable LoadInventorySlots found=%t err=%v", found, loadErr)
			}
			if buffStore.loadedSnapshot != nil {
				t.Fatal("writable LoadInventorySlots must not seed loadedSnapshot")
			}
			// Full Load populates loadedSnapshot for delta write.
			bag, found, loadErr := inv.Load(ctx, "77")
			if loadErr != nil || !found {
				t.Fatalf("writable Load found=%t err=%v", found, loadErr)
			}
			if buffStore.loadedSnapshot == nil || len(buffStore.loadedSnapshot.Slots) != 8 {
				t.Fatalf("writable Load did not populate loadedSnapshot: %+v", buffStore.loadedSnapshot)
			}
			s.calls = nil
			target := bag.Slots["0:50"]
			target.Extra["buff_swap_instance"] = "inst-abc"
			bag.Slots["0:50"] = target
			return repository.SaveInventoryFields(ctx, inv, bag, repository.InventoryFieldSlots)
		},
	)
	if err != nil {
		t.Fatalf("WithinBuffOwnedSnapshot: %v", err)
	}
	for _, call := range s.calls {
		if strings.Contains(call.Query, "DELETE FROM") && strings.Contains(call.Query, "dnf_inventory_item_extra") &&
			!strings.Contains(call.Query, "entry_key") {
			t.Fatalf("writable buff snapshot executed full-collection extra DELETE instead of slot delta: %s", call.Query)
		}
		if strings.Contains(call.Query, "DELETE FROM") && strings.Contains(call.Query, "dnf_inventory_items") &&
			!strings.Contains(call.Query, "entry_key") {
			t.Fatalf("writable buff snapshot executed full-collection items DELETE instead of slot delta: %s", call.Query)
		}
	}
	after, found, err := g.Inventory.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("reload after buff delta found=%t err=%v", found, err)
	}
	if after.Slots["0:50"].Extra["buff_swap_instance"] != "inst-abc" ||
		after.Slots["0:57"].Extra["seed"] != "v7" ||
		after.Warehouse["0:1"].ItemID != 99001 || after.Warehouse["0:1"].Extra["keep"] != "yes" {
		t.Fatalf("post-delta bag/warehouse corrupted: %+v", after)
	}
}

func TestP4WithinCharacterPetsEnablesInventoryDeltaTracking(t *testing.T) {
	ctx := context.Background()
	s, _, router := newDeathDeltaSQL(t)
	g := newMySQLGroupFromRouter(router, false)

	slots := make(map[string]repository.ItemStack, 6)
	for i := 0; i < 6; i++ {
		slots[fmt.Sprintf("7:%d", i)] = repository.ItemStack{
			ItemID: int64(20000 + i),
			Count:  1,
			Extra:  map[string]string{"pet": fmt.Sprintf("p%d", i)},
		}
	}
	if err := g.Inventory.Save(ctx, repository.InventoryRecord{
		CharacterID: "77",
		Slots:       slots,
	}); err != nil {
		t.Fatal(err)
	}

	uow := &mysqlCharacterPetUnitOfWork{router: router}
	err := uow.WithinCharacterPets(ctx, "77", func(inv repository.InventoryRepository, _ repository.EquipmentRepository, _ repository.PetRepository) error {
		scoped, ok := inv.(*characterPetScopedInventoryRepository)
		if !ok {
			t.Fatalf("unexpected pet inventory wrapper %T", inv)
		}
		if !scoped.base.(*mysqlBagStore).trackSlotChanges {
			t.Fatal("WithinCharacterPets must enable inventory trackSlotChanges")
		}
		bag, found, loadErr := inv.Load(ctx, "77")
		if loadErr != nil || !found {
			t.Fatalf("pet inventory load found=%t err=%v", found, loadErr)
		}
		s.calls = nil
		st := bag.Slots["7:0"]
		st.Count = 2
		bag.Slots["7:0"] = st
		return repository.SaveInventoryFields(ctx, inv, bag, repository.InventoryFieldSlots)
	})
	if err != nil {
		t.Fatalf("WithinCharacterPets: %v", err)
	}
	for _, call := range s.calls {
		if strings.Contains(call.Query, "DELETE FROM") && strings.Contains(call.Query, "dnf_inventory_item_extra") &&
			!strings.Contains(call.Query, "entry_key") {
			t.Fatalf("WithinCharacterPets executed full-collection extra DELETE: %s", call.Query)
		}
	}
	after, found, err := g.Inventory.Load(ctx, "77")
	if err != nil || !found || after.Slots["7:0"].Count != 2 || after.Slots["7:5"].Extra["pet"] != "p5" {
		t.Fatalf("post-pet-delta bag mismatch: found=%t err=%v bag=%+v", found, err, after)
	}
}

type p4RaidEntryConnector struct {
	base driver.Connector
}

func (c p4RaidEntryConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return p4RaidEntryConn{deathDeltaConn: conn.(deathDeltaConn)}, nil
}

func (c p4RaidEntryConnector) Driver() driver.Driver { return c.base.Driver() }

type p4RaidEntryConn struct {
	deathDeltaConn
}

func (c p4RaidEntryConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(query, "INSERT IGNORE INTO ") && strings.Contains(query, "dnf_settings") {
		c.s.calls = append(c.s.calls, fakeSQLCall{Query: query, Args: cargoDeltaArgs(args)})
		return driver.RowsAffected(0), nil
	}
	return c.deathDeltaConn.ExecContext(ctx, query, args)
}

func TestP4RaidEntryAndTradeGroupPerOwnerDeltaTrackingAndSaveFields(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"raid-entry", "trade-group"} {
		t.Run(mode, func(t *testing.T) {
			s, _, router := newDeathDeltaSQL(t)
			if mode == "raid-entry" {
				rdb := sql.OpenDB(p4RaidEntryConnector{base: deathDeltaConnector{s}})
				rdb.SetMaxOpenConns(1)
				t.Cleanup(func() { rdb.Close() })
				router.db = SQLDBAdapter{rdb}
			}
			g := newMySQLGroupFromRouter(router, false)
			if mode == "raid-entry" {
				if err := g.Settings.Save(ctx, repository.SettingsRecord{Scope: "raid:entry:scope", Values: map[string]string{}}); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range []string{"19", "20"} {
				stats := map[string]int64{"raid_daily_count": 1}
				for i := 0; i < 20; i++ {
					stats[fmt.Sprintf("unrelated_%d", i)] = int64(i + 1)
				}
				if err := g.Character.Save(ctx, repository.CharacterRecord{
					CharacterID: id,
					AccountID:   "acct-" + id,
					Name:        "Hero" + id,
					Level:       90,
					Stats:       stats,
				}); err != nil {
					t.Fatal(err)
				}
				slots := map[string]repository.ItemStack{
					"0:50": {ItemID: 10096296, Count: 5, Extra: map[string]string{"tag": "ticket"}},
					"0:51": {ItemID: 10000001, Count: 9, Extra: map[string]string{"keep": "stay"}},
				}
				if err := g.Inventory.Save(ctx, repository.InventoryRecord{
					CharacterID: id,
					Slots:       slots,
				}); err != nil {
					t.Fatal(err)
				}
			}

			uow := &mysqlCharacterTradeUnitOfWork{router: router}
			applyCheck := func(chars repository.CharacterRepository, invs repository.InventoryRepository) error {
				sc := chars.(*characterTradeScopedCharacterRepository)
				si := invs.(*characterTradeScopedInventoryRepository)
				if sc.base.(*mysqlCharStore).trackStatChanges {
					t.Fatal("multi-owner character base must stay untracked")
				}
				if si.base.(*mysqlBagStore).trackSlotChanges {
					t.Fatal("multi-owner inventory base must stay untracked")
				}
				if !sc.ownerStore("19").(*mysqlCharStore).trackStatChanges || !sc.ownerStore("20").(*mysqlCharStore).trackStatChanges {
					t.Fatal("per-owner character stores must enable trackStatChanges")
				}
				if !si.ownerStore("19").(*mysqlBagStore).trackSlotChanges || !si.ownerStore("20").(*mysqlBagStore).trackSlotChanges {
					t.Fatal("per-owner inventory stores must enable trackSlotChanges")
				}
				c19, ok, err := chars.Load(ctx, "19")
				if err != nil || !ok {
					t.Fatalf("load c19 ok=%t err=%v", ok, err)
				}
				c20, ok, err := chars.Load(ctx, "20")
				if err != nil || !ok {
					t.Fatalf("load c20 ok=%t err=%v", ok, err)
				}
				b19, ok, err := invs.Load(ctx, "19")
				if err != nil || !ok {
					t.Fatalf("load b19 ok=%t err=%v", ok, err)
				}
				b20, ok, err := invs.Load(ctx, "20")
				if err != nil || !ok {
					t.Fatalf("load b20 ok=%t err=%v", ok, err)
				}
				s.calls = nil
				c19.Stats["raid_daily_count"] = 2
				c20.Stats["raid_daily_count"] = 3
				if err := repository.SaveCharacterFields(ctx, chars, c19, repository.CharacterFieldStats); err != nil {
					return err
				}
				if err := repository.SaveCharacterFields(ctx, chars, c20, repository.CharacterFieldStats); err != nil {
					return err
				}
				t19 := b19.Slots["0:50"]
				t19.Count = 4
				b19.Slots["0:50"] = t19
				t20 := b20.Slots["0:50"]
				t20.Count = 3
				b20.Slots["0:50"] = t20
				if err := repository.SaveInventoryFields(ctx, invs, b19, repository.InventoryFieldSlots); err != nil {
					return err
				}
				return repository.SaveInventoryFields(ctx, invs, b20, repository.InventoryFieldSlots)
			}

			var err error
			if mode == "raid-entry" {
				err = uow.WithinRaidEntryAssets(ctx, []string{"19", "20"}, "raid:entry:scope", func(c repository.CharacterRepository, i repository.InventoryRepository, _ repository.SettingsRepository) error {
					return applyCheck(c, i)
				})
			} else {
				err = uow.withinCharacterInventoryGroup(ctx, []string{"19", "20"}, "", func(c repository.CharacterRepository, i repository.InventoryRepository, _ repository.AccountInventoryRepository) error {
					return applyCheck(c, i)
				})
			}
			if err != nil {
				t.Fatalf("%s failed: %v", mode, err)
			}
			for _, call := range s.calls {
				if strings.Contains(call.Query, "DELETE FROM") && strings.Contains(call.Query, "dnf_character_stats") &&
					!strings.Contains(call.Query, "stat_key") {
					t.Fatalf("%s executed full-table stats DELETE instead of stat delta: %s", mode, call.Query)
				}
				if strings.Contains(call.Query, "dnf_character_roster") {
					t.Fatalf("%s touched roster table during SaveCharacterFields(Stats): %s", mode, call.Query)
				}
				if strings.Contains(call.Query, "DELETE FROM") && strings.Contains(call.Query, "dnf_inventory_item_extra") &&
					!strings.Contains(call.Query, "entry_key") {
					t.Fatalf("%s executed full-collection extra DELETE instead of slot delta: %s", mode, call.Query)
				}
			}
			after19, _, _ := g.Character.Load(ctx, "19")
			after20, _, _ := g.Character.Load(ctx, "20")
			bag19, _, _ := g.Inventory.Load(ctx, "19")
			bag20, _, _ := g.Inventory.Load(ctx, "20")
			if after19.Stats["raid_daily_count"] != 2 || after19.Stats["unrelated_19"] != 20 ||
				after20.Stats["raid_daily_count"] != 3 || after20.Stats["unrelated_19"] != 20 {
				t.Fatalf("%s stats mismatch: c19=%v c20=%v", mode, after19.Stats["raid_daily_count"], after20.Stats["raid_daily_count"])
			}
			if bag19.Slots["0:50"].Count != 4 || bag19.Slots["0:51"].Extra["keep"] != "stay" ||
				bag20.Slots["0:50"].Count != 3 || bag20.Slots["0:51"].Extra["keep"] != "stay" {
				t.Fatalf("%s bags mismatch: b19=%+v b20=%+v", mode, bag19.Slots, bag20.Slots)
			}
		})
	}
}

func TestP4WithinAccountMailboxLocksNarrowCharacterIdentity(t *testing.T) {
	ctx := context.Background()
	s, _, router := newDeathDeltaSQL(t)
	g := newMySQLGroupFromRouter(router, false)
	if err := g.Account.Save(ctx, repository.AccountRecord{AccountID: "acct-77", HonorExp: 10}); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, repository.CharacterRecord{
		CharacterID: "77",
		AccountID:   "acct-77",
		Name:        "MailHero",
		Level:       90,
		Stats:       map[string]int64{"custom_stat": 42},
	}); err != nil {
		t.Fatal(err)
	}
	uow := &mysqlMailboxAssetUnitOfWork{router: router}

	// WithinAccountMailbox must lock Account then narrow Character identity (no stats/roster scan).
	s.calls = nil
	if err := uow.WithinAccountMailbox(ctx, "acct-77", "77", func(_ repository.AccountRepository, _ repository.MailboxRepository) error {
		return nil
	}); err != nil {
		t.Fatalf("WithinAccountMailbox: %v", err)
	}
	if len(s.calls) < 2 {
		t.Fatalf("expected account + character lock queries, got %+v", s.calls)
	}
	if !strings.Contains(s.calls[0].Query, "dnf_accounts") || !strings.HasSuffix(s.calls[0].Query, " FOR UPDATE") {
		t.Fatalf("first lock must be dnf_accounts FOR UPDATE, got: %s", s.calls[0].Query)
	}
	if !strings.Contains(s.calls[1].Query, "dnf_characters") || !strings.HasSuffix(s.calls[1].Query, " FOR UPDATE") {
		t.Fatalf("second lock must be dnf_characters FOR UPDATE, got: %s", s.calls[1].Query)
	}
	for _, call := range s.calls {
		if strings.Contains(call.Query, "dnf_character_stats") || strings.Contains(call.Query, "dnf_character_roster") {
			t.Fatalf("WithinAccountMailbox unexpectedly queried stats/roster: %s", call.Query)
		}
	}

	// Foreign account must still be rejected.
	if err := uow.WithinAccountMailbox(ctx, "acct-other", "77", func(_ repository.AccountRepository, _ repository.MailboxRepository) error {
		t.Fatal("callback should not run for mismatched account")
		return nil
	}); !errors.Is(err, repository.ErrMailboxAssetTransactionUnavailable) {
		t.Fatalf("expected ErrMailboxAssetTransactionUnavailable for wrong account, got %v", err)
	}

	// WithinAccountMailboxCharacter must still provide full CharacterRecord with stats.
	var loadedChar repository.CharacterRecord
	if err := uow.WithinAccountMailboxCharacter(ctx, "acct-77", "77", func(_ repository.AccountRepository, ch repository.CharacterRecord, _ repository.MailboxRepository) error {
		loadedChar = ch
		return nil
	}); err != nil {
		t.Fatalf("WithinAccountMailboxCharacter: %v", err)
	}
	if loadedChar.CharacterID != "77" || loadedChar.Stats["custom_stat"] != 42 {
		t.Fatalf("WithinAccountMailboxCharacter lost stats: %+v", loadedChar)
	}
}

type p4StaticInventoryRepo struct {
	record repository.InventoryRecord
	found  bool
}

func (r p4StaticInventoryRepo) Load(context.Context, string) (repository.InventoryRecord, bool, error) {
	return r.record, r.found, nil
}
func (r p4StaticInventoryRepo) Save(context.Context, repository.InventoryRecord) error { return nil }

func TestP4LoadInventorySlotItemPrefixFoundSingleQuerySemantics(t *testing.T) {
	ctx := context.Background()
	expire := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	t.Run("missing-owner", func(t *testing.T) {
		db := &fakeSQLDB{rowsets: []fakeSQLRows{{}}}
		store := newTestMySQLGroup(t, db).Inventory.(*mysqlBagStore)
		got, found, err := store.LoadInventorySlotItemPrefixFound(ctx, "19", "0:", 10096296)
		if err != nil || found || len(got) != 0 || len(db.execs) != 1 {
			t.Fatalf("missing-owner got=%+v found=%t err=%v execs=%d", got, found, err, len(db.execs))
		}
	})

	t.Run("present-owner-zero-matches", func(t *testing.T) {
		db := &fakeSQLDB{rowsets: []fakeSQLRows{{
			rows: []fakeSQLRow{{
				values: []any{"19", nil, nil, nil, nil, nil, nil, nil, nil},
			}},
		}}}
		store := newTestMySQLGroup(t, db).Inventory.(*mysqlBagStore)
		got, found, err := store.LoadInventorySlotItemPrefixFound(ctx, "19", "0:", 10096296)
		if err != nil || !found || len(got) != 0 || len(db.execs) != 1 {
			t.Fatalf("zero-matches got=%+v found=%t err=%v execs=%d", got, found, err, len(db.execs))
		}
		if store.loadedSnapshot != nil {
			t.Fatal("partial projection must never seed loadedSnapshot")
		}
	})

	t.Run("present-owner-matched-with-extras", func(t *testing.T) {
		db := &fakeSQLDB{rowsets: []fakeSQLRows{{
			rows: []fakeSQLRow{
				{values: []any{"19", "0:50", int64(10096296), int64(3), true, expire, []byte{1, 2}, "k1", "v1"}},
				{values: []any{"19", "0:50", int64(10096296), int64(3), true, expire, nil, "k2", "v2"}},
			},
		}}}
		store := newTestMySQLGroup(t, db).Inventory.(*mysqlBagStore)
		got, found, err := store.LoadInventorySlotItemPrefixFound(ctx, "19", "0:", 10096296)
		if err != nil || !found || len(got) != 1 || len(db.execs) != 1 {
			t.Fatalf("matched got=%+v found=%t err=%v execs=%d", got, found, err, len(db.execs))
		}
		want := repository.ItemStack{
			ItemID:   10096296,
			Count:    3,
			Bind:     true,
			ExpireAt: expire,
			RawEntry: []byte{1, 2},
			Extra:    map[string]string{"k1": "v1", "k2": "v2"},
		}
		if !reflect.DeepEqual(got["0:50"], want) {
			t.Fatalf("matched stack = %+v, want %+v", got["0:50"], want)
		}
	})

	t.Run("empty-prefix-multi-slot-0-1-2-extras", func(t *testing.T) {
		db := &fakeSQLDB{rowsets: []fakeSQLRows{{
			rows: []fakeSQLRow{
				{values: []any{"19", "00:50", int64(10096296), int64(2), false, nil, nil, nil, nil}},
				{values: []any{"19", "0:54", int64(10096296), int64(1), true, expire, []byte{9}, "only", "one"}},
				{values: []any{"19", "1:2", int64(10096296), int64(4), false, nil, []byte{3, 4}, "a", "1"}},
				{values: []any{"19", "1:2", int64(10096296), int64(4), false, nil, nil, "b", "2"}},
			},
		}}}
		store := newTestMySQLGroup(t, db).Inventory.(*mysqlBagStore)
		got, found, err := store.LoadInventorySlotItemPrefixFound(ctx, "19", "", 10096296)
		if err != nil || !found || len(got) != 3 || len(db.execs) != 1 {
			t.Fatalf("empty-prefix multi-slot got=%+v found=%t err=%v execs=%d", got, found, err, len(db.execs))
		}
		if strings.Contains(db.execs[0].Query, "LIKE") {
			t.Fatalf("empty prefix should omit LIKE predicate: %s", db.execs[0].Query)
		}
		if got["00:50"].Count != 2 || len(got["00:50"].Extra) != 0 ||
			got["0:54"].Extra["only"] != "one" || len(got["0:54"].Extra) != 1 ||
			got["1:2"].Extra["a"] != "1" || got["1:2"].Extra["b"] != "2" {
			t.Fatalf("unexpected grouped stacks: %+v", got)
		}
	})

	t.Run("case-sensitive-byte-prefix-filters-ci-sql", func(t *testing.T) {
		db := &fakeSQLDB{rowsets: []fakeSQLRows{{
			rows: []fakeSQLRow{
				{values: []any{"19", "a:1", int64(10096296), int64(5), false, nil, nil, nil, nil}},
				{values: []any{"19", "A:1", int64(10096296), int64(2), false, nil, nil, nil, nil}},
			},
		}}}
		store := newTestMySQLGroup(t, db).Inventory.(*mysqlBagStore)
		got, found, err := store.LoadInventorySlotItemPrefixFound(ctx, "19", "A:", 10096296)
		if err != nil || !found || len(got) != 1 || got["A:1"].Count != 2 {
			t.Fatalf("case-sensitive prefix filter failed: got=%+v found=%t err=%v", got, found, err)
		}
	})

	t.Run("invalid-args-rejected-consistently", func(t *testing.T) {
		store := newTestMySQLGroup(t, &fakeSQLDB{}).Inventory.(*mysqlBagStore)
		fallback := p4StaticInventoryRepo{found: true, record: repository.InventoryRecord{CharacterID: "19"}}
		for _, tc := range []struct {
			id     string
			itemID int64
		}{{"", 1}, {"19", 0}, {"19", -5}} {
			if _, _, err := store.LoadInventorySlotItemPrefixFound(ctx, tc.id, "", tc.itemID); err == nil {
				t.Fatalf("mysql expected error for id=%q itemID=%d", tc.id, tc.itemID)
			}
			if _, _, err := repository.LoadInventorySlotItemPrefixFound(ctx, fallback, tc.id, "", tc.itemID); err == nil {
				t.Fatalf("fallback expected error for id=%q itemID=%d", tc.id, tc.itemID)
			}
		}
	})

	t.Run("fallback-rejects-mismatched-character-id", func(t *testing.T) {
		repo := p4StaticInventoryRepo{
			found: true,
			record: repository.InventoryRecord{
				CharacterID: "20",
				Slots:       map[string]repository.ItemStack{"0:50": {ItemID: 10096296, Count: 5}},
			},
		}
		got, found, err := repository.LoadInventorySlotItemPrefixFound(ctx, repo, "19", "0:", 10096296)
		if err != nil || found || len(got) != 0 {
			t.Fatalf("fallback owner mismatch accepted: got=%+v found=%t err=%v", got, found, err)
		}
	})
}

func TestP4CharacterMirrorStatsTemplateCacheConcurrent(t *testing.T) {
	r := mysqlRouter{
		sqlTemplates: &mysqlSQLTemplates{},
	}
	table := "`dnf_s1_w1`.`dnf_characters`"
	combos := [][]string{
		nil,
		{repository.CharacterGrowTypeStatKey},
		{"gold"},
		{"fatigue", repository.CharacterGrowTypeStatKey},
	}

	const workers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([][]string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			out := make([]string, len(combos))
			for j, keys := range combos {
				out[j] = r.characterMirrorQuery(table, keys)
			}
			results[idx] = out
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 1; i < workers; i++ {
		if !reflect.DeepEqual(results[i], results[0]) {
			t.Fatalf("worker %d mismatch: got=%q want=%q", i, results[i], results[0])
		}
	}
	if _, ok := r.sqlTemplates.characterMirrorBase.Load(table + "\x00" + repository.CharacterGrowTypeStatKey); !ok {
		t.Fatal("expected grow_type query template to be cached")
	}
	if _, ok := r.sqlTemplates.characterMirrorBase.Load(table); !ok {
		t.Fatal("expected base mirror query template to be cached")
	}
}
