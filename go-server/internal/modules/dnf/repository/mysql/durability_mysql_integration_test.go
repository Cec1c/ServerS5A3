//go:build mysql_integration

package mysql_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/equip"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	mysqlrepo "longheng.io/server/internal/modules/dnf/repository/mysql"
)

const durabilityMySQLItemID int64 = 101040493

type durabilityMySQLFixture struct {
	accountID       string
	characterID     string
	characterNumber uint16
}

// TestMySQLDurabilityOwnerDecreaseAutoRepairAndRollback uses one explicitly
// gated, one-time loopback database for all three production-owner cases. It
// never accepts a DSN with a preselected database and never targets a player
// schema. The trigger used by the rollback case exists only in this disposable
// database and is not a production fault-injection hook.
func TestMySQLDurabilityOwnerDecreaseAutoRepairAndRollback(t *testing.T) {
	if os.Getenv("DNF_FATE_DISPOSABLE_DB") != "1" {
		t.Fatal("requires an explicitly isolated disposable database instance")
	}
	cfg, err := drivermysql.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" {
		t.Fatalf("invalid isolated TCP DSN: %v", err)
	}
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" || strings.TrimSpace(port) == "" || cfg.DBName != "" {
		t.Fatal("test requires loopback TCP with no preselected database")
	}
	cfg.ParseTime = true

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	open := func(maxConns int) *sql.DB {
		t.Helper()
		db, openErr := sql.Open("mysql", cfg.FormatDSN())
		if openErr != nil {
			t.Fatalf("open isolated database: %v", openErr)
		}
		db.SetMaxOpenConns(maxConns)
		db.SetMaxIdleConns(maxConns)
		if pingErr := db.PingContext(ctx); pingErr != nil {
			_ = db.Close()
			t.Fatalf("ping isolated database: %v", pingErr)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	admin := open(2)
	data := open(2)

	databaseName := fmt.Sprintf("durability_mysql_%d", time.Now().UnixNano())
	if !regexp.MustCompile("^durability_mysql_[0-9]+$").MatchString(databaseName) {
		t.Fatal("unsafe disposable database name")
	}
	databaseIdent := durabilityMySQLQuotedIdentifier(databaseName)
	entriesIdent := databaseIdent + "." + durabilityMySQLQuotedIdentifier("dnf_equipment_entries")
	extraIdent := databaseIdent + "." + durabilityMySQLQuotedIdentifier("dnf_equipment_entry_extra")
	var existingSchemas int
	if err := admin.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", databaseName).Scan(&existingSchemas); err != nil {
		t.Fatalf("check disposable schema name: %v", err)
	}
	if existingSchemas != 0 {
		t.Fatal("disposable schema name already exists; refusing to reuse it")
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, dropErr := admin.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS "+databaseIdent); dropErr != nil {
			t.Errorf("drop disposable database: %v", dropErr)
		}
	})

	plan := repo.DatabasePlan{ReadDatabases: []string{databaseName}, WriteDatabases: []string{databaseName}}
	if err := mysqlrepo.EnsureMySQLSchema(ctx, admin, mysqlrepo.SchemaOptions{
		AutoCreate:      true,
		CreateDatabases: true,
		DatabasePlan:    plan,
	}); err != nil {
		t.Fatalf("create disposable schema: %v", err)
	}
	group, err := mysqlrepo.NewMySQLGroupFromDB(data, mysqlrepo.MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatalf("assemble MySQL group: %v", err)
	}
	owner, err := equip.NewDurabilityOwner(group)
	if err != nil {
		t.Fatalf("create durability owner: %v", err)
	}
	resolver := func(int64) (alignedcmd.RepairCostEvidence, error) {
		return alignedcmd.RepairCostEvidence{
			EquipmentType:   "[weapon]",
			MaxDurability:   48,
			RepairPrice:     4800,
			Grade:           20,
			RepairCostRate:  0.08,
			QuickRepairRate: 1.5,
			UpgradeRates:    []float64{1, 1, 1},
		}, nil
	}
	now := time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)

	t.Run("ordinary-decrease-persists-raw-and-extra", func(t *testing.T) {
		fixture := durabilityMySQLFixture{
			accountID:       "durability-mysql-account-ordinary",
			characterID:     "51001",
			characterNumber: 51001,
		}
		before := seedDurabilityMySQLFixture(t, ctx, group, fixture, 48, false, 0, now)
		result, decreaseErr := owner.Decrease(ctx, durabilityMySQLDecreaseCommand(fixture, now), resolver)
		if decreaseErr != nil {
			t.Fatalf("decrease: %v", decreaseErr)
		}
		if result.Previous != 48 || result.Current != 47 || !result.Changed || result.AutoRepaired || result.Gold != 5000 {
			t.Fatalf("unexpected ordinary result: %+v", result)
		}
		after := durabilityMySQLAssertStored(t, ctx, data, group, entriesIdent, extraIdent, fixture, 47)
		durabilityMySQLAssertOnlyDurabilityBytesChanged(t, before.RawEntry, after.RawEntry, 47)
		if after.Extra["keep_me"] != "unchanged" || after.Extra["quality_seed"] != "203004656" || after.Extra["ext_data0"] != "79" {
			t.Fatalf("ordinary decrease changed unrelated extra fields: %+v", after.Extra)
		}
		for _, key := range []string{"durability", "current_durability", "value_b_word"} {
			if got := after.Extra[key]; got != "47" {
				t.Fatalf("%s = %q, want 47", key, got)
			}
		}
		wantHex := hex.EncodeToString(after.RawEntry)
		for _, key := range []string{"raw_entry_hex", "equipment_raw_entry_hex"} {
			if got := after.Extra[key]; got != wantHex {
				t.Fatalf("%s does not match persisted raw entry", key)
			}
		}
	})

	t.Run("auto-repair-consumes-sixth-quota-and-caps-seventh", func(t *testing.T) {
		fixture := durabilityMySQLFixture{
			accountID:       "durability-mysql-account-auto",
			characterID:     "51002",
			characterNumber: 51002,
		}
		_ = seedDurabilityMySQLFixture(t, ctx, group, fixture, 1, true, 5, now)
		result, decreaseErr := owner.Decrease(ctx, durabilityMySQLDecreaseCommand(fixture, now), resolver)
		if decreaseErr != nil {
			t.Fatalf("sixth auto repair: %v", decreaseErr)
		}
		if result.Previous != 1 || result.Current != 48 || !result.Changed || !result.AutoRepaired || result.Gold != 5000 {
			t.Fatalf("unexpected sixth-repair result: %+v", result)
		}
		character, found, loadErr := group.Character.Load(ctx, fixture.characterID)
		if loadErr != nil || !found {
			t.Fatalf("reload character found=%t err=%v", found, loadErr)
		}
		if got := premium.DailyUsage(character, premium.DevilSlotAutoRepair, now); got != 6 {
			t.Fatalf("daily usage after sixth repair = %d, want 6", got)
		}
		if character.Stats["gold"] != 5000 {
			t.Fatalf("auto repair changed gold: %+v", character.Stats)
		}
		durabilityMySQLAssertStored(t, ctx, data, group, entriesIdent, extraIdent, fixture, 48)

		equipmentRecord, found, loadErr := group.Equipment.Load(ctx, fixture.characterID)
		if loadErr != nil || !found {
			t.Fatalf("reload equipment found=%t err=%v", found, loadErr)
		}
		entry := equipmentRecord.Entries["12"]
		binary.LittleEndian.PutUint16(entry.RawEntry[equip.RepairDurabilityOffsetCurrentRaw77:equip.RepairDurabilityOffsetCurrentRaw77+2], 1)
		for _, key := range []string{"durability", "current_durability", "value_b_word"} {
			entry.Extra[key] = "1"
		}
		wantHex := hex.EncodeToString(entry.RawEntry)
		for _, key := range []string{"raw_entry_hex", "equipment_raw_entry_hex"} {
			entry.Extra[key] = wantHex
		}
		equipmentRecord.Entries["12"] = entry
		if saveErr := group.Equipment.Save(ctx, equipmentRecord); saveErr != nil {
			t.Fatalf("reset durability to one: %v", saveErr)
		}

		result, decreaseErr = owner.Decrease(ctx, durabilityMySQLDecreaseCommand(fixture, now), resolver)
		if decreaseErr != nil {
			t.Fatalf("seventh repair attempt: %v", decreaseErr)
		}
		if result.Previous != 1 || result.Current != 0 || !result.Changed || result.AutoRepaired || result.Gold != 5000 {
			t.Fatalf("unexpected capped result: %+v", result)
		}
		character, found, loadErr = group.Character.Load(ctx, fixture.characterID)
		if loadErr != nil || !found {
			t.Fatalf("reload capped character found=%t err=%v", found, loadErr)
		}
		if got := premium.DailyUsage(character, premium.DevilSlotAutoRepair, now); got != 6 {
			t.Fatalf("daily usage after capped attempt = %d, want 6", got)
		}
		durabilityMySQLAssertStored(t, ctx, data, group, entriesIdent, extraIdent, fixture, 0)
	})

	t.Run("equipment-trigger-failure-rolls-back-quota-and-durability", func(t *testing.T) {
		fixture := durabilityMySQLFixture{
			accountID:       "durability-mysql-account-rollback",
			characterID:     "51003",
			characterNumber: 51003,
		}
		_ = seedDurabilityMySQLFixture(t, ctx, group, fixture, 1, true, 5, now)
		characterBefore, found, loadErr := group.Character.Load(ctx, fixture.characterID)
		if loadErr != nil || !found {
			t.Fatalf("load character before rollback found=%t err=%v", found, loadErr)
		}
		equipmentBefore, found, loadErr := group.Equipment.Load(ctx, fixture.characterID)
		if loadErr != nil || !found {
			t.Fatalf("load equipment before rollback found=%t err=%v", found, loadErr)
		}

		triggerName := fmt.Sprintf("durability_fail_%d", time.Now().UnixNano())
		triggerIdent := databaseIdent + "." + durabilityMySQLQuotedIdentifier(triggerName)
		createTrigger := "CREATE TRIGGER " + triggerIdent +
			" BEFORE INSERT ON " + entriesIdent +
			" FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'durability equipment failure injected by test'"
		if _, triggerErr := admin.ExecContext(ctx, createTrigger); triggerErr != nil {
			t.Fatalf("create disposable equipment failure trigger: %v", triggerErr)
		}
		t.Cleanup(func() {
			cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			if _, dropErr := admin.ExecContext(cleanupCtx, "DROP TRIGGER IF EXISTS "+triggerIdent); dropErr != nil {
				t.Errorf("drop disposable equipment failure trigger: %v", dropErr)
			}
		})

		_, decreaseErr := owner.Decrease(ctx, durabilityMySQLDecreaseCommand(fixture, now), resolver)
		if decreaseErr == nil || !strings.Contains(decreaseErr.Error(), "durability equipment failure injected by test") {
			t.Fatalf("decrease error = %v, want disposable equipment trigger failure", decreaseErr)
		}
		characterAfter, found, loadErr := group.Character.Load(ctx, fixture.characterID)
		if loadErr != nil || !found {
			t.Fatalf("load character after rollback found=%t err=%v", found, loadErr)
		}
		equipmentAfter, found, loadErr := group.Equipment.Load(ctx, fixture.characterID)
		if loadErr != nil || !found {
			t.Fatalf("load equipment after rollback found=%t err=%v", found, loadErr)
		}
		if !reflect.DeepEqual(characterAfter, characterBefore) {
			t.Fatalf("character quota/gold changed after equipment failure:\nbefore=%+v\nafter=%+v", characterBefore, characterAfter)
		}
		if !reflect.DeepEqual(equipmentAfter, equipmentBefore) {
			t.Fatalf("equipment changed after final save failure:\nbefore=%+v\nafter=%+v", equipmentBefore, equipmentAfter)
		}
		if got := premium.DailyUsage(characterAfter, premium.DevilSlotAutoRepair, now); got != 5 {
			t.Fatalf("daily usage after rollback = %d, want 5", got)
		}
		durabilityMySQLAssertStored(t, ctx, data, group, entriesIdent, extraIdent, fixture, 1)
	})
}

