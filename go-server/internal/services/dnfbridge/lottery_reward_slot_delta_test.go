package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func lotteryRewardSlotDeltaCatalog(t *testing.T, rewardCount uint32) *pvfDungeonDropCatalog {
	t.Helper()
	_, source := newMagicBoxTestCatalog(t)
	source["stackable/stackable.lst"] += "900 `lottery/source.stk`\n"
	source["stackable/lottery/source.stk"] = fmt.Sprintf(
		"[stackable type] `[upgradable legacy]` 1\n[stack limit] 100\n[int data] 2600014 100 %d\n[/int data]\n",
		rewardCount,
	)
	source["stackable/potion/ptn.stk"] = "[name] `Lottery Delta Reward`\n[stackable type] `[material]`\n[stack limit] 2\n"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func lotteryRewardSlotDeltaRepos(t *testing.T, slots map[string]dnfrepo.ItemStack) dnfrepo.Group {
	t.Helper()
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Stats:       map[string]int64{"gold": 1000, "fatigue": 120},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       dnfrepo.CloneInventory(dnfrepo.InventoryRecord{Slots: slots}).Slots,
	}); err != nil {
		t.Fatal(err)
	}
	return repos
}

func lotteryRewardSlotDeltas(before, after map[string]dnfrepo.ItemStack, itemID int64) map[int16]int64 {
	result := make(map[int16]int64)
	for key, stack := range after {
		listType, slot, parsed := parseSceneInventorySlotKey(key)
		if !parsed || listType != dnfrepo.MainInventoryListType || stack.ItemID != itemID {
			continue
		}
		if delta := stack.Count - before[key].Count; delta > 0 {
			result[slot] = delta
		}
	}
	return result
}

func lotteryRewardSlotExpectedDisplay(deltas map[int16]int64) (int16, int64) {
	var slot int16
	var delta int64
	for candidateSlot, candidateDelta := range deltas {
		if delta == 0 || candidateDelta > delta || (candidateDelta == delta && candidateSlot < slot) {
			slot, delta = candidateSlot, candidateDelta
		}
	}
	return slot, delta
}

func TestLotteryRewardSlotDeltaRecordsActualGrantTargets(t *testing.T) {
	ctx := context.Background()
	beforeSlots := map[string]dnfrepo.ItemStack{
		"0:75": {ItemID: 900, Count: 1},
	}
	repos := lotteryRewardSlotDeltaRepos(t, beforeSlots)
	catalog := lotteryRewardSlotDeltaCatalog(t, 5)
	service := currentBoosterTestService(catalog, repos)
	service.options.currentClientVersion = "official-24.9"
	session := &gameSession{selectedCharacterID: 19}
	request := currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: 75, AllowLotteryGold: true}
	definition, err := service.prepareCurrentBooster(ctx, session, catalog, request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.commitCurrentBooster(ctx, session, catalog, definition, request)
	if err != nil {
		t.Fatal(err)
	}
	after, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	want := lotteryRewardSlotDeltas(beforeSlots, after.Slots, 2600014)
	if len(want) < 2 {
		t.Fatalf("fixture did not split reward: after=%+v", after.Slots)
	}
	got := make(map[int16]uint32)
	var total uint32
	for _, delta := range result.RewardSlotDeltas {
		if delta.RewardItemID != 2600014 || delta.ListType != dnfrepo.MainInventoryListType || delta.Delta == 0 {
			t.Fatalf("unexpected reward delta=%+v", delta)
		}
		if _, duplicate := got[delta.SlotIndex]; duplicate {
			t.Fatalf("duplicate reward delta slot=%d entries=%+v", delta.SlotIndex, result.RewardSlotDeltas)
		}
		got[delta.SlotIndex] = delta.Delta
		total += delta.Delta
	}
	if total != 5 || len(got) != len(want) {
		t.Fatalf("recorded deltas=%+v total=%d want=%+v", got, total, want)
	}
	for slot, delta := range want {
		if got[slot] != uint32(delta) {
			t.Fatalf("slot=%d recorded=%d want actual delta=%d all=%+v", slot, got[slot], delta, got)
		}
	}
}

