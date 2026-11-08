package dnfbridge

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	dnfdisjoint "longheng.io/server/internal/modules/dnf/disjoint"
	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestRealPVFAikenSupportTitleNPCDisjointRejected(t *testing.T) {
	catalog := loadRealAikenDisjointCatalog(t)
	config, err := catalog.currentDisjointPVFConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{currentAikenSupportTitleOwnedID, currentAikenSupportTitleCombatID} {
		for _, mode := range []string{"single", "compact-batch", "native-batch"} {
			t.Run(fmt.Sprintf("%d/%s", id, mode), func(t *testing.T) {
				ctx := context.Background()
				repos, _ := mustCurrentDisjointTestAssets(t, map[string]r.ItemStack{"0:10": {ItemID: id, Count: 1}, "0:11": {ItemID: id, Count: 1}})
				service := currentDisjointTestService(repos, catalog)
				conn := &bufferConn{}
				p := &gameSession{conn: conn, selectedCharacterID: 19}
				before, _, _ := repos.Inventory.Load(ctx, "19")
				account, _, _ := repos.Account.Load(ctx, "account-1")
				warehouse, _, _ := repos.AccountInventory.Load(ctx, "account-1")
				character, _, _ := repos.Character.Load(ctx, "19")
				if mode == "native-batch" {
					owner, e := dnfdisjoint.NewOwner(repos)
					if e != nil {
						t.Fatal(e)
					}
					outcomes, e := commitNativeNPCDisjointRequests(ctx, owner, "account-1", "19", catalog, config, []currentDisjointItemRequest{{SourceSlot: 10, ToolSlot: -1}, {SourceSlot: 11, ToolSlot: -1}})
					if e != nil || len(outcomes) != 2 {
						t.Fatalf("batch: %v", e)
					}
					for _, outcome := range outcomes {
						if outcome.err == nil {
							t.Fatal("forbidden title produced a native success")
						}
					}
				} else {
					body := currentDisjointItemTestBody(10, 0, -1, 0)
					if mode == "compact-batch" {
						body = currentDisjointBatchBody([]int16{10, 11}, -1, 0)
					}
					if e := service.handleCurrentDisjointItem(p, body); e != nil {
						t.Fatal(e)
					}
					ack, tail := splitGameServerUpperPacket(t, conn.write.Bytes())
					if len(tail) != 0 || len(ack.Body) < 2 || ack.Body[0] != 0 {
						t.Fatalf("title disjoint ACK=%x", ack.Body)
					}
				}
				after, _, _ := repos.Inventory.Load(ctx, "19")
				afterAccount, _, _ := repos.Account.Load(ctx, "account-1")
				afterWarehouse, _, _ := repos.AccountInventory.Load(ctx, "account-1")
				afterCharacter, _, _ := repos.Character.Load(ctx, "19")
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(account, afterAccount) || !reflect.DeepEqual(warehouse, afterWarehouse) || !reflect.DeepEqual(character, afterCharacter) {
					t.Fatal("rejected title changed assets")
				}
			})
		}
	}
}

func TestRealPVFAikenSupportTitlePlayerMachineRejected(t *testing.T) {
	items := loadRealAikenDisjointCatalog(t)
	jobs, err := dnfexpertjob.Load(context.Background(), items.source)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{currentAikenSupportTitleOwnedID, currentAikenSupportTitleCombatID} {
		for _, mode := range []string{"visitor-single", "visitor-batch", "personal-single", "personal-batch"} {
			t.Run(fmt.Sprintf("%d/%s", id, mode), func(t *testing.T) {
				ctx := context.Background()
				// Put an ordinary item first in batch requests: rejection of the
				// second item must roll back earlier fees, rewards and machine wear.
				repos, _ := mustCurrentDisjointTestAssets(t, map[string]r.ItemStack{"0:10": {ItemID: 100300212, Count: 1}, "0:11": {ItemID: id, Count: 1}})
				for _, charID := range []string{"19", "20"} {
					c := r.CharacterRecord{CharacterID: charID, AccountID: "account-1", Stats: map[string]int64{"gold": 1000, "expert_job_type": 3, "expert_job_exp": 10, currentExpertJobMachineGradeStat: 1, currentExpertJobMachineEnduranceStat: 300}}
					if e := repos.Character.Save(ctx, c); e != nil {
						t.Fatal(e)
					}
				}
				if e := repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "20", Slots: map[string]r.ItemStack{}}); e != nil {
					t.Fatal(e)
				}
				conn, ownerConn := &bufferConn{}, &bufferConn{}
				p := &gameSession{conn: conn, selectedCharacterID: 19, accountID: "account-1"}
				owner := uint16(20)
				if mode == "personal-single" || mode == "personal-batch" {
					owner = 19
				}
				op := &gameSession{conn: ownerConn, selectedCharacterID: owner, accountID: "account-1"}
				if owner == 19 {
					op = p
				}
				store := &currentExpertJobStore{Kind: currentExpertJobDisjointStoreKind, OwnerCharacterID: owner, OwnerSession: op, MachineGrade: 1, Endurance: 300, Cost: 100}
				s := currentDisjointTestService(repos, items)
				s.expertJobCatalog = jobs
				s.onlinePlayers = newOnlinePlayerManager()
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 19, Session: p})
				s.options.currentClientVersion = "official-24.9"
				if owner != 19 {
					s.expertJobStores = map[uint16]*currentExpertJobStore{owner: store}
					s.expertJobVisitors = map[*gameSession]uint16{p: owner}
				}
				before, _, _ := repos.Inventory.Load(ctx, "19")
				c1, _, _ := repos.Character.Load(ctx, "19")
				c2, _, _ := repos.Character.Load(ctx, "20")
				account, _, _ := repos.Account.Load(ctx, "account-1")
				warehouse, _, _ := repos.AccountInventory.Load(ctx, "account-1")
				body := []byte{byte(owner), byte(owner >> 8), 11, 0, 0}
				if mode == "visitor-batch" || mode == "personal-batch" {
					body = playerDisjointBatchBody(owner, 10, 11)
				}
				if e := s.handleCurrentExpertJobStoreDisjoint(p, 235, body); e != nil {
					t.Fatal(e)
				}
				ack, tail := splitGameServerUpperPacket(t, conn.write.Bytes())
				if len(tail) != 0 || len(ack.Body) < 2 || ack.Body[0] != 0 {
					t.Fatalf("machine ACK=%x", ack.Body)
				}
				after, _, _ := repos.Inventory.Load(ctx, "19")
				a1, _, _ := repos.Character.Load(ctx, "19")
				a2, _, _ := repos.Character.Load(ctx, "20")
				a, _, _ := repos.Account.Load(ctx, "account-1")
				w, _, _ := repos.AccountInventory.Load(ctx, "account-1")
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(c1, a1) || !reflect.DeepEqual(c2, a2) || !reflect.DeepEqual(account, a) || !reflect.DeepEqual(warehouse, w) || store.Endurance != 300 || ownerConn.write.Len() != 0 {
					t.Fatal("rejected title changed assets or notified owner")
				}
			})
		}
	}
}
