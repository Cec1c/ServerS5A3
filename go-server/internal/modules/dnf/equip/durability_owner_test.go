package equip

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	durabilityOwnerTestAccount   = "durability-account"
	durabilityOwnerTestCharacter = "77"
	durabilityOwnerTestItemID    = int64(101040493)
	durabilityOwnerTestSlot      = int16(12)
)

func durabilityOwnerTestNow() time.Time {
	return time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)
}

func durabilityOwnerTestRaw77(slot int16, itemID int64, durability uint16, packedExt byte, qualitySeed uint32) []byte {
	raw := make([]byte, currentEquipmentRawEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(slot))
	binary.LittleEndian.PutUint32(raw[2:6], uint32(itemID))
	binary.LittleEndian.PutUint32(raw[6:10], qualitySeed)
	raw[0x0A] = packedExt
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], durability)
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], uint32(itemID))
	return raw
}

func durabilityOwnerTestResolve(equipmentType string, maxDurability int64) alignedcmd.RepairCostResolver {
	return func(int64) (alignedcmd.RepairCostEvidence, error) {
		return alignedcmd.RepairCostEvidence{
			EquipmentType:   equipmentType,
			MaxDurability:   maxDurability,
			RepairPrice:     4800,
			Grade:           20,
			RepairCostRate:  0.08,
			QuickRepairRate: 1.5,
			UpgradeRates:    []float64{1, 1, 1},
		}, nil
	}
}

func seedDurabilityOwnerFixture(t *testing.T, current uint16) (context.Context, dnfrepo.Group) {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: durabilityOwnerTestAccount}); err != nil {
		t.Fatal(err)
	}
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
		CharacterID: durabilityOwnerTestCharacter,
		AccountID:   durabilityOwnerTestAccount,
		Stats:       map[string]int64{"gold": 5000},
	})
	raw := durabilityOwnerTestRaw77(durabilityOwnerTestSlot, durabilityOwnerTestItemID, current, 0x4F, 203004656)
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{
		CharacterID: durabilityOwnerTestCharacter,
		Entries: map[string]dnfrepo.EquipmentEntry{
			strconv.Itoa(int(durabilityOwnerTestSlot)): {
				SlotIndex: durabilityOwnerTestSlot,
				ItemID:    durabilityOwnerTestItemID,
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
			},
		},
	})
	return ctx, repos
}

func newDurabilityOwner(t *testing.T, repos dnfrepo.Group) *DurabilityOwner {
	t.Helper()
	owner, err := NewDurabilityOwner(repos)
	if err != nil {
		t.Fatalf("NewDurabilityOwner error = %v", err)
	}
	return owner
}

func durabilityOwnerTestCommand(accountID string, slot int16) DecreaseDurabilityCommand {
	return DecreaseDurabilityCommand{
		AccountID:   accountID,
		CharacterID: 77,
		Slot:        slot,
		Now:         durabilityOwnerTestNow(),
	}
}

func assertRawChangedOnlyAtDurability(t *testing.T, before, after []byte) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("raw length changed: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if i == RepairDurabilityOffsetCurrentRaw77 || i == RepairDurabilityOffsetCurrentRaw77+1 {
			continue
		}
		if before[i] != after[i] {
			t.Fatalf("raw byte 0x%X changed: 0x%02X -> 0x%02X", i, before[i], after[i])
		}
	}
}