func TestLotteryOfficialAckUsesOneRealDeltaAndOp14StaysAbsolute(t *testing.T) {
	stackLimitSlots := func(extra map[string]dnfrepo.ItemStack) map[string]dnfrepo.ItemStack {
		slots := map[string]dnfrepo.ItemStack{"0:75": {ItemID: 900, Count: 1}}
		for key, stack := range extra {
			slots[key] = stack
		}
		return slots
	}
	cases := []struct {
		name        string
		rewardCount uint32
		slots       map[string]dnfrepo.ItemStack
		unchanged   string
	}{
		{
			name:        "lowest_same_id_full",
			rewardCount: 3,
			slots: stackLimitSlots(map[string]dnfrepo.ItemStack{
				"0:3": {ItemID: 2600014, Count: 2},
			}),
			unchanged: "0:3",
		},
		{
			name:        "different_expiration",
			rewardCount: 2,
			slots: stackLimitSlots(map[string]dnfrepo.ItemStack{
				"0:65": {
					ItemID:   2600014,
					Count:    1,
					ExpireAt: time.Now().UTC().Add(time.Hour),
					Extra: map[string]string{
						"item_kind":      string(dungeonDropItemStackable),
						"pvf_path":       "stackable/potion/ptn.stk",
						"stackable_type": "[material]",
					},
				},
			}),
			unchanged: "0:65",
		},
		{
			name:        "split_across_multiple_slots",
			rewardCount: 5,
			slots:       stackLimitSlots(nil),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repos := lotteryRewardSlotDeltaRepos(t, tc.slots)
			catalog := lotteryRewardSlotDeltaCatalog(t, tc.rewardCount)
			service := currentBoosterTestService(catalog, repos)
			service.options.currentClientVersion = "official-24.9"
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 19}
			if err := service.handleCurrentLotteryItem(session, []byte{0, 0, 75, 0}); err != nil {
				t.Fatal(err)
			}
			packets := drainUpperPackets(t, conn)
			if len(packets) < 2 || packets[0].msgID != 27 || len(packets[0].body) != 126 || packets[0].body[0] != 1 {
				t.Fatalf("packets=%+v", packets)
			}
			after, found, err := repos.Inventory.Load(context.Background(), "19")
			if err != nil || !found {
				t.Fatalf("load inventory found=%t err=%v", found, err)
			}
			actual := lotteryRewardSlotDeltas(tc.slots, after.Slots, 2600014)
			wantSlot, wantDelta := lotteryRewardSlotExpectedDisplay(actual)
			ack := packets[0].body
			if gotSlot, gotItem, gotDelta := int16(binary.LittleEndian.Uint16(ack[3:5])), binary.LittleEndian.Uint32(ack[5:9]), binary.LittleEndian.Uint32(ack[9:13]); gotSlot != wantSlot || gotItem != 2600014 || gotDelta != uint32(wantDelta) {
				t.Fatalf("ack slot/item/delta=%d/%d/%d want=%d/%d/%d actual=%+v", gotSlot, gotItem, gotDelta, wantSlot, 2600014, wantDelta, actual)
			}
			rows := make(map[int16][]byte)
			for _, packet := range packets[1:] {
				if packet.msgID != 14 || len(packet.body) < 3 || packet.body[0] != dnfrepo.MainInventoryListType {
					continue
				}
				count := int(binary.LittleEndian.Uint16(packet.body[1:3]))
				if len(packet.body) != 3+currentItemListEntryWireSize*count {
					t.Fatalf("op14 body=%x", packet.body)
				}
				for at := 3; at < len(packet.body); at += currentItemListEntryWireSize {
					row := packet.body[at : at+currentItemListEntryWireSize]
					rows[int16(binary.LittleEndian.Uint16(row[:2]))] = row
				}
			}
			for slot := range actual {
				row, present := rows[slot]
				if !present || binary.LittleEndian.Uint32(row[2:6]) != 2600014 || binary.LittleEndian.Uint32(row[6:10]) != uint32(after.Slots[fmt.Sprintf("0:%d", slot)].Count) {
					t.Fatalf("slot=%d op14=%x want absolute count=%d all=%+v", slot, row, after.Slots[fmt.Sprintf("0:%d", slot)].Count, rows)
				}
			}
			if tc.unchanged != "" {
				if after.Slots[tc.unchanged].Count != tc.slots[tc.unchanged].Count {
					t.Fatalf("unchanged slot %s changed: before=%+v after=%+v", tc.unchanged, tc.slots[tc.unchanged], after.Slots[tc.unchanged])
				}
				if _, present := rows[int16(mustParseLotterySlotForTest(t, tc.unchanged))]; present {
					t.Fatalf("unchanged slot %s received a fake op14 row", tc.unchanged)
				}
			}
		})
	}
}

func mustParseLotterySlotForTest(t *testing.T, key string) int16 {
	t.Helper()
	listType, slot, parsed := parseSceneInventorySlotKey(key)
	if !parsed || listType != dnfrepo.MainInventoryListType {
		t.Fatalf("invalid slot key %q", key)
	}
	return slot
}
