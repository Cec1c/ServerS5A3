package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Round 17 / task B: which expiration sources the shared wire late pass keeps
// once only the 0x77 row survives, checked against the parent's independent
// expiration flag + giftbox-history protection.
//
// Entry point: (*Service).applyCurrentPVFUsePeriodsToEntriesWithCatalog, the one
// late pass behind the login snapshot (main bag + warehouse), account cargo, the
// equipment snapshot and the occupied-equipment refresh. Mail attachments are
// projected by mailbox_pvf.go / reconcileCurrentMailAttachmentExpirations and are
// not claimed here.
//
// Definitions are injected through pvfDungeonDropCatalog.itemCache, so these
// cases do not depend on how the manifest-backed loader finds history
// (giftbox_expiration_history.go reads etc/local/giftbox_expiration_history.etc
// only for archives with an explicit file inventory). The history list is a plain
// []uint32 on the definition, exactly what resolve attaches for a matching box.
//
// The flag is local: currentItemListEntry.independentExpiration is filled only by
// currentItemListEntryFromStack (current_item_list_stack_projection.go:36) and the
// equipment entry constructor (current_item_list_equipment_projection.go:28) from
// the stack/equipment Extra. Raw constructors leave it false, which is the
// boundary covered below.
const (
	r17bBoxItemID     uint32 = 490099101
	r17bUsableBoxID   uint32 = 490099102
	r17bDailyBoxID    uint32 = 490099103
	r17bBoxPVFPath           = "stackable/round17b/chn_490099101.stk"
	r17bUsableBoxPath        = "stackable/round17b/chn_490099102.stk"
	r17bDailyBoxPath         = "stackable/round17b/chn_490099103.stk"
)

// r17bOldValue and r17bHorizonValue stand for the two deadlines the patch tool
// records for an already retired declaration: the exact old value plus 1830268800
// (2028-01-01 00:00 CST), the horizon the retired-date policy had written.
const (
	r17bOldValue     uint32 = 1531951200
	r17bHorizonValue uint32 = 1830268800
)

func r17bNow() time.Time { return time.Date(2026, time.October, 16, 4, 0, 0, 0, time.UTC) }

func r17bBoxDefinition(itemID uint32, pvfPath string) dungeonDropItemDefinition {
	return dungeonDropItemDefinition{
		ItemID:                   itemID,
		Kind:                     dungeonDropItemStackable,
		PVFPath:                  pvfPath,
		StackableType:            "[booster]",
		StackLimit:               1000,
		ExpirationDate:           time.Unix(int64(itemexpiration.GiftBoxHistoryDeadline), 0).UTC(),
		GiftBoxExpirationHistory: []uint32{r17bOldValue, r17bHorizonValue},
	}
}

func r17bCatalogWithDefinitions(definitions ...dungeonDropItemDefinition) *pvfDungeonDropCatalog {
	cache := make(map[uint32]dungeonDropItemDefinition, len(definitions))
	for _, definition := range definitions {
		cache[definition.ItemID] = definition
	}
	return &pvfDungeonDropCatalog{source: dungeonDropCatalogTestSource{}, itemCache: cache}
}

func r17bStack(itemID uint32, expire uint32, extra map[string]string) dnfrepo.ItemStack {
	merged := map[string]string{"expire_time": strconv.FormatUint(uint64(expire), 10), "item_kind": string(dungeonDropItemStackable)}
	for key, value := range extra {
		merged[key] = value
	}
	return dnfrepo.ItemStack{ItemID: int64(itemID), Count: 1, ExpireAt: time.Unix(int64(expire), 0).UTC(), Extra: merged}
}

func r17bRowExpire(entry currentItemListEntry) uint32 {
	return binary.LittleEndian.Uint32(entry.data[currentItemListExpireTimeOffset : currentItemListExpireTimeOffset+4])
}

