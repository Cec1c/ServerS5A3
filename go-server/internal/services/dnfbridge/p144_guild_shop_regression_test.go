package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestP144RealPVFGuildShopPurchaseContainer(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
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
	for _, tc := range []struct {
		name string
		id   uint32
		slot int16
	}{{"medal", 100380051, 0}, {"gem", 90002, 49}} {
		t.Run(tc.name, func(t *testing.T) {
			pricing, err := resolveCurrentNPCShopActorPricing(shop, items, tc.id, 383)
			if err != nil {
				t.Fatal(err)
			}
			container := currentPVFItemTargetContainer(pricing.Definition)
			if !pricing.MaterialExchange || container.ListType != 38 || container.SlotStart != tc.slot {
				t.Fatalf("unexpected real product: %+v container=%+v", pricing, container)
			}
			t.Logf("PVF product=%d path=%s material=%d x%d gold=%d", tc.id, pricing.Definition.PVFPath, pricing.NeedMaterialItem, pricing.NeedMaterialCount, pricing.ExchangeGold)
			for _, mode := range []string{"purchase", "full_page", "missing_material"} {
				t.Run(mode, func(t *testing.T) {
					ctx := context.Background()
					amount := pricing.NeedMaterialCount + 7
					if mode == "missing_material" {
						amount = pricing.NeedMaterialCount - 1
					}
					slots := map[string]dnfrepo.ItemStack{"0:121": {ItemID: int64(pricing.NeedMaterialItem), Count: amount}}
					if mode == "full_page" {
						for slot := container.SlotStart; slot <= container.SlotEnd; slot++ {
							slots[currentCeraShopInventorySlotKey(38, slot)] = dnfrepo.ItemStack{ItemID: int64(tc.id), Count: 1, Bind: true}
						}
					}
					repos := mustCurrentNPCShopRepositories(t, pricing.ExchangeGold+1000, slots)
					character, _, _ := repos.Character.Load(ctx, "19")
					character.Level = 90
					character.Job = "0"
					if err := repos.Character.Save(ctx, character); err != nil {
						t.Fatal(err)
					}
					if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{}}); err != nil {
						t.Fatal(err)
					}
					service := currentNPCShopTestService(repos, shop, items)
					service.initialEquipmentArchive = archive
					conn := &bufferConn{}
					session := &gameSession{conn: conn, connID: "p144", selectedCharacterID: 19}
					count := uint32(1)
					if pricing.Definition.Kind == dungeonDropItemEquipment {
						count = 0
					}
					if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketBuyItem), currentNPCShopBuyTestBody(tc.id, count, 0, 383)); err != nil {
						t.Fatal(err)
					}
					ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
					inv, _, err := repos.Inventory.Load(ctx, "19")
					if err != nil {
						t.Fatal(err)
					}
					char, _, _ := repos.Character.Load(ctx, "19")
					if mode != "purchase" {
						if ack.Body[0] != 0 || len(rest) != 0 || !reflect.DeepEqual(inv.Slots, slots) || char.Stats["gold"] != pricing.ExchangeGold+1000 {
							t.Fatalf("rejection mutated assets or succeeded: ack=%x inventory=%+v", ack.Body, inv.Slots)
						}
						return
					}
					stack, found := inv.Slots[currentCeraShopInventorySlotKey(38, tc.slot)]
					if ack.Body[0] != 1 || !found || stack.ItemID != int64(tc.id) || stack.Count != 1 {
						t.Fatalf("purchase must persist list38:%d, ack=%x slots=%+v", tc.slot, ack.Body, inv.Slots)
					}
					if inv.Slots["0:121"].Count != 7 || char.Stats["gold"] != 1000 {
						t.Fatalf("wrong cost: %+v gold=%d", inv.Slots["0:121"], char.Stats["gold"])
					}
					foundUpdate := false
					for len(rest) > 0 {
						packet, tail := splitGameServerUpperPacket(t, rest)
						rest = tail
						if packet.Header.MsgID == 14 && len(packet.Body) >= 3 {
							for off := 3; off+currentItemListEntryWireSize <= len(packet.Body); off += currentItemListEntryWireSize {
								if binary.LittleEndian.Uint32(packet.Body[off+2:off+6]) == tc.id {
									if packet.Body[0] != 38 {
										t.Fatalf("product leaked to wire list %d", packet.Body[0])
									}
									foundUpdate = true
								}
							}
						}
					}
					if !foundUpdate {
						t.Fatal("missing list38 product notification")
					}
					fresh := currentNPCShopTestService(repos, shop, items)
					fresh.initialEquipmentArchive = archive
					body, _, _, ok := fresh.buildCurrentItemListBodyForSession(ctx, session, 38)
					if !ok || len(body) < 3 || body[0] != 38 || binary.LittleEndian.Uint16(body[1:3]) != 1 || binary.LittleEndian.Uint32(body[5:9]) != tc.id {
						t.Fatalf("fresh login medal projection=%x ok=%v", body, ok)
					}
					if tc.name == "medal" {
						// Use the actual newly purchased instance through op19, without synthetic durability.
						instance := binary.LittleEndian.Uint32(stack.RawEntry[6:10])
						move := itemSlotFamilyGuardMoveBody(38, tc.slot, 3, 32)
						binary.LittleEndian.PutUint32(move[3:7], instance)
						move[26] = op19CurrentUIPlayerFlag
						conn.write.Reset()
						if err := fresh.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), move); err != nil {
							t.Fatal(err)
						}
						worn, _, _ := repos.Equipment.Load(ctx, "19")
						entry, present := worn.Entries["32"]
						if !present || entry.ItemID != int64(tc.id) {
							t.Fatalf("purchased medal op19 equip failed: %+v packets=%x", worn.Entries, conn.write.Bytes())
						}
						if binary.LittleEndian.Uint16(entry.RawEntry[11:13]) != 0 {
							t.Fatal("medal must retain real PVF zero durability")
						}
						projected, good := currentItemListEntryFromEquipment(entry)
						if !good || binary.LittleEndian.Uint16(projected.data[:2]) != 32 {
							t.Fatal("equipped login projection lost medal")
						}
						unequip := itemSlotFamilyGuardMoveBody(3, 32, 38, tc.slot)
						binary.LittleEndian.PutUint32(unequip[3:7], instance)
						unequip[26] = op19CurrentUIPlayerFlag
						if err := fresh.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), unequip); err != nil {
							t.Fatal(err)
						}
						inv, _, _ = repos.Inventory.Load(ctx, "19")
						worn, _, _ = repos.Equipment.Load(ctx, "19")
						if inv.Slots["38:0"].ItemID != int64(tc.id) || len(worn.Entries) != 0 {
							t.Fatalf("op19 unequip failed: slots=%+v worn=%+v", inv.Slots, worn.Entries)
						}
					}

				})
			}
		})
	}
}

