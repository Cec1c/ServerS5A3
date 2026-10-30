package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestFastLotteryTwentyOneOpensKeepIncrementalInventory(t *testing.T) {
	// Even an old general-box refresh preference must not invalidate the
	// official client's cached source object during its automatic loop.
	t.Setenv(itemGrantNoticeEnv, "containers")
	for _, paid := range []bool{false, true} {
		t.Run(fmt.Sprint(paid), func(t *testing.T) {
			ctx := context.Background()
			_, source := newMagicBoxTestCatalog(t)
			source["stackable/stackable.lst"] += "900 `fast.stk`\n"
			fee := "[need material] 10007367 2\n"
			if paid {
				fee = "[lottery use cost] 100\n[lottery use need item] 10007367 2\n"
			}
			source["stackable/fast.stk"] = "[stackable type] `[upgradable legacy]` 1\n[stack limit] 100\n" + fee + "[int data] 2600014 100 1\n[/int data]\n"
			catalog, err := newPVFDungeonDropCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			repos := memory.NewMemoryGroup()
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Stats: map[string]int64{"gold": 2100, "fatigue": 120}}); err != nil {
				t.Fatal(err)
			}
			initial := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:75": {ItemID: 900, Count: 21}, "0:109": {ItemID: 10007367, Count: 42}}}
			if err := repos.Inventory.Save(ctx, initial); err != nil {
				t.Fatal(err)
			}
			s := currentBoosterTestService(catalog, repos)
			s.options.currentClientVersion = "official-24.9"
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 19}
			for opened := 1; opened <= 21; opened++ {
				before, _, err := repos.Inventory.Load(ctx, "19")
				if err != nil {
					t.Fatal(err)
				}
				if err := s.handleCurrentLotteryItem(session, []byte{0, 0, 75, 0}); err != nil {
					t.Fatal(err)
				}
				packets := drainUpperPackets(t, conn)
				if len(packets) < 2 || packets[0].msgID != 27 || len(packets[0].body) != 126 || packets[0].body[0] != 1 {
					t.Fatalf("open=%d packets=%+v", opened, packets)
				}
				if got := binary.LittleEndian.Uint32(packets[0].body[9:13]); got != 1 {
					t.Fatalf("open=%d native ADD/display quantity=%d, want this open's grant 1", opened, got)
				}
				rows := map[uint16][]byte{}
				for i, p := range packets {
					if dir := os.Getenv("DNF_FAST_JAR_PACKET_DIR"); dir != "" {
						if err := os.MkdirAll(dir, 0755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("paid-%t-open-%02d-%02d-op%d.bin", paid, opened, i, p.msgID)), p.body, 0600); err != nil {
							t.Fatal(err)
						}
					}
					if p.msgID == 13 {
						t.Fatalf("open %d destroys cached jar with full op13", opened)
					}
					if p.msgID != 14 {
						continue
					}
					if len(p.body) < 3 || p.body[0] != 0 {
						t.Fatalf("unexpected delta %x", p.body)
					}
					count := int(binary.LittleEndian.Uint16(p.body[1:3]))
					if len(p.body) != 3+119*count {
						t.Fatalf("delta length %d", len(p.body))
					}
					for at := 3; at < len(p.body); at += 119 {
						row := p.body[at : at+119]
						rows[binary.LittleEndian.Uint16(row[:2])] = row
					}
				}
				after, _, _ := repos.Inventory.Load(ctx, "19")
				character, _, _ := repos.Character.Load(ctx, "19")
				wantGold := int64(2100)
				if paid {
					wantGold -= int64(opened * 100)
				}
				if character.Stats["gold"] != wantGold || character.Stats["fatigue"] != 120 {
					t.Fatal(character.Stats)
				}
				if paid {
					row, ok := rows[0]
					if !ok || binary.LittleEndian.Uint32(row[2:6]) != 0 || binary.LittleEndian.Uint32(row[6:10]) != uint32(wantGold) {
						t.Fatalf("wallet open=%d row=%x", opened, row)
					}
				}
				for _, slot := range []uint16{75, 109} {
					stack, ok := after.Slots[fmt.Sprintf("0:%d", slot)]
					wantID, wantCount := uint32(0xffffffff), uint32(0)
					if ok {
						wantID, wantCount = uint32(stack.ItemID), uint32(stack.Count)
					}
					row, present := rows[slot]
					if !present || binary.LittleEndian.Uint32(row[2:6]) != wantID || binary.LittleEndian.Uint32(row[6:10]) != wantCount {
						t.Fatalf("open=%d slot=%d row=%x want=%d/%d", opened, slot, row, wantID, wantCount)
					}
				}
				var total int64
				for key, stack := range after.Slots {
					if stack.ItemID != 2600014 {
						continue
					}
					total += stack.Count
					// A later expiration second can create a new stack. Only changed
					// reward slots need a delta; unchanged earlier stacks stay cached.
					if reflect.DeepEqual(before.Slots[key], stack) {
						continue
					}
					_, slot, _ := parseSceneInventorySlotKey(key)
					row := rows[uint16(slot)]
					if len(row) != 119 || binary.LittleEndian.Uint32(row[6:10]) != uint32(stack.Count) {
						t.Fatalf("reward delta missing: %s=%+v", key, stack)
					}
				}
				if total != int64(opened) {
					t.Fatalf("rewards=%d want=%d", total, opened)
				}
				if err := s.handleCurrentLotteryOverflowConfirm(session, []byte{1, 27, 0}); err != nil {
					t.Fatal(err)
				}
				replayed, _, _ := repos.Inventory.Load(ctx, "19")
				if conn.write.Len() != 0 || !reflect.DeepEqual(after, replayed) {
					t.Fatal("old confirmation replayed settlement")
				}
			}
		})
	}
}

