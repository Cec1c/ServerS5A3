package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFEpicDisjointNPCAndPlayerLedgerAndWire(t *testing.T) {
	t.Setenv(currentDisjointEpicDisplayEnv, "paired-v1")
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	source, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	book, err := loadCurrentEpicPieceCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := dnfexpertjob.Load(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	// Resolve all actual mapped epic identities through the same Go path used
	// by both machines, including the runtime parser and placement rules.
	for _, piece := range book.pieces {
		output, _ := book.outputFor(piece)
		def, err := items.ResolveItem(output)
		if err != nil {
			t.Fatal(output, err)
		}
		doc, err := parseDungeonCardPVFDocument(source, def.PVFPath)
		if err != nil {
			t.Fatal(output, err)
		}
		actual, err := items.currentDisjointEpicPieceID(def, doc)
		if err != nil || actual != piece {
			t.Fatalf("equipment=%d piece=%d actual=%d err=%v", output, piece, actual, err)
		}
	}
	for _, mode := range []string{"npc-single", "npc-batch", "player-self", "player-visitor", "player-personal"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			for _, id := range []string{"19", "20"} {
				if err := g.Account.Save(ctx, r.AccountRecord{AccountID: "account-" + id, Metadata: map[string]string{}}); err != nil {
					t.Fatal(err)
				}
				if err := g.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: "account-" + id, Level: 90, Stats: map[string]int64{"gold": 1000000, "expert_job_type": 3, "expert_job_exp": 100000, currentExpertJobMachineGradeStat: 11, currentExpertJobMachineEnduranceStat: 300}}); err != nil {
					t.Fatal(err)
				}
				if err := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{}}); err != nil {
					t.Fatal(err)
				}
				if err := g.AccountInventory.Save(ctx, r.AccountInventoryRecord{AccountID: "account-" + id, Slots: map[string]r.ItemStack{}}); err != nil {
					t.Fatal(err)
				}
			}
			var output, piece uint32
			levels, err := currentAbyssEpicPieceLevelIndexFor(&pvfDungeonMonsterCatalog{source: source})
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range book.pieces {
				level, ok := levels.levelOf(id)
				if ok && level == 90 {
					piece = id
					output, _ = book.outputFor(id)
					break
				}
			}
			if piece == 0 {
				t.Fatal("no real Lv90 epic")
			}
			bag, _, _ := g.Inventory.Load(ctx, "19")
			bag.Slots = map[string]r.ItemStack{}
			bag.Slots["0:10"] = r.ItemStack{ItemID: int64(output), Count: 1}
			count := uint32(1)
			if mode == "npc-batch" {
				bag.Slots["0:11"] = r.ItemStack{ItemID: int64(output), Count: 1}
				count = 2
			}
			if err := g.Inventory.Save(ctx, bag); err != nil {
				t.Fatal(err)
			}
			conn := &bufferConn{}
			p := &gameSession{conn: conn, selectedCharacterID: 19, accountID: "account-19"}
			s := currentDisjointTestService(g, items)
			s.options.currentClientVersion = "official-24.9"
			s.options.epicPiecePush = epicPiecePushAlways
			if mode == "npc-single" {
				err = s.handleCurrentDisjointItem(p, currentDisjointItemTestBody(10, 0, -1, 317))
			} else if mode == "npc-batch" {
				err = s.handleCurrentDisjointItem(p, currentDisjointBatchBody([]int16{10, 11}, -1, 317))
			} else {
				ownerID := uint16(19)
				ownerSession := p
				if mode == "player-visitor" {
					ownerID = 20
					ownerSession = &gameSession{conn: &bufferConn{}, selectedCharacterID: 20, accountID: "account-20"}
				}
				s.expertJobCatalog = jobs
				s.onlinePlayers = newOnlinePlayerManager()
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 19, Session: p})
				store := &currentExpertJobStore{Kind: currentExpertJobDisjointStoreKind, OwnerCharacterID: ownerID, OwnerSession: ownerSession, MachineGrade: 11, Endurance: 300, Cost: 100}
				s.expertJobStores = map[uint16]*currentExpertJobStore{ownerID: store}
				s.expertJobVisitors = map[*gameSession]uint16{p: ownerID}
				if mode == "player-personal" {
					s.expertJobStores = nil
					s.expertJobVisitors = nil
				}
				err = s.handleCurrentExpertJobStoreDisjoint(p, 235, []byte{byte(ownerID), 0, 10, 0, 0})
			}
			if err != nil {
				t.Fatal(err)
			}
			a, _, err := g.Account.Load(ctx, "account-19")
			if err != nil {
				t.Fatal(err)
			}
			balance := r.ParseAccountEpicPieceBalance(a.Metadata[r.AccountEpicPieceMetadataKey(piece)])
			if balance != 30*count {
				t.Fatalf("real equipment=%d piece=%d ledger=%d want=%d wire=%x", output, piece, balance, 30*count, conn.write.Bytes())
			}
			shown := uint32(0)
			displayed := uint32(0)
			for _, pkt := range splitAllGameServerUpperPackets(t, conn.write.Bytes()) {
				if pkt.Header.MsgID == currentEpicPieceBalanceSyncOpcode && len(pkt.Body) == 13 && binary.LittleEndian.Uint32(pkt.Body[5:]) == piece {
					shown = binary.LittleEndian.Uint32(pkt.Body[9:])
				}
				if (pkt.Header.MsgID == 26 || pkt.Header.MsgID == 235) && len(pkt.Body) >= 7 && pkt.Body[0] == 1 {
					for i := 0; i < int(pkt.Body[6]); i++ {
						at := 7 + 10*i
						if at+10 > len(pkt.Body) {
							t.Fatal("short result row")
						}
						if binary.LittleEndian.Uint16(pkt.Body[at:]) == 65535 {
							if binary.LittleEndian.Uint32(pkt.Body[at+2:]) != piece {
								t.Fatal("wrong display piece")
							}
							displayed += binary.LittleEndian.Uint32(pkt.Body[at+6:])
						}
					}
				}
			}
			if shown != balance {
				t.Fatal("real ledger missing from native update", shown, balance)
			}
			if displayed != count*30 {
				t.Fatalf("result UI rows=%d want=%d", displayed, count*30)
			}
			after, _, _ := g.Inventory.Load(ctx, "19")
			for _, item := range after.Slots {
				if item.ItemID == int64(piece) {
					t.Fatal("piece display entered bag")
				}
			}
			for i := uint32(0); i < count; i++ {
				if after.Slots[fmt.Sprintf("0:%d", 10+i)].ItemID != 0 {
					t.Fatal("source not consumed")
				}
			}
			owner, _, _ := g.Account.Load(ctx, "account-20")
			if len(r.AccountEpicPieceBalances(owner)) != 0 {
				t.Fatal("visitor credited machine owner")
			}
		})
	}
}