func TestDurabilityOwnerOrdinaryWearPreservesExtQualityAndExtra(t *testing.T) {
	ctx, repos := seedDurabilityOwnerFixture(t, 48)
	owner := newDurabilityOwner(t, repos)
	entryBefore := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter).Entries[strconv.Itoa(int(durabilityOwnerTestSlot))]
	rawBefore := append([]byte(nil), entryBefore.RawEntry...)

	result, err := owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, durabilityOwnerTestSlot), durabilityOwnerTestResolve("[weapon]", 48))
	if err != nil {
		t.Fatalf("Decrease error = %v", err)
	}
	if result.Previous != 48 || result.Current != 47 || !result.Changed || result.AutoRepaired || result.Gold != 5000 {
		t.Fatalf("result = %+v, want 48->47 changed, auto=false, gold=5000", result)
	}
	entryAfter := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter).Entries[strconv.Itoa(int(durabilityOwnerTestSlot))]
	assertRawChangedOnlyAtDurability(t, rawBefore, entryAfter.RawEntry)
	if got := binary.LittleEndian.Uint16(entryAfter.RawEntry[0x0B:0x0D]); got != 47 {
		t.Fatalf("raw durability = %d, want 47", got)
	}
	if entryAfter.RawEntry[0x0A] != 0x4F || binary.LittleEndian.Uint32(entryAfter.RawEntry[6:10]) != 203004656 {
		t.Fatalf("packed ext/quality changed: ext=0x%02X quality=%d", entryAfter.RawEntry[0x0A], binary.LittleEndian.Uint32(entryAfter.RawEntry[6:10]))
	}
	wantExtras := map[string]string{
		"durability":              "47",
		"current_durability":      "47",
		"value_b_word":            "47",
		"max_durability":          "48",
		"quality_seed":            "203004656",
		"ext_data0":               "79",
		"raw_entry_hex":           hex.EncodeToString(entryAfter.RawEntry),
		"equipment_raw_entry_hex": hex.EncodeToString(entryAfter.RawEntry),
		"keep_me":                 "unchanged",
	}
	if !reflect.DeepEqual(entryAfter.Extra, wantExtras) {
		t.Fatalf("extra = %#v, want %#v", entryAfter.Extra, wantExtras)
	}
}

func TestDurabilityOwnerWornRowRetainsSourceBagSlot(t *testing.T) {
	for _, sourceSlot := range []int16{0, 16, 47} {
		t.Run(strconv.Itoa(int(sourceSlot)), func(t *testing.T) {
			ctx, repos := seedDurabilityOwnerFixture(t, 45)
			record := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			key := strconv.Itoa(int(durabilityOwnerTestSlot))
			entry := record.Entries[key]
			binary.LittleEndian.PutUint16(entry.RawEntry[:2], uint16(sourceSlot))
			entry.Extra["raw_entry_hex"] = hex.EncodeToString(entry.RawEntry)
			record.Entries[key] = entry
			if err := repos.Equipment.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			before := append([]byte(nil), entry.RawEntry...)

			owner := newDurabilityOwner(t, repos)
			result, err := owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, durabilityOwnerTestSlot), durabilityOwnerTestResolve("[weapon]", 48))
			if err != nil || result.Previous != 45 || result.Current != 44 || !result.Changed {
				t.Fatalf("Decrease result=%+v err=%v, want 45->44", result, err)
			}
			after := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter).Entries[key]
			assertRawChangedOnlyAtDurability(t, before, after.RawEntry)
			if got := binary.LittleEndian.Uint16(after.RawEntry[:2]); got != uint16(sourceSlot) {
				t.Fatalf("source bag slot = %d, want %d", got, sourceSlot)
			}
			if after.Extra["durability"] != "44" || after.Extra["raw_entry_hex"] != hex.EncodeToString(after.RawEntry) {
				t.Fatalf("durability shadow mismatch: %+v", after.Extra)
			}
		})
	}
}

func TestDurabilityOwnerOneToZeroDoesNotUnderflow(t *testing.T) {
	ctx, repos := seedDurabilityOwnerFixture(t, 1)
	owner := newDurabilityOwner(t, repos)
	result, err := owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, durabilityOwnerTestSlot), durabilityOwnerTestResolve("[weapon]", 48))
	if err != nil {
		t.Fatalf("Decrease error = %v", err)
	}
	if result.Previous != 1 || result.Current != 0 || !result.Changed || result.AutoRepaired {
		t.Fatalf("result = %+v, want 1->0 changed and not auto-repaired", result)
	}
	entry := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter).Entries[strconv.Itoa(int(durabilityOwnerTestSlot))]
	if got := binary.LittleEndian.Uint16(entry.RawEntry[0x0B:0x0D]); got != 0 {
		t.Fatalf("raw durability = %d, want 0", got)
	}

	result, err = owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, durabilityOwnerTestSlot), durabilityOwnerTestResolve("[weapon]", 48))
	if err != nil {
		t.Fatalf("second Decrease error = %v", err)
	}
	if result.Previous != 0 || result.Current != 0 || result.Changed || result.AutoRepaired {
		t.Fatalf("second result = %+v, want stable 0->0 changed=false", result)
	}
}

