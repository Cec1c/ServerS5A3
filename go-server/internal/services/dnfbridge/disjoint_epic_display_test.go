package dnfbridge

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestEpicDisjointResultDisplayUsesCommittedDeltaWithoutInventory(t *testing.T) {
	for _, mode := range []string{"", "unknown", "paired-v1"} {
		for _, batch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/batch=%t", mode, batch), func(t *testing.T) {
				t.Setenv(currentDisjointEpicDisplayEnv, mode)
				slots := map[string]r.ItemStack{"0:60": {ItemID: 5002, Count: 1}}
				body := currentDisjointItemTestBody(60, 0, -1, 317)
				gains := map[uint32]uint32{10300000: 30}
				if batch {
					slots["0:61"] = r.ItemStack{ItemID: 5002, Count: 1}
					slots["0:62"] = r.ItemStack{ItemID: 5003, Count: 1}
					body = currentDisjointBatchBody([]int16{60, 61, 62}, -1, 317)
					gains = map[uint32]uint32{10300000: 60, 10300001: 30}
				}
				repos := aikenDisjointTestRepositories(t, slots)
				seedEpicDisjointBalance(t, repos, 100)
				s := currentDisjointTestService(repos, epicDisjointCatalog(t))
				conn := &bufferConn{}
				p := &gameSession{conn: conn, selectedCharacterID: 19}
				if err := s.handleCurrentDisjointItem(p, body); err != nil {
					t.Fatal(err)
				}
				shown := map[uint32]uint32{}
				for _, pkt := range splitAllGameServerUpperPackets(t, conn.write.Bytes()) {
					if pkt.Header.MsgID != 26 || len(pkt.Body) == 0 || pkt.Body[0] != 1 {
						continue
					}
					for _, row := range parseCurrentDisjointRewardRows(t, pkt.Body) {
						if row.Slot == -1 {
							shown[row.ItemID] += row.Granted
						}
					}
				}
				if mode != "paired-v1" {
					gains = map[uint32]uint32{}
				}
				if !reflect.DeepEqual(shown, gains) {
					t.Fatalf("shown=%v want=%v", shown, gains)
				}
				account, _, _ := repos.Account.Load(context.Background(), "account-1")
				want := uint32(130)
				if batch {
					want = 160
				}
				if r.ParseAccountEpicPieceBalance(account.Metadata[r.AccountEpicPieceMetadataKey(10300000)]) != want {
					t.Fatal("display changed ledger")
				}
				bag, _, _ := repos.Inventory.Load(context.Background(), "19")
				warehouse, _, _ := repos.AccountInventory.Load(context.Background(), "account-1")
				for _, rows := range []map[string]r.ItemStack{bag.Slots, warehouse.Slots} {
					for _, item := range rows {
						if item.ItemID == 10300000 || item.ItemID == 10300001 {
							t.Fatal("display entered physical inventory")
						}
					}
				}
				conn.write.Reset()
				if err := s.handleCurrentDisjointItem(p, body); err != nil {
					t.Fatal(err)
				}
				after, _, _ := repos.Account.Load(context.Background(), "account-1")
				if !reflect.DeepEqual(account, after) {
					t.Fatal("replay credited pieces")
				}
			})
		}
	}
}

func TestEpicDisjointDisplayRespectsLedgerCapAndKeepsPhysicalRowsSeparate(t *testing.T) {
	t.Setenv(currentDisjointEpicDisplayEnv, "paired-v1")
	repos := aikenDisjointTestRepositories(t, map[string]r.ItemStack{"0:60": {ItemID: 5002, Count: 1}})
	seedEpicDisjointBalance(t, repos, r.AccountEpicPieceBalanceMax-10)
	s := currentDisjointTestService(repos, epicDisjointCatalog(t))
	result, err := s.commitCurrentDisjoint(context.Background(), &gameSession{selectedCharacterID: 19}, s.pvfItemCatalog, 60, 0, 317,
		currentEquipmentDisjointResolver(func() currentDisjointPVFConfig {
			c, err := s.pvfItemCatalog.currentDisjointPVFConfig()
			if err != nil {
				t.Fatal(err)
			}
			return c
		}()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.EpicPieceDisplay, []currentDisjointRewardSlot{{Slot: -1, ItemID: 10300000, Granted: 10}}) {
		t.Fatal(result.EpicPieceDisplay)
	}
	for _, row := range result.Rewards {
		if row.Slot < 0 || row.ItemID == 10300000 {
			t.Fatal("physical rows contain display")
		}
	}
	if got := currentDisjointBatchSlotRefreshes(currentDisjointBatchResult{Items: []currentDisjointBatchItem{{Slot: 60, Rewards: result.Rewards, EpicPieceDisplay: result.EpicPieceDisplay}}}); len(got) == 0 {
		t.Fatal("missing normal refresh")
	} else {
		for _, row := range got {
			if row.SlotIndex < 0 {
				t.Fatal("sentinel entered inventory refresh")
			}
		}
	}
}
