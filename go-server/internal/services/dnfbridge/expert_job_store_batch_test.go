package dnfbridge

import (
	"context"
	"encoding/binary"
	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"testing"
)

func playerDisjointBatchBody(owner uint16, slots ...int16) []byte {
	body := []byte{byte(owner), byte(owner >> 8), 255, 255, 0, 'B', '2', '3', 1, byte(len(slots))}
	for _, slot := range slots {
		body = binary.LittleEndian.AppendUint16(body, uint16(slot))
	}
	return body
}

func TestPlayerDisjointBatchSelectionValidation(t *testing.T) {
	good := playerDisjointBatchBody(20, 10, 11)
	requests, err := parseCurrentExpertJobStoreDisjointBatch(good)
	if err != nil || len(requests) != 2 || requests[1].TargetSlot != 11 || requests[1].OwnerID != 20 {
		t.Fatalf("selection=%+v err=%v", requests, err)
	}
	badMarker := append([]byte(nil), good...)
	badMarker[8] = 2
	badList := append([]byte(nil), good...)
	badList[4] = 1
	for _, bad := range [][]byte{good[:len(good)-1], append(good, 0), badMarker, badList, playerDisjointBatchBody(0, 10, 11), playerDisjointBatchBody(20, 10, 10), playerDisjointBatchBody(20, -1, 11), playerDisjointBatchBody(20)} {
		if _, err := parseCurrentExpertJobStoreDisjointBatch(bad); err == nil {
			t.Fatalf("accepted invalid selection %x", bad)
		}
	}
	if requests, err := parseCurrentExpertJobStoreDisjointBatch([]byte{20, 0, 10, 0, 0}); err != nil || len(requests) != 1 {
		t.Fatalf("legacy: %+v %v", requests, err)
	}
}