func TestDurabilityOwnerRejectsWrongOwnerSlotTypeRawAndExpiry(t *testing.T) {
	now := durabilityOwnerTestNow()
	t.Run("wrong-account", func(t *testing.T) {
		ctx, repos := seedDurabilityOwnerFixture(t, 20)
		if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "other-account"}); err != nil {
			t.Fatal(err)
		}
		before := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
		owner := newDurabilityOwner(t, repos)
		if _, err := owner.Decrease(ctx, durabilityOwnerTestCommand("other-account", durabilityOwnerTestSlot), durabilityOwnerTestResolve("[weapon]", 48)); !errors.Is(err, ErrDurabilityTargetInvalid) {
			t.Fatalf("wrong account error = %v, want ErrDurabilityTargetInvalid", err)
		}
		if after := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter); !reflect.DeepEqual(after, before) {
			t.Fatalf("wrong owner mutated equipment: before=%+v after=%+v", before, after)
		}
	})

	for _, slot := range []int16{-1, 33} {
		t.Run("invalid-slot-"+strconv.Itoa(int(slot)), func(t *testing.T) {
			ctx, repos := seedDurabilityOwnerFixture(t, 20)
			before := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			owner := newDurabilityOwner(t, repos)
			if _, err := owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, slot), durabilityOwnerTestResolve("[weapon]", 48)); !errors.Is(err, ErrDurabilityTargetInvalid) {
				t.Fatalf("slot %d error = %v, want ErrDurabilityTargetInvalid", slot, err)
			}
			if after := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter); !reflect.DeepEqual(after, before) {
				t.Fatalf("invalid slot mutated equipment: before=%+v after=%+v", before, after)
			}
		})
	}

	for _, equipmentType := range []string{"[title]", "[ring]", "[amulet]", "[wrist]", "[magic stone]"} {
		t.Run("non-wear-type-"+equipmentType, func(t *testing.T) {
			ctx, repos := seedDurabilityOwnerFixture(t, 20)
			before := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			owner := newDurabilityOwner(t, repos)
			if _, err := owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, durabilityOwnerTestSlot), durabilityOwnerTestResolve(equipmentType, 48)); !errors.Is(err, ErrRepairNotRepairable) {
				t.Fatalf("type %s error = %v, want ErrRepairNotRepairable", equipmentType, err)
			}
			if after := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter); !reflect.DeepEqual(after, before) {
				t.Fatalf("type %s mutated equipment: before=%+v after=%+v", equipmentType, before, after)
			}
		})
	}

	for _, tc := range []struct {
		name string
		raw  func([]byte) []byte
	}{
		{name: "raw77-item-mismatch", raw: func(raw []byte) []byte {
			binary.LittleEndian.PutUint32(raw[2:6], 1)
			return raw
		}},
		{name: "unsupported-length", raw: func(raw []byte) []byte {
			bad := make([]byte, 44)
			binary.LittleEndian.PutUint16(bad[0:2], uint16(durabilityOwnerTestSlot))
			binary.LittleEndian.PutUint32(bad[1:5], uint32(durabilityOwnerTestItemID))
			return bad
		}},
		{name: "durability-above-max", raw: func(raw []byte) []byte {
			binary.LittleEndian.PutUint16(raw[0x0B:0x0D], 49)
			return raw
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, repos := seedDurabilityOwnerFixture(t, 20)
			record := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			entry := record.Entries[strconv.Itoa(int(durabilityOwnerTestSlot))]
			entry.RawEntry = tc.raw(append([]byte(nil), entry.RawEntry...))
			record.Entries[strconv.Itoa(int(durabilityOwnerTestSlot))] = entry
			if err := repos.Equipment.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			before := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			owner := newDurabilityOwner(t, repos)
			if _, err := owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, durabilityOwnerTestSlot), durabilityOwnerTestResolve("[weapon]", 48)); !errors.Is(err, ErrDurabilityInvalid) {
				t.Fatalf("bad raw error = %v, want ErrDurabilityInvalid", err)
			}
			if after := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter); !reflect.DeepEqual(after, before) {
				t.Fatalf("bad raw mutated equipment: before=%+v after=%+v", before, after)
			}
		})
	}

	for _, expireAt := range []time.Time{now.Add(-time.Second), now} {
		t.Run("expired-"+expireAt.Format(time.RFC3339), func(t *testing.T) {
			ctx, repos := seedDurabilityOwnerFixture(t, 20)
			record := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			entry := record.Entries[strconv.Itoa(int(durabilityOwnerTestSlot))]
			entry.ExpireAt = expireAt
			record.Entries[strconv.Itoa(int(durabilityOwnerTestSlot))] = entry
			if err := repos.Equipment.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			before := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			owner := newDurabilityOwner(t, repos)
			if _, err := owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, durabilityOwnerTestSlot), durabilityOwnerTestResolve("[weapon]", 48)); !errors.Is(err, ErrDurabilityTargetInvalid) {
				t.Fatalf("expiry error = %v, want ErrDurabilityTargetInvalid", err)
			}
			if after := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter); !reflect.DeepEqual(after, before) {
				t.Fatalf("expired entry mutated: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestDurabilityOwnerPremium586AutoRepairUsesSixPerCharacterDailyLimit(t *testing.T) {
	ctx, repos := seedDurabilityOwnerFixture(t, 1)
	now := durabilityOwnerTestNow()
	account, _, err := repos.Account.Load(ctx, durabilityOwnerTestAccount)
	if err != nil {
		t.Fatal(err)
	}
	account.Metadata = map[string]string{
		premium.MetadataKey(premium.DevilSlotType(premium.DevilSlotAutoRepair)): strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10),
	}
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	character, _, err := repos.Character.Load(ctx, durabilityOwnerTestCharacter)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if !premium.TryConsumeDaily(&character, premium.DevilSlotAutoRepair, now) {
			t.Fatalf("seed auto-repair usage %d rejected", i+1)
		}
	}
	character.Stats["gold"] = 5000
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	owner := newDurabilityOwner(t, repos)

	result, err := owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, durabilityOwnerTestSlot), durabilityOwnerTestResolve("[weapon]", 48))
	if err != nil {
		t.Fatalf("sixth auto repair error = %v", err)
	}
	if result.Previous != 1 || result.Current != 48 || !result.AutoRepaired || !result.Changed || result.Gold != 5000 {
		t.Fatalf("sixth result = %+v, want free 1->48 auto repair", result)
	}
	character, _, err = repos.Character.Load(ctx, durabilityOwnerTestCharacter)
	if err != nil {
		t.Fatal(err)
	}
	if got := premium.DailyUsage(character, premium.DevilSlotAutoRepair, now); got != 6 {
		t.Fatalf("daily usage = %d, want 6", got)
	}
	if character.Stats["gold"] != 5000 {
		t.Fatalf("gold = %d, want unchanged 5000", character.Stats["gold"])
	}

	record := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
	key := strconv.Itoa(int(durabilityOwnerTestSlot))
	entry := record.Entries[key]
	binary.LittleEndian.PutUint16(entry.RawEntry[0x0B:0x0D], 1)
	entry.Extra["durability"] = "1"
	entry.Extra["current_durability"] = "1"
	entry.Extra["value_b_word"] = "1"
	entry.Extra["raw_entry_hex"] = hex.EncodeToString(entry.RawEntry)
	entry.Extra["equipment_raw_entry_hex"] = hex.EncodeToString(entry.RawEntry)
	record.Entries[key] = entry
	if err := repos.Equipment.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	result, err = owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, durabilityOwnerTestSlot), durabilityOwnerTestResolve("[weapon]", 48))
	if err != nil {
		t.Fatalf("capped auto repair error = %v", err)
	}
	if result.Previous != 1 || result.Current != 0 || result.AutoRepaired || !result.Changed || result.Gold != 5000 {
		t.Fatalf("capped result = %+v, want damaged 1->0, auto=false, gold=5000", result)
	}
	entry = loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter).Entries[key]
	if got := binary.LittleEndian.Uint16(entry.RawEntry[0x0B:0x0D]); got != 0 {
		t.Fatalf("capped raw durability = %d, want damaged zero", got)
	}
	character, _, err = repos.Character.Load(ctx, durabilityOwnerTestCharacter)
	if err != nil {
		t.Fatal(err)
	}
	if premium.DailyUsage(character, premium.DevilSlotAutoRepair, now) != 6 || character.Stats["gold"] != 5000 {
		t.Fatalf("capped state usage=%d gold=%d", premium.DailyUsage(character, premium.DevilSlotAutoRepair, now), character.Stats["gold"])
	}
}