// Synthetic gold-only shop exercises the alternate payment branch. The IDs and
// families are real; this is not a claim that NPC383 sells these goods for gold.
func TestP144GuildShopGoldContainer(t *testing.T) {
	source := bridgePVFSource{
		"monster/monster.lst":      "",
		"equipment/equipment.lst":  "100380051 `flag.equ`\n",
		"stackable/stackable.lst":  "90002 `gem.stk`\n",
		"equipment/flag.equ":       "[equipment type]\n`[flag]` 21\n[price]\n100\n[durability]\n0\n",
		"stackable/gem.stk":        "[stackable type]\n`[flag gem]`\n[price]\n100\n[stack limit]\n1\n",
		"itemshop/itemshop.lst":    "1 `gold.shp`\n",
		"itemshop/gold.shp":        "[NPC]\n383\n[item list]\n100380051 90002\n[/item list]\n",
		"equipment/pricetable.tbl": "[rate]\n200 150 30\n",
	}
	shop, err := newCurrentNPCShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{100380051, 90002} {
		def, err := items.ResolveItem(id)
		if err != nil {
			t.Fatal(err)
		}
		page := currentPVFItemTargetContainer(def)
		for _, full := range []bool{false, true} {
			slots := map[string]dnfrepo.ItemStack{}
			if full {
				for i := page.SlotStart; i <= page.SlotEnd; i++ {
					slots[currentCeraShopInventorySlotKey(38, i)] = dnfrepo.ItemStack{ItemID: int64(id), Count: 1, Bind: true}
				}
			}
			repos := mustCurrentNPCShopRepositories(t, 1000, slots)
			s := currentNPCShopTestService(repos, shop, items)
			result, err := s.commitCurrentNPCShopBuy(context.Background(), &gameSession{selectedCharacterID: 19}, shop, items, currentNPCShopBuyRequest{ItemID: id, Count: 1, ActorContext: 383})
			inv, _, _ := repos.Inventory.Load(context.Background(), "19")
			char, _, _ := repos.Character.Load(context.Background(), "19")
			if full {
				if err == nil || !reflect.DeepEqual(inv.Slots, slots) || char.Stats["gold"] != 1000 {
					t.Fatalf("full page changed assets: id=%d err=%v", id, err)
				}
				continue
			}
			if err != nil || char.Stats["gold"] != 900 || len(result.GuildMedalUpdates) != 1 || len(result.Updates) != 1 || inv.Slots[currentCeraShopInventorySlotKey(38, page.SlotStart)].ItemID != int64(id) {
				t.Fatalf("gold purchase id=%d result=%+v err=%v", id, result, err)
			}
		}
	}
}