// TestRound17bStackEntriesCarryIndependentExpirationFromExtra proves the flag
// survives the actual stack constructor for every source that owns its own
// deadline, including the keys written by the production rental and daily
// producers. The definition shape also excludes relative and daily items.
func TestRound17bStackEntriesCarryIndependentExpirationFromExtra(t *testing.T) {
	cases := []struct {
		name      string
		extra     map[string]string
		protected bool
	}{
		{name: "gm override with days ledger", extra: map[string]string{"expiration_source": currentGMExpirationOverrideSource, "gm_expire_days": "30"}, protected: true},
		{name: "gm override marker alone", extra: map[string]string{"expiration_source": currentGMExpirationOverrideSource}, protected: true},
		{name: "relative grant source", extra: map[string]string{"expiration_source": "runtime_pvf_usable_period_grant", "usable_period_days": "1"}, protected: true},
		{name: "relative grant by day count", extra: map[string]string{"usable_period_days": "1"}, protected: true},
		{name: "production rental keys", extra: map[string]string{"source": currentRentalItemSource, "rental_duration_hours": "24"}, protected: true},
		{name: "production daily key", extra: map[string]string{itemexpiration.DailyDeleteDeadlineKey: "1790000000"}, protected: true},
		{name: "plain runtime pvf row", extra: map[string]string{"expiration_source": currentPVFWrongExpirationSource}, protected: false},
		{name: "row without any source key", extra: nil, protected: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stack := r17bStack(r17bBoxItemID, r17bHorizonValue, testCase.extra)
			entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, 40, stack)
			if !entry.expirationProvenanceKnown || entry.independentExpiration != testCase.protected {
				t.Fatalf("independentExpiration=%t want %t for extra=%v", entry.independentExpiration, testCase.protected, testCase.extra)
			}
		})
	}
}

// TestRound17bGiftBoxHistoryRenewalNeedsTheStackDerivedFlag is the boundary in
// question. A row whose bytes still come from a stack keeps its own deadline;
// the identical bytes arriving as a raw-only row keep their deadline because
// their ownership metadata is unknown. A known unprotected stack is renewed on
// an exact history match; an unlisted value is never renewed.
func TestRound17bGiftBoxHistoryRenewalNeedsTheStackDerivedFlag(t *testing.T) {
	definition := r17bBoxDefinition(r17bBoxItemID, r17bBoxPVFPath)
	catalog := r17bCatalogWithDefinitions(definition)
	service := &Service{pvfItemCatalog: catalog}
	now := r17bNow()
	horizonUnix := currentPVFExpirationUnix(definition.ExpirationDate)
	if horizonUnix != itemexpiration.GiftBoxHistoryDeadline || len(definition.GiftBoxExpirationHistory) != 2 {
		t.Fatalf("fixture definition=%+v", definition)
	}

	// A GM grant whose deadline happens to equal a recorded history value: the
	// stack still carries expiration_source, so the row is protected.
	gmStack := r17bStack(r17bBoxItemID, r17bHorizonValue, map[string]string{
		"expiration_source": currentGMExpirationOverrideSource,
		"gm_expire_days":    "30",
	})
	protected := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, 41, gmStack)
	if !protected.independentExpiration || r17bRowExpire(protected) != r17bHorizonValue {
		t.Fatalf("protected row flag=%t 0x38=%d", protected.independentExpiration, r17bRowExpire(protected))
	}
	// The store-side projection agrees: with the GM gate off (default in tests)
	// the marked stack still keeps its own deadline.
	kept, _ := applyCurrentPVFItemExpirationAt(gmStack, definition, now)
	if got := currentItemListStackExpire(kept); got != r17bHorizonValue {
		t.Fatalf("store projection moved the protected deadline to %d want %d", got, r17bHorizonValue)
	}
	// The identical bytes with no metadata at all: a raw-only wire entry.
	wireOnly := currentItemListEntry{data: protected.data}

	stale := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, 42, r17bStack(r17bBoxItemID, r17bOldValue, nil))
	if stale.independentExpiration || r17bRowExpire(stale) != r17bOldValue {
		t.Fatalf("stale row flag=%t 0x38=%d", stale.independentExpiration, r17bRowExpire(stale))
	}
	// Not a recorded value: an instance deadline next to the old one.
	unlisted := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, 43, r17bStack(r17bBoxItemID, r17bOldValue+12345, nil))

	entries := []currentItemListEntry{protected, wireOnly, stale, unlisted}
	patched, err := service.applyCurrentPVFUsePeriodsToEntriesWithCatalog(context.Background(), entries, catalog, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("late pass patched %d of %d rows", patched, len(entries))

	if got := r17bRowExpire(entries[0]); got != r17bHorizonValue {
		t.Fatalf("protected stack row 0x38=%d want %d (renewed date would be %d)", got, r17bHorizonValue, horizonUnix)
	}
	if got := r17bRowExpire(entries[1]); got != r17bHorizonValue {
		t.Fatalf("raw-only row 0x38=%d want preserved %d (metadata unavailable)", got, r17bHorizonValue)
	}
	if got := r17bRowExpire(entries[2]); got != horizonUnix {
		t.Fatalf("stale copy row 0x38=%d want renewed %d", got, horizonUnix)
	}
	if got := r17bRowExpire(entries[3]); got != r17bOldValue+12345 {
		t.Fatalf("unlisted row 0x38=%d want %d (history matching is exact, never <=)", got, r17bOldValue+12345)
	}
}