func TestDurabilityOwnerConcurrentDecreaseDoesNotLoseUpdates(t *testing.T) {
	ctx, repos := seedDurabilityOwnerFixture(t, 48)
	owner := newDurabilityOwner(t, repos)
	const calls = 20
	results := make(chan DecreaseDurabilityResult, calls)
	errs := make(chan error, calls)
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, durabilityOwnerTestSlot), durabilityOwnerTestResolve("[weapon]", 48))
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Fatalf("concurrent Decrease error = %v", err)
	}
	previous := make([]int, 0, calls)
	for result := range results {
		if !result.Changed || result.AutoRepaired {
			t.Fatalf("unexpected concurrent result = %+v", result)
		}
		previous = append(previous, int(result.Previous))
	}
	sort.Ints(previous)
	wantPrevious := make([]int, calls)
	for i := range wantPrevious {
		wantPrevious[i] = 29 + i
	}
	if !reflect.DeepEqual(previous, wantPrevious) {
		t.Fatalf("previous values = %v, want every value %v exactly once", previous, wantPrevious)
	}
	entry := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter).Entries[strconv.Itoa(int(durabilityOwnerTestSlot))]
	if got := binary.LittleEndian.Uint16(entry.RawEntry[0x0B:0x0D]); got != 28 {
		t.Fatalf("final durability = %d, want 28", got)
	}
}