func TestFastLotteryFailedSettlementEmitsOnlyFailure(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint(late), func(t *testing.T) {
			ctx := context.Background()
			gold := int64(99)
			if late {
				gold = 1000
			}
			repos := lotteryPriceAssets(t, gold)
			if late {
				repos.AccountAssets = lotteryFailLateAssets{repos.AccountAssets}
			}
			s := currentBoosterTestService(lotteryPriceFixture(t, "100"), repos)
			s.options.currentClientVersion = "official-24.9"
			before, _, _ := repos.Inventory.Load(ctx, "19")
			conn := &bufferConn{}
			if err := s.handleCurrentLotteryItem(&gameSession{conn: conn, selectedCharacterID: 19}, []byte{0, 0, 75, 0}); err != nil {
				t.Fatal(err)
			}
			packets := drainUpperPackets(t, conn)
			after, _, _ := repos.Inventory.Load(ctx, "19")
			character, _, _ := repos.Character.Load(ctx, "19")
			if len(packets) != 1 || packets[0].msgID != 27 || !reflect.DeepEqual(packets[0].body, []byte{0, 0x13}) || !reflect.DeepEqual(before, after) || character.Stats["gold"] != gold {
				t.Fatal("failed settlement changed assets or emitted inventory success")
			}
		})
	}
}

func TestFastLotteryLastJarRewardReusesSourceSlot(t *testing.T) {
	ctx := context.Background()
	repos := lotteryPriceAssets(t, 100)
	inv, _, _ := repos.Inventory.Load(ctx, "19")
	for slot := 3; slot <= 248; slot++ {
		inv.Slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: 2002, Count: 1}
	}
	inv.Slots["0:75"] = dnfrepo.ItemStack{ItemID: 900, Count: 1}
	if err := repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	s := currentBoosterTestService(lotteryPriceFixture(t, "100"), repos)
	s.options.currentClientVersion = "official-24.9"
	conn := &bufferConn{}
	if err := s.handleCurrentLotteryItem(&gameSession{conn: conn, selectedCharacterID: 19}, []byte{0, 0, 75, 0}); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, conn)
	after, _, _ := repos.Inventory.Load(ctx, "19")
	if after.Slots["0:75"].ItemID != 2600014 || after.Slots["0:75"].Count != 1 {
		t.Fatalf("source slot reuse=%+v", after.Slots["0:75"])
	}
	if len(packets) != 3 || packets[0].msgID != 27 || len(packets[0].body) != 126 || binary.LittleEndian.Uint16(packets[0].body[3:5]) != 75 {
		t.Fatalf("last reward ACK=%+v", packets)
	}
	// Native CMD27 consumes/removes the old jar before reading its reward row.
	// A later explicit empty source row would delete the newly awarded object.
	row := packets[2].body[3:]
	if packets[2].msgID != 14 || len(row) != 119 || binary.LittleEndian.Uint16(row[:2]) != 75 || binary.LittleEndian.Uint32(row[2:6]) != 2600014 {
		t.Fatalf("reused slot delta=%x", row)
	}
}
