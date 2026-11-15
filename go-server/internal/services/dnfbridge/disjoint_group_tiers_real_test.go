package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFGroupDisjointerTiers(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	source, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	jobs, err := dnfexpertjob.Load(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	config, _ := jobs.Disjointer()
	for _, sample := range []struct {
		item int64
		base int64 // Approved level-7 result; the input PVF tables are unchanged.
	}{
		{101000723, 89}, {101000722, 153}, {101000721, 10},
		{101000724, 294}, {101000720, 364}, {500072, 9}, {101000718, 288},
	} {
		def, err := items.ResolveItem(uint32(sample.item))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := parseDungeonCardPVFDocument(source, def.PVFPath)
		if err != nil {
			t.Fatal(err)
		}
		piece, err := items.currentDisjointEpicPieceID(def, doc)
		if err != nil {
			t.Fatal(err)
		}
		for grade := 7; grade <= 11; grade++ {
			want := sample.base * int64(100+(grade-7)*5) / 100
			for _, self := range []bool{false, true} {
				for _, batch := range []bool{false, true} {
					t.Run(fmt.Sprintf("item%d/grade%d/self%t/batch%t", sample.item, grade, self, batch), func(t *testing.T) {
						g := memory.NewMemoryGroup()
						repair, ok := config.RepairRule(grade)
						if !ok {
							t.Fatal("missing repair grade")
						}
						for _, id := range []string{"19", "20"} {
							if err := g.Account.Save(ctx, r.AccountRecord{AccountID: "account-" + id, Metadata: map[string]string{}}); err != nil {
								t.Fatal(err)
							}
							if err := g.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: "account-" + id, Level: 90, Stats: map[string]int64{"gold": 10000, "expert_job_type": 3, "expert_job_exp": 1200, currentExpertJobMachineGradeStat: int64(grade), currentExpertJobMachineEnduranceStat: repair.MaxEndurance}}); err != nil {
								t.Fatal(err)
							}
							bag := map[string]r.ItemStack{}
							if id == "19" {
								bag["0:10"] = r.ItemStack{ItemID: sample.item, Count: 1}
								bag["0:11"] = r.ItemStack{ItemID: sample.item, Count: 1}
							}
							if err := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: bag}); err != nil {
								t.Fatal(err)
							}
							if err := g.AccountInventory.Save(ctx, r.AccountInventoryRecord{AccountID: "account-" + id, Slots: map[string]r.ItemStack{}}); err != nil {
								t.Fatal(err)
							}
						}
						owner := uint16(20)
						if self {
							owner = 19
						}
						session := &gameSession{selectedCharacterID: 19, accountID: "account-19"}
						store := &currentExpertJobStore{OwnerCharacterID: owner, OwnerSession: &gameSession{selectedCharacterID: owner, accountID: fmt.Sprintf("account-%d", owner)}, MachineGrade: grade, Endurance: repair.MaxEndurance, Cost: 100}
						service := &Service{repositoryProvider: func() (r.Group, bool) { return g, true }, expertJobCatalog: jobs, pvfItemCatalog: items}
						requests := []currentExpertJobStoreDisjointRequest{{OwnerID: owner, TargetSlot: 10}, {OwnerID: owner, TargetSlot: 11}}
						if batch {
							_, err = service.commitCurrentExpertJobStoreDisjointBatch(ctx, session, store, requests)
							if err != nil {
								t.Fatal(err)
							}
						} else {
							for _, req := range requests {
								result, err := service.commitCurrentExpertJobStoreDisjoint(ctx, session, store, req)
								if err != nil {
									t.Fatal(err)
								}
								store.Endurance = result.Endurance
							}
						}
						warehouse, _, _ := g.AccountInventory.Load(ctx, "account-19")
						cube := warehouse.Slots[currentCeraShopInventorySlotKey(0, currentDisjointWarehouseFixedSlots[3037])]
						if cube.ItemID != 3037 || cube.Count != 2*want {
							t.Fatalf("two items clear cubes = %+v, want %d", cube, 2*want)
						}
						other, _, _ := g.AccountInventory.Load(ctx, "account-20")
						if len(other.Slots) != 0 {
							t.Fatal("rewards credited to machine owner")
						}
						bag, _, _ := g.Inventory.Load(ctx, "19")
						if bag.Slots["0:10"].ItemID != 0 || bag.Slots["0:11"].ItemID != 0 {
							t.Fatal("source not consumed")
						}
						requester, _, _ := g.Character.Load(ctx, "19")
						ownerRecord, _, _ := g.Character.Load(ctx, fmt.Sprint(owner))
						wantGold := int64(9800)
						if self {
							wantGold = 10000
						} else if ownerRecord.Stats["gold"] != 10200 {
							t.Fatalf("owner fee = %d", ownerRecord.Stats["gold"])
						}
						if requester.Stats["gold"] != wantGold {
							t.Fatalf("requester gold = %d, want %d", requester.Stats["gold"], wantGold)
						}
						loss := repair.MaxEndurance - ownerRecord.Stats[currentExpertJobMachineEnduranceStat]
						if loss < 2 || loss > 6 || ownerRecord.Stats["expert_job_exp"] < 1200 || ownerRecord.Stats["expert_job_exp"] > 1202 {
							t.Fatalf("machine state changed: %+v", ownerRecord.Stats)
						}
						if piece != 0 {
							account, _, _ := g.Account.Load(ctx, "account-19")
							if got := r.ParseAccountEpicPieceBalance(account.Metadata[r.AccountEpicPieceMetadataKey(piece)]); got != 60 {
								t.Fatalf("two epics returned %d pieces, want 60", got)
							}
						}
					})
				}
			}
		}
	}
}