func TestDurabilityOwnerExpiryAliasesFailClosed(t *testing.T) {
	now := durabilityOwnerTestNow()
	tests := []struct {
		name      string
		extra     map[string]string
		expireAt  time.Time
		wantError error
	}{
		{name: "expired-time-alias", extra: map[string]string{"expire_time": strconv.FormatInt(now.Add(-time.Second).Unix(), 10)}, wantError: ErrDurabilityTargetInvalid},
		{name: "expired-unix-alias", extra: map[string]string{"expire_unix": strconv.FormatInt(now.Add(-time.Second).Unix(), 10)}, wantError: ErrDurabilityTargetInvalid},
		{name: "malformed-time-alias", extra: map[string]string{"expire_time": "not-a-time"}, wantError: ErrDurabilityInvalid},
		{name: "oversized-unix-alias", extra: map[string]string{"expire_unix": "4294967296"}, wantError: ErrDurabilityInvalid},
		{name: "zero-extra-is-permanent", extra: map[string]string{"expire_time": "0", "expire_unix": "0"}},
		{name: "future-extra-only", extra: map[string]string{"expire_time": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}},
		{name: "future-unix-hex", extra: map[string]string{"expire_unix": "0x" + strconv.FormatInt(now.Add(time.Hour).Unix(), 16)}},
		{name: "future-field-authority", expireAt: now.Add(time.Hour)},
		{name: "zero-extra-future-field", extra: map[string]string{"expire_time": "0"}, expireAt: now.Add(time.Hour)},
		{name: "expired-field-wins-over-future-extra", extra: map[string]string{"expire_time": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}, expireAt: now, wantError: ErrDurabilityTargetInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, repos := seedDurabilityOwnerFixture(t, 20)
			record := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			key := strconv.Itoa(int(durabilityOwnerTestSlot))
			entry := record.Entries[key]
			for name, value := range tc.extra {
				entry.Extra[name] = value
			}
			entry.ExpireAt = tc.expireAt
			record.Entries[key] = entry
			if err := repos.Equipment.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			before := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			owner := newDurabilityOwner(t, repos)
			result, err := owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, durabilityOwnerTestSlot), durabilityOwnerTestResolve("[weapon]", 48))
			if tc.wantError != nil {
				if !errors.Is(err, tc.wantError) {
					t.Fatalf("Decrease error = %v, want %v", err, tc.wantError)
				}
				if after := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter); !reflect.DeepEqual(after, before) {
					t.Fatalf("rejected expiry mutated equipment: before=%+v after=%+v", before, after)
				}
				return
			}
			if err != nil || result.Previous != 20 || result.Current != 19 || !result.Changed {
				t.Fatalf("valid expiry result=%+v err=%v, want 20->19", result, err)
			}
		})
	}
}

