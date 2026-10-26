package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type medalNPCAbortAssets struct {
	repo.CharacterAssetUnitOfWork
	applied *bool
}

func (u medalNPCAbortAssets) WithinCharacterAssets(ctx context.Context, id string, f func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, func(c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		if err := f(c, i, e); err != nil {
			return err
		}
		*u.applied = true
		return errors.New("medal NPC injected commit failure")
	})
}

// The native guild NPC mode24 uses op80's list38 sender (025fe330),
// with the actual material index in the main bag, not a material-page offset.
// This is an offline handler replay, not a capture of the user's click.
func TestGuildMedalNPCRealMaterialRoute(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set explicit real PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	shop, err := newCurrentNPCShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	npc, err := archive.ReadText("npc/upgrade_machine_guild_agit.npc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(npc, "[upgrade item]") || !strings.Contains(npc, "[upgrade item cost free]") {
		t.Fatal("real guild NPC role missing")
	}
	material, err := items.ResolveItem(10154439)
	if err != nil {
		t.Fatal(err)
	}
	if material.StackableType != "[quest]" || material.SlotStart != 177 || material.SlotEnd != 232 {
		t.Fatalf("real material family/range changed: %+v", material)
	}
	pricing, err := resolveCurrentNPCShopActorPricing(shop, items, 100380051, 383)
	if err != nil {
		t.Fatal(err)
	}
	if pricing.NeedMaterialItem != 10154439 || pricing.NeedMaterialCount != 3000 {
		t.Fatalf("real purchase cost changed: %+v", pricing)
	}
	t.Logf("PVF=%s material=%d path=%s family=%s range=%d..%d", archive.Snapshot().Checksum, material.ItemID, material.PVFPath, material.StackableType, material.SlotStart, material.SlotEnd)
	for _, name := range []string{"exact_cost", "remaining_five", "insufficient", "wrong_family_slot", "wrong_material", "last_quest_slot", "outside_quest_slot", "late_commit_failure"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			inv := repo.InventoryRecord{Slots: map[string]repo.ItemStack{}}
			have := uint32(3018)
			if name == "insufficient" {
				have = 3017
			}
			if name == "remaining_five" {
				have = 3023
			}
			// Normal item-definition grant chooses quest slots; no forced material-page fixture.
			if _, err := grantCurrentCeraShopProductAt(&inv, material, have, time.Now()); err != nil {
				t.Fatal(err)
			}
			repos := mustCurrentNPCShopRepositories(t, 0, inv.Slots)
			s := currentNPCShopTestService(repos, shop, items)
			s.initialEquipmentArchive = archive
			conn := &bufferConn{}
			session := &gameSession{conn: conn, connID: "guild-medal-npc", selectedCharacterID: 19}
			if err := s.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketBuyItem), currentNPCShopBuyTestBody(100380051, 0, 0, 383)); err != nil {
				t.Fatal(err)
			}
			buy, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			if buy.Body[0] != 1 {
				t.Fatalf("real medal purchase failed %x", buy.Body)
			}
			bought, _, err := repos.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			materialSlot := int16(-1)
			for slot := material.SlotStart; slot <= material.SlotEnd; slot++ {
				if st := bought.Slots[currentCeraShopInventorySlotKey(0, slot)]; st.ItemID == 10154439 && st.Count > 0 {
					if materialSlot >= 0 {
						t.Fatal("unexpected split remainder")
					}
					materialSlot = slot
				}
			}
			if materialSlot < 177 {
				t.Fatalf("certificate not in actual quest range: %d", materialSlot)
			}
			key := currentCeraShopInventorySlotKey(0, materialSlot)
			st := bought.Slots[key]
			switch name {
			case "wrong_family_slot", "last_quest_slot", "outside_quest_slot":
				delete(bought.Slots, key)
				materialSlot = 121
				if name == "last_quest_slot" {
					materialSlot = 232
				}
				if name == "outside_quest_slot" {
					materialSlot = 233
				}
				key = currentCeraShopInventorySlotKey(0, materialSlot)
				if len(st.RawEntry) >= 2 {
					binary.LittleEndian.PutUint16(st.RawEntry, uint16(materialSlot))
				}
				bought.Slots[key] = st
			case "wrong_material":
				st.ItemID = 3037
				bought.Slots[key] = st
			}
			if err := repos.Inventory.Save(ctx, bought); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Inventory.Load(ctx, "19")
			beforeChar, _, _ := repos.Character.Load(ctx, "19")
			applied := false
			if name == "late_commit_failure" {
				repos.CharacterAssets = medalNPCAbortAssets{repos.CharacterAssets, &applied}
				s = currentNPCShopTestService(repos, shop, items)
				s.initialEquipmentArchive = archive
			}
			body := p144FlagUpgradeBody()
			binary.LittleEndian.PutUint16(body[8:10], uint16(materialSlot))
			conn.write.Reset()
			if err := s.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketUpgradeItem), body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			after, _, _ := repos.Inventory.Load(ctx, "19")
			afterChar, _, _ := repos.Character.Load(ctx, "19")
			success := name == "exact_cost" || name == "remaining_five" || name == "last_quest_slot"
			if !success {
				if len(ack.Body) < 2 || ack.Body[0] != 0 || len(rest) != 0 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeChar, afterChar) {
					t.Fatalf("rejection/rollback changed assets: ack=%x", ack.Body)
				}
				if name == "late_commit_failure" && !applied {
					t.Fatal("did not reach transaction apply")
				}
				return
			}
			if len(ack.Body) != 25 || ack.Body[0] != 1 || ack.Body[13] != 1 || ack.Body[14] != 38 {
				t.Fatalf("legal quest material slot=%d rejected: ack=%x", materialSlot, ack.Body)
			}
			want := before.Slots[key].Count - 18
			if after.Slots[key].Count != want || after.Slots["38:0"].RawEntry[10]&31 != 1 || !reflect.DeepEqual(beforeChar, afterChar) {
				t.Fatal("wrong charge/level/wallet")
			}
			if binary.LittleEndian.Uint16(ack.Body[2:4]) != uint16(materialSlot) || binary.LittleEndian.Uint32(ack.Body[4:8]) != uint32(want) {
				t.Fatalf("material ACK mismatch: %x", ack.Body)
			}
			lists := map[byte]bool{}
			for len(rest) > 0 {
				p, tail := splitGameServerUpperPacket(t, rest)
				rest = tail
				if p.Header.MsgID == 14 {
					lists[p.Body[0]] = true
				}
			}
			if !lists[0] || !lists[38] {
				t.Fatalf("missing split notifications %v", lists)
			}
			fresh := currentNPCShopTestService(repos, shop, items)
			fresh.initialEquipmentArchive = archive
			cold := &gameSession{selectedCharacterID: 19}
			projection, _, _, ok := fresh.buildCurrentItemListBodyForSession(ctx, cold, 38)
			if !ok || len(projection) < 16 || projection[13]&31 != 1 {
				t.Fatalf("cold medal projection=%x", projection)
			}
		})
	}
}
