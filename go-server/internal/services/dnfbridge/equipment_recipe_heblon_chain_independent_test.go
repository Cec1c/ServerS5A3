package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func independentHeblonSlot(t *testing.T, inv repo.InventoryRecord, id int64) int16 {
	t.Helper()
	slot := int16(-1)
	for key, stack := range inv.Slots {
		if stack.ItemID == id && stack.Count > 0 {
			if !strings.HasPrefix(key, "0:") || slot >= 0 {
				t.Fatalf("ambiguous source %d at %s", id, key)
			}
			n, err := strconv.Atoi(strings.TrimPrefix(key, "0:"))
			if err != nil {
				t.Fatal(err)
			}
			slot = int16(n)
		}
	}
	if slot < 0 {
		t.Fatalf("missing source item %d", id)
	}
	return slot
}

// Each branch buys its own box and chooses exactly one drawing. No recipe is
// seeded, injected between handlers, or recovered from the other branches.
func TestR137IndependentHeblonPurchaseSelectCraftColdReplay(t *testing.T) {
	a, recipes, items := heblonResources(t)
	shop, err := newCurrentNPCShopCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []int64{10304471, 10304472, 10304473} {
		for _, balance := range []int64{950, 951} {
			t.Run(fmt.Sprintf("drawing%d_balance%d", id, balance), func(t *testing.T) {
				entry, found, err := recipes.Recipe(id)
				if err != nil || !found {
					t.Fatalf("recipe: %v", err)
				}
				repos, _, p := heblonFixture(t, a, items, entry, balance)
				// Fixture supplies equipment and two batches of inputs. Remove its seeded
				// drawing before any request so the whole chain must establish ownership.
				inv, _, err := repos.Inventory.Load(ctx, "19")
				if err != nil {
					t.Fatal(err)
				}
				delete(inv.Slots, "0:69")
				if err := repos.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
				before, _, _ := repos.Inventory.Load(ctx, "19")
				beforeAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
				beforeChar, _, _ := repos.Character.Load(ctx, "19")
				s := currentNPCShopTestService(repos, shop, items)
				s.initialEquipmentArchive = a
				p.connID = fmt.Sprintf("independent-%d-%d", id, balance)
				call := func(svc *Service, session *gameSession, op uint16, body []byte, success bool) []byte {
					t.Helper()
					conn := session.conn.(*bufferConn)
					conn.write.Reset()
					if err := svc.handleGameCommand(session, byte(dnfenum.GameCmdCommand), op, body); err != nil {
						t.Fatal(err)
					}
					ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
					if ack.Header.MsgID != op || ack.Header.Classification != 1 || len(ack.Body) == 0 || (ack.Body[0] == 1) != success {
						t.Fatalf("op%d success=%t ack=%+v body=%x", op, success, ack.Header, ack.Body)
					}
					return append([]byte(nil), conn.write.Bytes()...)
				}
				load := func() (repo.InventoryRecord, repo.AccountInventoryRecord, repo.CharacterRecord) {
					t.Helper()
					i, f, e := repos.Inventory.Load(ctx, "19")
					if e != nil || !f {
						t.Fatalf("inventory load %v", e)
					}
					ac, f, e := repos.AccountInventory.Load(ctx, "account-1")
					if e != nil || !f {
						t.Fatalf("account load %v", e)
					}
					ch, f, e := repos.Character.Load(ctx, "19")
					if e != nil || !f {
						t.Fatalf("character load %v", e)
					}
					return i, ac, ch
				}
				total := func(i repo.InventoryRecord, ac repo.AccountInventoryRecord, item int64) int64 {
					n := equipmentRecipeCompoundTestTotal(i, item)
					for _, st := range ac.Slots {
						if st.ItemID == item {
							n += st.Count
						}
					}
					return n
				}
				call(s, p, uint16(dnfenum.CmdPacketBuyItem), currentNPCShopBuyTestBody(10304511, 1, 244, 353), true)
				purchased, pa, pc := load()
				if total(purchased, pa, 10159142) != balance-950 || total(purchased, pa, 10304511) != 1 || pc.Stats["gold"] != beforeChar.Stats["gold"] {
					t.Fatal("buy did not charge exactly950 once and grant one box")
				}
				for _, m := range entry.Materials {
					if total(purchased, pa, m.ItemID) != total(before, beforeAccount, m.ItemID) {
						t.Fatalf("buy consumed input%d", m.ItemID)
					}
				}
				boxSlot := independentHeblonSlot(t, purchased, 10304511)
				selection := make([]byte, currentBoosterSelectionBodySize)
				binary.LittleEndian.PutUint16(selection, uint16(boxSlot))
				binary.LittleEndian.PutUint32(selection[4:], uint32(id))
				call(s, p, uint16(dnfenum.CmdPacketUseBoosterItem), selection, true)
				selected, sa, sc := load()
				if total(selected, sa, 10304511) != 0 || total(selected, sa, id) != 1 || total(selected, sa, 10159142) != balance-950 || sc.Stats["gold"] != beforeChar.Stats["gold"] {
					t.Fatal("selection cost or grant mismatch")
				}
				for _, other := range []int64{10304471, 10304472, 10304473} {
					if other != id && total(selected, sa, other) != 0 {
						t.Fatal("unselected drawing granted")
					}
				}
				for _, m := range entry.Materials {
					if total(selected, sa, m.ItemID) != total(before, beforeAccount, m.ItemID) {
						t.Fatalf("selection consumed input%d", m.ItemID)
					}
				}
				drawingSlot := independentHeblonSlot(t, selected, id)
				request := append(equipmentRecipeCompoundTestSlotRequest(uint32(drawingSlot), 1), 0, 1)
				wire := call(s, p, 25, request, true)
				final, fa, fc := load()
				if total(final, fa, 10159142) != balance-950 || total(final, fa, id) != 0 || total(final, fa, entry.Outputs[0].ItemID) != 1 || total(final, fa, 10304511) != 0 || fc.Stats["gold"] != beforeChar.Stats["gold"] {
					t.Fatal("craft double-priced, lost drawing, or duplicated output")
				}
				for _, m := range entry.Materials {
					if got, want := total(final, fa, m.ItemID), total(before, beforeAccount, m.ItemID)-m.Count; got != want {
						t.Fatalf("input%d got%d want%d", m.ItemID, got, want)
					}
				}
				want := append([]byte(nil), before.Slots["0:9"].RawEntry...)
				binary.LittleEndian.PutUint32(want[2:], uint32(entry.Outputs[0].ItemID))
				output, e := items.ResolveItem(uint32(entry.Outputs[0].ItemID))
				if e != nil {
					t.Fatal(e)
				}
				want[0x0d] = 0
				if normalizeDungeonDropStackableType(output.AttachType) == "sealing" {
					want[0x0d] = 1
				}
				if !bytes.Equal(final.Slots["0:9"].RawEntry, want) || final.Slots["0:9"].Extra["instance_note"] != "preserve" {
					t.Fatal("persistent instance bytes/metadata lost")
				}
				checkWire := func(wire []byte, full bool) {
					t.Helper()
					seen := 0
					if !full {
						_, rest := splitGameServerUpperPacket(t, wire) // ordinary ACK13
						wire = rest
					}
					for len(wire) > 0 {
						packet, rest := splitGameServerUpperPacketWithHeader(t, wire, dnfproto.GameServerUpperHeaderSize16)
						wire = rest
						if packet.Header.Classification != 0 || len(packet.Body) < 5 || packet.Body[0] != 0 {
							continue
						}
						off := 3
						if full {
							if packet.Header.MsgID != 13 {
								continue
							}
							off = 5
						} else if packet.Header.MsgID == 13 {
							off = 5
						} else if packet.Header.MsgID != 14 {
							continue
						}
						for ; off+currentItemListEntryWireSize <= len(packet.Body); off += currentItemListEntryWireSize {
							row := packet.Body[off : off+currentItemListEntryWireSize]
							if binary.LittleEndian.Uint16(row) == 9 && binary.LittleEndian.Uint32(row[2:]) == uint32(entry.Outputs[0].ItemID) {
								seen++
								if !bytes.Equal(row, want) {
									t.Fatalf("wire instance lost full=%t got%x want%x", full, row, want)
								}
							}
						}
					}
					if seen == 0 {
						t.Fatalf("no equipment projection full=%t", full)
					}
				}
				checkWire(wire, false)
				unchanged := func(label string) {
					t.Helper()
					i, ac, ch := load()
					if !reflect.DeepEqual(i, final) || !reflect.DeepEqual(ac, fa) || !reflect.DeepEqual(ch, fc) {
						t.Fatalf("%s mutated committed assets", label)
					}
				}
				call(s, p, 25, request, false)
				unchanged("same-session op25 replay")
				call(s, p, uint16(dnfenum.CmdPacketUseBoosterItem), selection, false)
				unchanged("same-session box selection replay")
				// Fresh service/catalog and session: share only the PVF resource and repo.
				coldItems, e := newPVFDungeonDropCatalog(a)
				if e != nil {
					t.Fatal(e)
				}
				coldShop, e := newCurrentNPCShopCatalog(a)
				if e != nil {
					t.Fatal(e)
				}
				cold := currentNPCShopTestService(repos, coldShop, coldItems)
				cold.initialEquipmentArchive = a
				cp := &gameSession{conn: &bufferConn{}, connID: "independent-cold", selectedCharacterID: 19, accountID: "account-1"}
				if err := cold.sendSelectedCurrentItemLists(cp, "independent_cold"); err != nil {
					t.Fatal(err)
				}
				checkWire(cp.conn.(*bufferConn).write.Bytes(), true)
				// List construction may normalize unrelated wallet metadata. Establish the
				// durable post-projection snapshot before checking rejected replays.
				coldInv, coldAccount, coldChar := load()
				if !bytes.Equal(coldInv.Slots["0:9"].RawEntry, want) || total(coldInv, coldAccount, 10159142) != balance-950 {
					t.Fatal("cold projection mutated output or purchase balance")
				}
				for _, m := range entry.Materials {
					if total(coldInv, coldAccount, m.ItemID) != total(final, fa, m.ItemID) {
						t.Fatal("cold projection changed materials")
					}
				}
				call(cold, cp, 25, request, false)
				call(cold, cp, uint16(dnfenum.CmdPacketUseBoosterItem), selection, false)
				after, aa, cc := load()
				if !reflect.DeepEqual(after, coldInv) || !reflect.DeepEqual(aa, coldAccount) || !reflect.DeepEqual(cc, coldChar) {
					t.Fatal("cold-session replay changed assets")
				}
				t.Logf("box=%d selected=%d input=%d output=%d price=%d->%d; same-session/cold replay rejected, instance119 preserved", 10304511, id, entry.Materials[0].ItemID, entry.Outputs[0].ItemID, balance, balance-950)
			})
		}
	}
}