func TestDurabilityOwnerDurabilityAliasesAreAuthoritative(t *testing.T) {
	tests := []struct {
		name              string
		durability        string
		currentDurability string
		raw               uint16
		wantPrevious      uint16
		wantCurrent       uint16
		wantError         error
	}{
		{name: "explicit-zero-does-not-revive-stale-raw", durability: "0", currentDurability: "48", raw: 48, wantPrevious: 0, wantCurrent: 0},
		{name: "explicit-seven-overrides-stale-raw", durability: "7", currentDurability: "48", raw: 48, wantPrevious: 7, wantCurrent: 6},
		{name: "primary-durability-overrides-current-alias", durability: "48", currentDurability: "0", raw: 48, wantPrevious: 48, wantCurrent: 47},
		{name: "current-durability-is-second-choice", durability: "", currentDurability: "7", raw: 48, wantPrevious: 7, wantCurrent: 6},
		{name: "no-alias-uses-raw", durability: "", currentDurability: "", raw: 48, wantPrevious: 48, wantCurrent: 47},
		{name: "malformed-primary-does-not-fall-through", durability: "not-a-number", currentDurability: "7", raw: 48, wantError: ErrDurabilityInvalid},
		{name: "overflowing-primary-does-not-fall-through", durability: "65536", currentDurability: "7", raw: 48, wantError: ErrDurabilityInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, repos := seedDurabilityOwnerFixture(t, 48)
			record := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			key := strconv.Itoa(int(durabilityOwnerTestSlot))
			entry := record.Entries[key]
			binary.LittleEndian.PutUint16(entry.RawEntry[0x0B:0x0D], tc.raw)
			entry.Extra["durability"] = tc.durability
			entry.Extra["current_durability"] = tc.currentDurability
			record.Entries[key] = entry
			if err := repos.Equipment.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			before := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			owner := newDurabilityOwner(t, repos)
			result, err := owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, durabilityOwnerTestSlot), durabilityOwnerTestResolve("[weapon]", 48))
			if tc.wantError != nil {
				if !errors.Is(err, tc.wantError) {
					t.Fatalf("Decrease error = %v, want %v", err, tc.wantError)
				}
				if after := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter); !reflect.DeepEqual(after, before) {
					t.Fatalf("malformed durability mutated equipment: before=%+v after=%+v", before, after)
				}
				return
			}
			if err != nil || result.Previous != tc.wantPrevious || result.Current != tc.wantCurrent || !result.Changed {
				t.Fatalf("result=%+v err=%v, want %d->%d changed", result, err, tc.wantPrevious, tc.wantCurrent)
			}
			entry = loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter).Entries[key]
			if got := binary.LittleEndian.Uint16(entry.RawEntry[0x0B:0x0D]); got != tc.wantCurrent {
				t.Fatalf("raw durability = %d, want %d", got, tc.wantCurrent)
			}
			wantAlias := strconv.Itoa(int(tc.wantCurrent))
			for _, alias := range []string{"durability", "current_durability", "value_b_word"} {
				if entry.Extra[alias] != wantAlias {
					t.Fatalf("%s = %q, want %q", alias, entry.Extra[alias], wantAlias)
				}
			}
		})
	}
}