func TestRealPVFPlayerDisjointBatchAtomicResult(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	source, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := dnfexpertjob.Load(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"visitor", "self", "personal", "missing-second", "locked-second", "total-fee"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			for _, id := range []string{"19", "20"} {
				gold := int64(1000)
				if scenario == "total-fee" && id == "19" {
					gold = 150
				}
				if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: "account-" + id, Stats: map[string]int64{"gold": gold, "expert_job_type": 3, "expert_job_exp": 10, currentExpertJobMachineGradeStat: 1, currentExpertJobMachineEnduranceStat: 300}}); err != nil {
					t.Fatal(err)
				}
				slots := map[string]dnfrepo.ItemStack{}
				if id == "19" {
					slots["0:10"] = dnfrepo.ItemStack{ItemID: 100300212, Count: 1}
					slots["0:11"] = dnfrepo.ItemStack{ItemID: 100300212, Count: 1}
				}
				if scenario == "missing-second" {
					delete(slots, "0:11")
				}
				if scenario == "locked-second" && id == "19" {
					stack := slots["0:11"]
					stack.Count = 2
					slots["0:11"] = stack
				}
				if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: id, Slots: slots}); err != nil {
					t.Fatal(err)
				}
				if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "account-" + id, Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
					t.Fatal(err)
				}
			}
			beforeRequester, _, _ := repos.Character.Load(ctx, "19")
			beforeOwner, _, _ := repos.Character.Load(ctx, "20")
			beforeInventory, _, _ := repos.Inventory.Load(ctx, "19")
			beforeWarehouse, _, _ := repos.AccountInventory.Load(ctx, "account-19")
			self := scenario == "self" || scenario == "personal"
			owner := uint16(20)
			if self {
				owner = 19
			}
			requesterConn, ownerConn := &bufferConn{}, &bufferConn{}
			session := &gameSession{conn: requesterConn, selectedCharacterID: 19, accountID: "account-19"}
			ownerSession := &gameSession{conn: ownerConn, selectedCharacterID: owner, accountID: "account-20"}
			if self {
				ownerSession = session
			}
			store := &currentExpertJobStore{Kind: currentExpertJobDisjointStoreKind, OwnerCharacterID: owner, OwnerSession: ownerSession, MachineGrade: 1, Endurance: 300, Cost: 100}
			service := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }, expertJobCatalog: jobs, pvfItemCatalog: items, onlinePlayers: newOnlinePlayerManager(), expertJobStores: map[uint16]*currentExpertJobStore{owner: store}, expertJobVisitors: map[*gameSession]uint16{session: owner}}
			service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 19, Session: session})
			service.options.gameUpperHeader = gameUpperHeaderChannel13
			service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			if scenario == "personal" {
				service.options.currentClientVersion = "official-24.9"
				service.expertJobStores = nil
				service.expertJobVisitors = nil
			}
			body := playerDisjointBatchBody(owner, 10, 11)
			if err := service.handleCurrentExpertJobStoreDisjoint(session, 235, body); err != nil {
				t.Fatal(err)
			}
			requester, _, _ := repos.Character.Load(ctx, "19")
			ownerRecord, _, _ := repos.Character.Load(ctx, "20")
			inventory, _, _ := repos.Inventory.Load(ctx, "19")
			warehouse, _, _ := repos.AccountInventory.Load(ctx, "account-19")
			packets := splitAllGameServerUpperPackets(t, requesterConn.write.Bytes())
			resultPackets := 0
			for _, packet := range packets {
				if packet.Header.MsgID == 235 {
					resultPackets++
					if scenario != "visitor" && !self {
						if len(packet.Body) < 1 || packet.Body[0] != 0 {
							t.Fatalf("failure=%x", packet.Body)
						}
					} else {
						if packet.Body[0] != 1 {
							t.Fatalf("result=%x", packet.Body)
						}
						rows := int(packet.Body[6])
						if len(packet.Body) != 7+10*rows+8 {
							t.Fatalf("result cursor len=%d rows=%d", len(packet.Body), rows)
						}
						seen := map[uint32]bool{}
						for i := 0; i < rows; i++ {
							row := packet.Body[7+10*i : 17+10*i]
							item := binary.LittleEndian.Uint32(row[2:6])
							slot := int16(binary.LittleEndian.Uint16(row))
							count := binary.LittleEndian.Uint32(row[6:10])
							if seen[item] {
								t.Fatalf("duplicate accumulated item %d", item)
							}
							seen[item] = true
							stack := warehouse.Slots[currentCeraShopInventorySlotKey(0, slot)]
							if stack.ItemID == int64(item) && stack.Count != int64(count) {
								t.Fatalf("combined reward %d: wire=%d warehouse=%d", item, count, stack.Count)
							}
						}
					}
				}
			}
			if resultPackets != 1 {
				t.Fatalf("result popups=%d", resultPackets)
			}
			if scenario == "personal" {
				clears := 0
				for _, packet := range packets {
					if packet.Header.MsgID != 14 || len(packet.Body) != 3+2*currentItemListEntryWireSize || packet.Body[0] != 0 || binary.LittleEndian.Uint16(packet.Body[1:3]) != 2 {
						continue
					}
					for i, slot := range []uint16{10, 11} {
						row := packet.Body[3+i*currentItemListEntryWireSize:]
						if binary.LittleEndian.Uint16(row[:2]) != slot || binary.LittleEndian.Uint32(row[2:6]) != ^uint32(0) || binary.LittleEndian.Uint32(row[6:10]) != 0 {
							t.Fatalf("batch clear row=%x", row[:10])
						}
					}
					clears++
				}
				if clears != 1 {
					t.Fatalf("expected one combined consumed-slot refresh, got %d", clears)
				}
			}
			if scenario != "visitor" && !self {
				if !reflect.DeepEqual(beforeRequester, requester) || !reflect.DeepEqual(beforeOwner, ownerRecord) || !reflect.DeepEqual(beforeInventory, inventory) || !reflect.DeepEqual(beforeWarehouse, warehouse) || store.Endurance != 300 || ownerConn.write.Len() != 0 {
					t.Fatal("late failure partially committed assets or income")
				}
				return
			}
			if _, ok := inventory.Slots["0:10"]; ok {
				t.Fatal("first source remains")
			}
			if _, ok := inventory.Slots["0:11"]; ok {
				t.Fatal("second source remains")
			}
			wantGold := int64(800)
			if self {
				wantGold = 1000
			}
			if requester.Stats["gold"] != wantGold {
				t.Fatalf("gold=%d", requester.Stats["gold"])
			}
			actualOwner := ownerRecord
			if self {
				actualOwner = requester
			}
			if actualOwner.Stats["expert_job_exp"] < 10 || actualOwner.Stats["expert_job_exp"] > 12 || actualOwner.Stats[currentExpertJobMachineEnduranceStat] >= 300 {
				t.Fatalf("machine state=%+v", actualOwner.Stats)
			}
			if !self && ownerRecord.Stats["gold"] != 1200 {
				t.Fatalf("owner gold=%d", ownerRecord.Stats["gold"])
			}
			requesterConn.write.Reset()
			ownerConn.write.Reset()
			if err := service.handleCurrentExpertJobStoreDisjoint(session, 235, body); err != nil {
				t.Fatal(err)
			}
			afterReplay, _, _ := repos.Character.Load(ctx, "19")
			afterWarehouse, _, _ := repos.AccountInventory.Load(ctx, "account-19")
			if !reflect.DeepEqual(afterReplay, requester) || !reflect.DeepEqual(afterWarehouse, warehouse) || (!self && ownerConn.write.Len() != 0) {
				t.Fatal("replay changed settled assets")
			}
		})
	}
}