func durabilityMySQLQuotedIdentifier(name string) string {
	return "\x60" + name + "\x60"
}

func durabilityMySQLDecreaseCommand(fixture durabilityMySQLFixture, now time.Time) equip.DecreaseDurabilityCommand {
	return equip.DecreaseDurabilityCommand{
		AccountID:   fixture.accountID,
		CharacterID: fixture.characterNumber,
		Slot:        12,
		Now:         now,
	}
}

func seedDurabilityMySQLFixture(
	t *testing.T,
	ctx context.Context,
	group repo.Group,
	fixture durabilityMySQLFixture,
	current uint16,
	autoRepair bool,
	autoRepairUsed int64,
	now time.Time,
) repo.EquipmentEntry {
	t.Helper()
	account := repo.AccountRecord{
		AccountID: fixture.accountID,
		State:     "active",
		Metadata:  map[string]string{"keep": "original"},
	}
	if autoRepair {
		account.Metadata[premium.MetadataKey(premium.DevilSlotType(premium.DevilSlotAutoRepair))] = strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10)
	}
	if err := group.Account.Save(ctx, account); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	character := repo.CharacterRecord{
		CharacterID: fixture.characterID,
		AccountID:   fixture.accountID,
		Slot:        0,
		Name:        "DurabilitySQL" + fixture.characterID,
		Job:         "0",
		Level:       90,
		Stats:       map[string]int64{"gold": 5000},
	}
	for i := int64(0); i < autoRepairUsed; i++ {
		if !premium.TryConsumeDaily(&character, premium.DevilSlotAutoRepair, now) {
			t.Fatalf("seed auto-repair usage %d", i+1)
		}
	}
	if err := group.Character.Save(ctx, character); err != nil {
		t.Fatalf("seed character: %v", err)
	}
	raw := durabilityMySQLRaw77(current)
	entry := repo.EquipmentEntry{
		SlotIndex: 12,
		ItemID:    durabilityMySQLItemID,
		RawEntry:  raw,
		Extra: map[string]string{
			"durability":              strconv.Itoa(int(current)),
			"current_durability":      strconv.Itoa(int(current)),
			"value_b_word":            strconv.Itoa(int(current)),
			"max_durability":          "48",
			"quality_seed":            "203004656",
			"ext_data0":               "79",
			"raw_entry_hex":           hex.EncodeToString(raw),
			"equipment_raw_entry_hex": hex.EncodeToString(raw),
			"keep_me":                 "unchanged",
		},
	}
	if err := group.Equipment.Save(ctx, repo.EquipmentRecord{
		CharacterID: fixture.characterID,
		Entries:     map[string]repo.EquipmentEntry{"12": entry},
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed equipment: %v", err)
	}
	return entry
}