func TestDurabilityOwnerWeapon24RequiresVagabondBranch(t *testing.T) {
	tests := []struct {
		name          string
		slot          int16
		equipmentType string
		job           string
		grow          int64
		growPresent   bool
		wantError     error
	}{
		{name: "support-weapon-any-job", slot: 24, equipmentType: "[support weapon]", job: "10"},
		{name: "weapon24-vagabond-branch4", slot: 24, equipmentType: "[weapon]", job: "11", grow: 0x04, growPresent: true},
		{name: "weapon24-awakened-vagabond-branch4", slot: 24, equipmentType: "[weapon]", job: "11", grow: 0x24, growPresent: true},
		{name: "weapon24-wrong-branch", slot: 24, equipmentType: "[weapon]", job: "11", grow: 0x03, growPresent: true, wantError: ErrRepairNotRepairable},
		{name: "weapon24-wrong-job", slot: 24, equipmentType: "[weapon]", job: "10", grow: 0x04, growPresent: true, wantError: ErrRepairNotRepairable},
		{name: "weapon24-missing-growth", slot: 24, equipmentType: "[weapon]", job: "11", wantError: ErrRepairNotRepairable},
		{name: "charm31-is-not-worn-equipment", slot: 31, equipmentType: "[charm]", job: "11", grow: 0x04, growPresent: true, wantError: ErrRepairNotRepairable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, repos := seedDurabilityOwnerFixture(t, 20)
			record := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			oldKey := strconv.Itoa(int(durabilityOwnerTestSlot))
			entry := record.Entries[oldKey]
			delete(record.Entries, oldKey)
			entry.SlotIndex = tc.slot
			entry.RawEntry = durabilityOwnerTestRaw77(tc.slot, durabilityOwnerTestItemID, 20, 0x4F, 203004656)
			record.Entries[strconv.Itoa(int(tc.slot))] = entry
			if err := repos.Equipment.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			character, _, err := repos.Character.Load(ctx, durabilityOwnerTestCharacter)
			if err != nil {
				t.Fatal(err)
			}
			character.Job = tc.job
			if tc.growPresent {
				character.Stats[dnfrepo.CharacterGrowTypeStatKey] = tc.grow
			}
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			before := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			owner := newDurabilityOwner(t, repos)
			result, err := owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, tc.slot), durabilityOwnerTestResolve(tc.equipmentType, 48))
			if tc.wantError != nil {
				if !errors.Is(err, tc.wantError) {
					t.Fatalf("Decrease error = %v, want %v", err, tc.wantError)
				}
				if after := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter); !reflect.DeepEqual(after, before) {
					t.Fatalf("rejected slot mutated equipment: before=%+v after=%+v", before, after)
				}
				return
			}
			if err != nil || result.Previous != 20 || result.Current != 19 || !result.Changed {
				t.Fatalf("accepted slot result=%+v err=%v, want 20->19", result, err)
			}
		})
	}
}

type durabilityOwnerFaultAssets struct {
	group dnfrepo.Group
	fail  error
	order []string
}

var _ dnfrepo.AccountCharacterAssetUnitOfWork = (*durabilityOwnerFaultAssets)(nil)