// Read-only evidence, not a claim about the unobserved P144 client request.
// It identifies the dedicated PVF table independently of the generic resolver.
func TestP144RealPVFMedalUpgradeEvidence(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := archive.ReadText("etc/upgrade.etc")
	if err != nil {
		t.Fatal(err)
	}
	flagStart := strings.Index(raw, "`flag_upgrade_table`")
	if flagStart < 0 {
		t.Fatal("missing flag_upgrade_table")
	}
	end := strings.Index(raw[flagStart:], "[/table]")
	if end < 0 {
		t.Fatal("missing table end")
	}
	flagRaw := "[table]\n[table type]\n" + raw[flagStart:flagStart+end] + "[/table]"
	doc, err := dnfpvf.Parse("flag-table-evidence", flagRaw)
	if err != nil {
		t.Fatal(err)
	}
	flag := parseCurrentUpgradeTable(doc, flagRaw)
	row, ok := flag.RowForLevel(1)
	if !ok || row.MaterialItemID != 10154439 || row.MaterialCount != 1 {
		t.Fatalf("real flag first row=%+v", row)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	material, err := items.ResolveItem(uint32(row.MaterialItemID))
	if err != nil {
		t.Fatal(err)
	}
	shop, err := newCurrentNPCShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	s := currentNPCShopTestService(mustCurrentNPCShopRepositories(t, 0, nil), shop, items)
	s.initialEquipmentArchive = archive
	generic, err := s.alignedUpgradePolicyResolver()("reinforce", 100380051, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real flag +0->+1=%+v material_path=%s; current generic policy=%+v", row, material.PVFPath, generic)
	// Opt-in acceptance probe deliberately remains red until the dedicated
	// protocol/container/cost path is supported; default tests only read evidence.
	if os.Getenv("DNFBRIDGE_P144_VERIFY_UPGRADE_GAP") == "1" && (generic.MaterialItemID != row.MaterialItemID || generic.MaterialCount != row.MaterialCount) {
		t.Fatalf("unfixed dedicated flag policy: got material=%d x%d, PVF flag table=%d x%d", generic.MaterialItemID, generic.MaterialCount, row.MaterialItemID, row.MaterialCount)
	}
}
