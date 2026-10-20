package dnfbridge

import (
	"context"
	"encoding/binary"
	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestRealPVFExpertMachineEpicRewardsAndRequesterWarehouse(t *testing.T) {
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
	for _, self := range []bool{false, true} {
		t.Run(map[bool]string{false: "visitor", true: "self"}[self], func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			for _, id := range []string{"19", "20"} {
				if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: "account-" + id, Stats: map[string]int64{"gold": 1000, "expert_job_type": 3, "expert_job_exp": 10, currentExpertJobMachineGradeStat: 1, currentExpertJobMachineEnduranceStat: 300}}); err != nil {
					t.Fatal(err)
				}
				if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: id, Slots: map[string]dnfrepo.ItemStack{"0:10": {ItemID: 100300212, Count: 1}}}); err != nil {
					t.Fatal(err)
				}
				if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "account-" + id, Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
					t.Fatal(err)
				}
			}
			owner := uint16(20)
			if self {
				owner = 19
			}
			service := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }, expertJobCatalog: jobs, pvfItemCatalog: items}
			session := &gameSession{selectedCharacterID: 19, accountID: "account-19"}
			store := &currentExpertJobStore{OwnerCharacterID: owner, MachineGrade: 1, Endurance: 300, Cost: 100}
			result, err := service.commitCurrentExpertJobStoreDisjoint(ctx, session, store, currentExpertJobStoreDisjointRequest{OwnerID: owner, TargetList: 0, TargetSlot: 10})
			if err != nil {
				t.Fatal(err)
			}
			// This PVF explicitly permits [gain exp] 0..1 per disassembly.
			if result.Experience < 0 || result.Experience > 1 || result.Endurance >= 300 {
				t.Fatalf("machine state %+v", result)
			}
			ownerRecord, _, _ := repos.Character.Load(ctx, map[bool]string{false: "20", true: "19"}[self])
			if ownerRecord.Stats["expert_job_exp"] != 10+result.Experience {
				t.Fatalf("experience was not persisted: %+v result=%+v", ownerRecord.Stats, result)
			}
			inventory, _, _ := repos.Inventory.Load(ctx, "19")
			if _, ok := inventory.Slots["0:10"]; ok {
				t.Fatal("source not consumed")
			}
			account, _, _ := repos.AccountInventory.Load(ctx, "account-19")
			for _, item := range []int64{3037, 10158124} {
				slot := currentDisjointWarehouseFixedSlots[uint32(item)]
				stack := account.Slots[currentCeraShopInventorySlotKey(0, slot)]
				if stack.ItemID != item || stack.Count <= 0 {
					t.Fatalf("warehouse item %d slot %d: %+v rows=%+v warehouse=%+v", item, slot, stack, result.Materials, account.Slots)
				}
			}
			other, _, _ := repos.AccountInventory.Load(ctx, "account-20")
			if len(other.Slots) != 0 {
				t.Fatalf("owner warehouse changed: %+v", other)
			}
			for _, stack := range inventory.Slots {
				if stack.ItemID == 3037 || stack.ItemID == 10158124 {
					t.Fatalf("warehouse reward in backpack: %+v", stack)
				}
			}
			// Exercise the wire path on a second item. The persisted fee is
			// charged once, and only a visitor use emits the native income
			// notification; replaying the consumed slot must emit none.
			if inventory.Slots == nil {
				inventory.Slots = make(map[string]dnfrepo.ItemStack)
			}
			inventory.Slots["0:11"] = dnfrepo.ItemStack{ItemID: 100300212, Count: 1}
			if err := repos.Inventory.Save(ctx, inventory); err != nil {
				t.Fatal(err)
			}
			visitorConn, ownerConn := &bufferConn{}, &bufferConn{}
			session.conn = visitorConn
			ownerSession := &gameSession{conn: ownerConn, selectedCharacterID: owner, accountID: "account-20"}
			if self {
				ownerSession = session
			}
			store.OwnerSession, store.Endurance = ownerSession, result.Endurance
			service.onlinePlayers = newOnlinePlayerManager()
			service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 19, Session: session})
			service.expertJobStores = map[uint16]*currentExpertJobStore{owner: store}
			service.expertJobVisitors = map[*gameSession]uint16{session: owner}
			service.options.gameUpperHeader = gameUpperHeaderChannel13
			service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			body := []byte{byte(owner), 0, 11, 0, 0}
			if err := service.handleCurrentExpertJobStoreDisjoint(session, 235, body); err != nil {
				t.Fatal(err)
			}
			packets := splitAllGameServerUpperPackets(t, ownerConn.write.Bytes())
			incomePackets := 0
			for _, packet := range packets {
				if packet.Header.MsgID == currentExpertJobDisjointOwnerNotification {
					incomePackets++
					if packet.Header.Classification != 0 || len(packet.Body) != 9 || packet.Body[0] != 1 || binary.LittleEndian.Uint32(packet.Body[1:5]) != 1200 {
						t.Fatalf("native income=%x", packet.Body)
					}
				}
			}
			if (!self && incomePackets != 1) || (self && incomePackets != 0) {
				t.Fatalf("income notifications=%d self=%t", incomePackets, self)
			}
			requester, _, _ := repos.Character.Load(ctx, "19")
			wantGold := int64(800)
			if self {
				wantGold = 1000
			}
			if requester.Stats["gold"] != wantGold {
				t.Fatalf("fee wallet=%d want=%d", requester.Stats["gold"], wantGold)
			}
			ownerConn.write.Reset()
			if err := service.handleCurrentExpertJobStoreDisjoint(session, 235, body); err != nil {
				t.Fatal(err)
			}
			if ownerConn.write.Len() != 0 {
				t.Fatal("rejected replay advanced owner income")
			}
		})
	}
}