func durabilityMySQLRaw77(current uint16) []byte {
	raw := make([]byte, 0x77)
	binary.LittleEndian.PutUint16(raw[0:2], 12)
	binary.LittleEndian.PutUint32(raw[2:6], uint32(durabilityMySQLItemID))
	binary.LittleEndian.PutUint32(raw[6:10], 203004656)
	raw[0x0A] = 0x4F
	binary.LittleEndian.PutUint16(raw[equip.RepairDurabilityOffsetCurrentRaw77:equip.RepairDurabilityOffsetCurrentRaw77+2], current)
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], uint32(durabilityMySQLItemID))
	return raw
}

func durabilityMySQLAssertStored(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	group repo.Group,
	entriesIdent string,
	extraIdent string,
	fixture durabilityMySQLFixture,
	wantDurability uint16,
) repo.EquipmentEntry {
	t.Helper()
	record, found, err := group.Equipment.Load(ctx, fixture.characterID)
	if err != nil || !found {
		t.Fatalf("load persisted equipment found=%t err=%v", found, err)
	}
	entry, ok := record.Entries["12"]
	if !ok {
		t.Fatal("persisted equipment slot 12 missing")
	}
	if got := binary.LittleEndian.Uint16(entry.RawEntry[equip.RepairDurabilityOffsetCurrentRaw77 : equip.RepairDurabilityOffsetCurrentRaw77+2]); got != wantDurability {
		t.Fatalf("repository raw durability = %d, want %d", got, wantDurability)
	}
	if got := entry.Extra["durability"]; got != strconv.Itoa(int(wantDurability)) {
		t.Fatalf("repository Extra.durability = %q, want %d", got, wantDurability)
	}

	var raw []byte
	rawQuery := "SELECT raw_entry FROM " + entriesIdent + " WHERE character_id = ? AND entry_key = '12'"
	if err := db.QueryRowContext(ctx, rawQuery, fixture.characterID).Scan(&raw); err != nil {
		t.Fatalf("read raw equipment row: %v", err)
	}
	if len(raw) != 0x77 {
		t.Fatalf("persisted raw entry length = %d, want 119", len(raw))
	}
	if got := binary.LittleEndian.Uint16(raw[equip.RepairDurabilityOffsetCurrentRaw77 : equip.RepairDurabilityOffsetCurrentRaw77+2]); got != wantDurability {
		t.Fatalf("database raw durability = %d, want %d", got, wantDurability)
	}
	if !reflect.DeepEqual(raw, entry.RawEntry) {
		t.Fatal("repository Load raw entry differs from direct database row")
	}

	var durability string
	extraQuery := "SELECT extra_value FROM " + extraIdent + " WHERE character_id = ? AND entry_key = '12' AND extra_key = 'durability'"
	if err := db.QueryRowContext(ctx, extraQuery, fixture.characterID).Scan(&durability); err != nil {
		t.Fatalf("read durability extra row: %v", err)
	}
	if durability != strconv.Itoa(int(wantDurability)) || durability != entry.Extra["durability"] {
		t.Fatalf("database Extra.durability = %q, repository = %q, want %d", durability, entry.Extra["durability"], wantDurability)
	}
	return entry
}

func durabilityMySQLAssertOnlyDurabilityBytesChanged(t *testing.T, before, after []byte, want uint16) {
	t.Helper()
	if len(before) != len(after) || len(after) != 0x77 {
		t.Fatalf("raw lengths before=%d after=%d, want 119", len(before), len(after))
	}
	if got := binary.LittleEndian.Uint16(after[equip.RepairDurabilityOffsetCurrentRaw77 : equip.RepairDurabilityOffsetCurrentRaw77+2]); got != want {
		t.Fatalf("after durability = %d, want %d", got, want)
	}
	for i := range before {
		if i == equip.RepairDurabilityOffsetCurrentRaw77 || i == equip.RepairDurabilityOffsetCurrentRaw77+1 {
			continue
		}
		if before[i] != after[i] {
			t.Fatalf("raw byte 0x%X changed: 0x%02X -> 0x%02X", i, before[i], after[i])
		}
	}
}
