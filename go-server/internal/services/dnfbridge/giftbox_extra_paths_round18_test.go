package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestGiftBoxExtraPathIdentityRound18(t *testing.T) {
	const extraPath = "stackable/10304001/10304500.stk"
	const kind = "[grouped random box]"
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, path, kind string
		want             bool
	}{
		{"verified pair", extraPath, kind, true},
		{"same type other path", "stackable/unverified.stk", kind, false},
		{"verified path wrong type", extraPath, "[quest]", false},
		{"unknown quest box", "stackable/chn_box_20min_invitation_10002350.stk", "[quest]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := fmt.Sprintf("[version]\n1\n[target]\n%d\n[entry]\n`%s` `%s` %d\n[/entry]\n", itemexpiration.GiftBoxHistoryDeadline, tc.path, tc.kind, giftHistoryOldRound17)
			_, err := loadCurrentGiftBoxExpirationHistory(giftHistorySourceRound17{itemexpiration.GiftBoxHistoryPath: manifest})
			if (err == nil) != tc.want {
				t.Fatalf("manifest accepted=%t want=%t: %v", err == nil, tc.want, err)
			}
			def := giftHistoryDefinitionRound17()
			def.PVFPath, def.StackableType = tc.path, tc.kind
			before := giftHistoryStackRound17(giftHistoryOldRound17, nil)
			after, _ := applyCurrentPVFItemExpirationAt(before, def, now)
			want := giftHistoryOldRound17
			if tc.want {
				want = itemexpiration.GiftBoxHistoryDeadline
			}
			if got := currentItemListStackExpire(after); got != want {
				t.Fatalf("renewal=%d want=%d", got, want)
			}
		})
	}
}

// Read real original definitions to derive their exact old deadlines and use
// real candidate definitions for the production projection/wire path. No fake
// item IDs are inserted into the production catalog.
func TestRealGiftBoxExtraPathsRound18(t *testing.T) {
	source, candidate := os.Getenv("DNF_GIFTBOX_EXPIRY_SOURCE"), os.Getenv("DNF_GIFTBOX_HISTORY_CANDIDATE")
	if source == "" || candidate == "" {
		t.Skip("set source and candidate PVF paths")
	}
	a, err := platformpvf.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	b, err := platformpvf.Open(candidate)
	if err != nil {
		t.Fatal(err)
	}
	old, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	next, err := newPVFDungeonDropCatalog(b)
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{
		"stackable/10304001/10304500.stk":                                                                     false,
		"stackable/cash/chn_20150129_mysterious_hammer_pocket/chn_avatar_box_10004710.stk":                    false,
		"stackable/event/chn_choice_dungeon_2014_01/chn_stk_firework_money_02.stk":                            false,
		"stackable/event/chn_event_20131107_solo_paradise_couple_hell/chn_stk_rose_without_love_10002497.stk": false,
		"stackable/event/chn_choice_dungeon_2014_07/chn_stk_firework_money_02_10003865.stk":                   false,
		"stackable/cash/chn_20131210_ameba_box/chn_ameba_cerashop_2683281.stk":                                false,
		"stackable/event/chn_event_20141111_solo_day/chn_stk_rose_love_10004383.stk":                          false,
		"stackable/490006001/chn_490006626.stk":                                                               false,
		"stackable/event/chn_choice_dungeon_2013_09/chn_stk_firework_money_02.stk":                            false,
	}
	// This document is not selected by the current production item index.
	// Verify its script identity and history without inventing an item ID;
	// the other eight must reach the actual resolver and wire projection below.
	unselected := map[string]bool{
		"stackable/10304001/10304500.stk": true,
	}
	for p := range unselected {
		row, found := next.giftBoxHistory[p]
		if !found {
			t.Fatalf("extra document missing history %s", p)
		}
		if err := giftHistoryVerifyUnlistedScriptRound17(next, p, row); err != nil {
			t.Fatalf("extra document %s: %v", p, err)
		}
	}
	// Production references may have a root already, or select a revised file.
	for id, ref := range old.itemRefs {
		if ref.kind != dungeonDropItemStackable {
			continue
		}
		p, _, readErr := readDungeonDropText(a, string(ref.kind), ref.path)
		p = strings.ToLower(p)
		if _, exists := wanted[p]; !exists {
			continue
		}
		if unselected[p] {
			t.Fatalf("document now selected by production list: %s id=%d; update reachability evidence", p, id)
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		original, err := old.ResolveItem(id)
		if err != nil {
			t.Fatal(err)
		}
		def, err := next.ResolveItem(id)
		if err != nil {
			t.Fatal(err)
		}
		if !itemexpiration.IsGiftBoxPath(p, def.StackableType) || len(def.GiftBoxExpirationHistory) == 0 || currentPVFExpirationUnix(def.ExpirationDate) != itemexpiration.GiftBoxHistoryDeadline {
			t.Fatalf("unproved extra item %d %+v", id, def)
		}
		wantOld := currentPVFExpirationUnix(original.ExpirationDate)
		if wantOld == 0 {
			t.Fatalf("missing old date %d", id)
		}
		stack := giftHistoryStackRound17(wantOld, nil)
		stack.ItemID = int64(id)
		rows := []currentItemListEntry{currentItemListEntryFromStack(0, 9, stack)}
		if _, err := (&Service{}).applyCurrentPVFUsePeriodsToEntriesWithCatalog(context.Background(), rows, next, time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
		if got := binary.LittleEndian.Uint32(rows[0].data[currentItemListExpireTimeOffset:]); got != itemexpiration.GiftBoxHistoryDeadline {
			t.Fatalf("real item %d wire deadline %d", id, got)
		}
		protected := giftHistoryStackRound17(wantOld, map[string]string{"expiration_source": "gm_override", "gm_expire_days": "30"})
		protected.ItemID = int64(id)
		projected, _ := applyCurrentPVFItemExpirationAt(protected, def, time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
		if currentItemListStackExpire(projected) != wantOld {
			t.Fatalf("GM exact date collision changed item %d", id)
		}
		wanted[p] = true
		t.Logf("extra path=%s id=%d old=%d history=%v", p, id, wantOld, def.GiftBoxExpirationHistory)
	}
	for p, covered := range wanted {
		if !covered && !unselected[p] {
			t.Errorf("no production-list resolution for extra path %s", p)
		}
	}
}