func (a *durabilityOwnerFaultAssets) WithinAccountCharacterAssets(
	context.Context, string, string,
	func(dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	return dnfrepo.ErrAccountCharacterAssetTransactionUnavailable
}

func (a *durabilityOwnerFaultAssets) WithinAccountCharacterLedgerAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(dnfrepo.AccountRepository, dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if accountID == "" || characterID == "" || apply == nil {
		return dnfrepo.ErrAccountCharacterAssetTransactionUnavailable
	}
	account, found, err := a.group.Account.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found || account.AccountID != accountID {
		return dnfrepo.ErrAccountCharacterAssetTransactionUnavailable
	}
	characterBefore, characterFound, err := a.group.Character.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !characterFound {
		return ErrDurabilityTargetInvalid
	}
	equipmentBefore, equipmentFound, err := a.group.Equipment.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !equipmentFound {
		return ErrEquipmentNotFound
	}
	characterTx := &durabilityOwnerStagedCharacter{
		CharacterRepository: a.group.Character,
		characterID:         characterID,
		record:              dnfrepo.CloneCharacter(characterBefore),
		exists:              true,
	}
	equipmentTx := &durabilityOwnerStagedEquipment{
		EquipmentRepository: a.group.Equipment,
		characterID:         characterID,
		record:              dnfrepo.CloneEquipment(equipmentBefore),
		exists:              true,
	}
	if err := apply(a.group.Account, a.group.AccountInventory, characterTx, a.group.Inventory, equipmentTx); err != nil {
		return err
	}

	characterAttempted := false
	equipmentAttempted := false
	rollback := func(cause error) error {
		joined := []error{cause}
		if equipmentAttempted {
			joined = append(joined, a.group.Equipment.Save(context.Background(), dnfrepo.CloneEquipment(equipmentBefore)))
		}
		if characterAttempted {
			joined = append(joined, a.group.Character.Save(context.Background(), dnfrepo.CloneCharacter(characterBefore)))
		}
		return errors.Join(joined...)
	}
	if characterTx.dirty {
		characterAttempted = true
		if err := a.group.Character.Save(ctx, characterTx.record); err != nil {
			return rollback(err)
		}
		a.order = append(a.order, "character")
	}
	if equipmentTx.dirty {
		equipmentAttempted = true
		if err := a.group.Equipment.Save(ctx, equipmentTx.record); err != nil {
			return rollback(err)
		}
		a.order = append(a.order, "equipment")
		if a.fail != nil {
			return rollback(a.fail)
		}
	}
	return nil
}

type durabilityOwnerStagedCharacter struct {
	dnfrepo.CharacterRepository
	characterID string
	record      dnfrepo.CharacterRecord
	exists      bool
	dirty       bool
}

func (s *durabilityOwnerStagedCharacter) Load(ctx context.Context, characterID string) (dnfrepo.CharacterRecord, bool, error) {
	if err := dnfrepo.TransactionKeyError(ctx, s.characterID, characterID); err != nil {
		return dnfrepo.CharacterRecord{}, false, err
	}
	return dnfrepo.CloneCharacter(s.record), s.exists, nil
}

func (s *durabilityOwnerStagedCharacter) Save(ctx context.Context, record dnfrepo.CharacterRecord) error {
	if err := dnfrepo.TransactionKeyError(ctx, s.characterID, record.CharacterID); err != nil {
		return err
	}
	s.record = dnfrepo.CloneCharacter(record)
	s.exists = true
	s.dirty = true
	return nil
}

type durabilityOwnerStagedEquipment struct {
	dnfrepo.EquipmentRepository
	characterID string
	record      dnfrepo.EquipmentRecord
	exists      bool
	dirty       bool
}

func (s *durabilityOwnerStagedEquipment) Load(ctx context.Context, characterID string) (dnfrepo.EquipmentRecord, bool, error) {
	if err := dnfrepo.TransactionKeyError(ctx, s.characterID, characterID); err != nil {
		return dnfrepo.EquipmentRecord{}, false, err
	}
	return dnfrepo.CloneEquipment(s.record), s.exists, nil
}

func (s *durabilityOwnerStagedEquipment) Save(ctx context.Context, record dnfrepo.EquipmentRecord) error {
	if err := dnfrepo.TransactionKeyError(ctx, s.characterID, record.CharacterID); err != nil {
		return err
	}
	s.record = dnfrepo.CloneEquipment(record)
	s.exists = true
	s.dirty = true
	return nil
}

func TestDurabilityOwnerEquipmentFinalSaveFailureRollsBackBothOwners(t *testing.T) {
	ctx, repos := seedDurabilityOwnerFixture(t, 1)
	now := durabilityOwnerTestNow()
	account, _, err := repos.Account.Load(ctx, durabilityOwnerTestAccount)
	if err != nil {
		t.Fatal(err)
	}
	account.Metadata = map[string]string{
		premium.MetadataKey(premium.DevilSlotType(premium.DevilSlotAutoRepair)): strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
	}
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	characterBefore, _, err := repos.Character.Load(ctx, durabilityOwnerTestCharacter)
	if err != nil {
		t.Fatal(err)
	}
	equipmentBefore := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
	commitErr := errors.New("last equipment save failed")
	faults := &durabilityOwnerFaultAssets{group: repos, fail: commitErr}
	repos.AccountAssets = faults
	owner := newDurabilityOwner(t, repos)

	if _, err := owner.Decrease(ctx, durabilityOwnerTestCommand(durabilityOwnerTestAccount, durabilityOwnerTestSlot), durabilityOwnerTestResolve("[weapon]", 48)); !errors.Is(err, commitErr) {
		t.Fatalf("Decrease error = %v, want %v", err, commitErr)
	}
	if !reflect.DeepEqual(faults.order, []string{"character", "equipment"}) {
		t.Fatalf("commit order = %v, want character then equipment", faults.order)
	}
	characterAfter, _, err := repos.Character.Load(ctx, durabilityOwnerTestCharacter)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(characterAfter, characterBefore) {
		t.Fatalf("character stats/daily usage changed after rollback: before=%+v after=%+v", characterBefore, characterAfter)
	}
	equipmentAfter := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
	if !reflect.DeepEqual(equipmentAfter, equipmentBefore) {
		t.Fatalf("equipment changed after rollback: before=%+v after=%+v", equipmentBefore, equipmentAfter)
	}
	if !bytes.Equal(equipmentAfter.Entries[strconv.Itoa(int(durabilityOwnerTestSlot))].RawEntry[0x0B:0x0D], []byte{1, 0}) {
		t.Fatalf("rolled-back durability bytes = %x, want 0100", equipmentAfter.Entries[strconv.Itoa(int(durabilityOwnerTestSlot))].RawEntry[0x0B:0x0D])
	}
}