// TestRound17bRelativeAndDailyRowsSurviveTheLatePass covers the two sources the
// history segment cannot cover: a relative [usable period] grant and a
// [daily delete item] ticket keep their own deadline through the late pass.
func TestRound17bRelativeAndDailyRowsSurviveTheLatePass(t *testing.T) {
	usable := r17bBoxDefinition(r17bUsableBoxID, r17bUsableBoxPath)
	usable.UsablePeriodDays = 1
	daily := r17bBoxDefinition(r17bDailyBoxID, r17bDailyBoxPath)
	daily.DailyDeleteItem = true
	catalog := r17bCatalogWithDefinitions(usable, daily)
	service := &Service{pvfItemCatalog: catalog}
	now := r17bNow()
	declaredUnix := itemexpiration.GiftBoxHistoryDeadline

	t.Run("relative usable period", func(t *testing.T) {
		grantDefinition, err := currentPVFItemDefinitionForGrantAt(usable, now)
		if err != nil {
			t.Fatal(err)
		}
		want := time.Unix(now.Unix()+24*60*60, 0).UTC()
		if !grantDefinition.ExpirationDate.Equal(want) {
			t.Fatalf("grant deadline=%s want %s", grantDefinition.ExpirationDate, want)
		}
		granted, _ := applyCurrentPVFItemExpirationAt(dnfrepo.ItemStack{ItemID: int64(r17bUsableBoxID), Count: 1}, grantDefinition, now)
		entries := []currentItemListEntry{currentItemListEntryFromStack(dnfrepo.MainInventoryListType, 44, granted)}
		if !entries[0].independentExpiration {
			t.Fatal("relative grant row lost its independent-expiration flag")
		}
		if _, err := service.applyCurrentPVFUsePeriodsToEntriesWithCatalog(context.Background(), entries, catalog, now); err != nil {
			t.Fatal(err)
		}
		if got := r17bRowExpire(entries[0]); got != uint32(want.Unix()) {
			t.Fatalf("relative row 0x38=%d want %d (declared date is %d)", got, uint32(want.Unix()), declaredUnix)
		}
	})

	t.Run("daily delete ticket", func(t *testing.T) {
		deadline := itemexpiration.NextDailyDeleteAt(now)
		stack := r17bStack(r17bDailyBoxID, uint32(deadline.Unix()), map[string]string{
			itemexpiration.DailyDeleteDeadlineKey: strconv.FormatInt(deadline.Unix(), 10),
		})
		entries := []currentItemListEntry{currentItemListEntryFromStack(dnfrepo.MainInventoryListType, 45, stack)}
		if _, err := service.applyCurrentPVFUsePeriodsToEntriesWithCatalog(context.Background(), entries, catalog, now); err != nil {
			t.Fatal(err)
		}
		if got := r17bRowExpire(entries[0]); got != uint32(deadline.Unix()) {
			t.Fatalf("daily row 0x38=%d want %d (declared date is %d)", got, uint32(deadline.Unix()), declaredUnix)
		}
	})
}

// TestRound17bGiftBoxHistoryExcludesRelativeAndDailyShapes records why the patch
// tool must skip those documents and why their pre-patch rows keep the old value:
// the renewal rule requires an absolute-date stackable box, so a recorded
// deadline can never be applied to a [usable period] or [daily delete item] box
// even when a definition carries a history list.
func TestRound17bGiftBoxHistoryExcludesRelativeAndDailyShapes(t *testing.T) {
	usable := r17bBoxDefinition(r17bUsableBoxID, r17bUsableBoxPath)
	usable.UsablePeriodDays = 1
	daily := r17bBoxDefinition(r17bDailyBoxID, r17bDailyBoxPath)
	daily.DailyDeleteItem = true
	for name, definition := range map[string]dungeonDropItemDefinition{"usable period": usable, "daily delete": daily} {
		t.Run(name, func(t *testing.T) {
			if currentGiftBoxInstanceNeedsRenewal(definition, r17bOldValue) {
				t.Fatalf("definition=%+v renews through a history list it cannot own", definition)
			}
			if renewed := currentGiftBoxHistoryExpiration(definition, r17bOldValue, false); !renewed.IsZero() {
				t.Fatalf("unprotected %s shape renewed to %s", name, renewed)
			}
		})
	}
	absolute := r17bBoxDefinition(r17bBoxItemID, r17bBoxPVFPath)
	if !currentGiftBoxInstanceNeedsRenewal(absolute, r17bOldValue) {
		t.Fatalf("absolute-date box with history=%v does not renew its recorded old date", absolute.GiftBoxExpirationHistory)
	}
}
