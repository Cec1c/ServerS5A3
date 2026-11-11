package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/inventory"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Exercises the real preview/nonce/use handler, transaction owner and wire
// projection together. Repositories contain isolated fictional characters only.
func TestPrivateGrowthNativeConsumeWire(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires paired candidate PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	var wires []map[string]any
	for testAttr := byte(0); testAttr <= 4; testAttr++ {
		id := int64(990033101)
		for _, level := range []byte{1, 12, 31} {
			t.Run(fmt.Sprintf("attr=%d/%d", testAttr, level), func(t *testing.T) {
				g := memory.NewMemoryGroup()
				ctx := context.Background()
				ticket := repo.ItemStack{ItemID: id, Count: 1, RawEntry: make([]byte, 119), Extra: map[string]string{"item_kind": "equipment"}}
				if err := inventory.InitializeGrowthTicket(&ticket, 7); err != nil {
					t.Fatal(err)
				}
				if attr := testAttr; attr != 0 {
					ticket.RawEntry[0x13] = attr
					ticket.RawEntry[0x14] = 7
					for _, key := range []string{"amplify_type", "amplification_type", "byte_13", "value_13", "value_c"} {
						ticket.Extra[key] = strconv.Itoa(int(attr))
					}
					for _, key := range []string{"amplify_value", "amplification_value", "marker_16", "marker16", "value_d"} {
						ticket.Extra[key] = "7"
					}
				}
				ticket.RawEntry[0xa] = level
				for _, key := range []string{"ext_data0", "packed_flag_byte", "reinforce", "upgrade_level"} {
					ticket.Extra[key] = strconv.Itoa(int(level))
				}
				target := repo.ItemStack{ItemID: 101040493, Count: 1, RawEntry: make([]byte, 119), Extra: map[string]string{"item_kind": "equipment", "custom_keep": "kept"}}
				target.RawEntry[0x57] = 6
				before := repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:9": ticket, "0:10": target}}
				if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "account-1", Level: 90}); err != nil {
					t.Fatal(err)
				}
				if err := g.Inventory.Save(ctx, before); err != nil {
					t.Fatal(err)
				}
				s := equipmentRecipeCompoundTestService(t, emancipationFixtureSource(), g)
				s.pvfItemCatalog = catalog
				s.initialEquipmentArchive = a
				conn := &bufferConn{}
				p := &gameSession{conn: conn, selectedCharacterID: 77, characterGeneration: 1}
				s.gameSessions = map[uint16]*gameSession{77: p}
				identity, ok := s.boundGameSessionCharacterSnapshot(p)
				if !ok {
					t.Fatal("unbound fixture")
				}
				if err := s.openPrivateGrowth(ctx, p, identity, 9, ticket, before); err != nil {
					t.Fatal(err)
				}
				preview, tail := splitGameServerUpperPacket(t, conn.write.Bytes())
				if preview.Header.MsgID != privateGrowthOpcode || binary.LittleEndian.Uint16(preview.Body[6:]) != 0 || binary.LittleEndian.Uint16(preview.Body[10:]) != 1 || len(tail) != 0 {
					t.Fatalf("preview invalid: %x", preview.Body[:48])
				}
				_, wantAttribute, _ := inventory.GrowthTicketMode(ticket)
				if preview.Body[39] != wantAttribute {
					t.Fatalf("preview attribute=%d want %d", preview.Body[39], wantAttribute)
				}
				q := make([]byte, 32)
				copy(q, "GTQ1")
				binary.LittleEndian.PutUint16(q[4:], 1)
				binary.LittleEndian.PutUint16(q[6:], 1)
				binary.LittleEndian.PutUint16(q[8:], 10)
				copy(q[16:], preview.Body[16:32])
				conn.write.Reset()
				if err := s.handlePrivateGrowth(p, 1, q); err != nil {
					t.Fatal(err)
				}
				update, tail := splitGameServerUpperPacket(t, conn.write.Bytes())
				result, tail := splitGameServerUpperPacket(t, tail)
				if update.Header.MsgID != 14 || len(update.Body) != 3+2*119 || update.Body[0] != 0 || binary.LittleEndian.Uint16(update.Body[1:]) != 2 || binary.LittleEndian.Uint16(update.Body[3:]) != 9 || binary.LittleEndian.Uint32(update.Body[5:]) != math.MaxUint32 {
					t.Fatalf("native source RemoveItem absent: %x", update.Body[:15])
				}
				if result.Header.MsgID != privateGrowthOpcode || binary.LittleEndian.Uint16(result.Body[6:]) != 3 || len(tail) != 0 {
					t.Fatal("success result missing")
				}
				after, _, err := g.Inventory.Load(ctx, "77")
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := after.Slots["0:9"]; exists {
					t.Fatal("consumed ticket persisted")
				}
				out := after.Slots["0:10"]
				_, attr, _ := inventory.GrowthTicketMode(ticket)
				row := update.Body[3+119:]
				if out.ItemID != target.ItemID || inventory.GrowthTicketLevel(out) != level || out.Extra["custom_keep"] != "kept" || out.RawEntry[0x57] != 6 || binary.LittleEndian.Uint16(row) != 10 || binary.LittleEndian.Uint32(row[2:]) != uint32(target.ItemID) || row[0xa]&31 != level || byte(currentItemListEntryFromStack(0, 10, out).data[0x13]) != attr {
					t.Fatalf("target projection mismatch: %x", row)
				}
				saved := repo.CloneInventory(after)
				conn.write.Reset()
				if err := s.handlePrivateGrowth(p, 1, q); err != nil {
					t.Fatal(err)
				}
				replay, _, _ := g.Inventory.Load(ctx, "77")
				if conn.write.Len() != 0 || !reflect.DeepEqual(saved, replay) {
					t.Fatal("confirmation replay mutated assets")
				}
				// A fresh item can occupy the consumed slot immediately.
				after.Slots["0:9"] = repo.ItemStack{ItemID: 3037, Count: 50}
				if err := g.Inventory.Save(ctx, after); err != nil {
					t.Fatal(err)
				}
				fresh, _, _ := g.Inventory.Load(ctx, "77")
				if fresh.Slots["0:9"].ItemID != 3037 {
					t.Fatal("consumed slot not reusable")
				}
				wires = append(wires, map[string]any{"ticket": id, "level": level, "attribute": attr, "bodyHex": hex.EncodeToString(update.Body)})
			})
		}
	}
	if dest := os.Getenv("DNFBRIDGE_GROWTH_WIRE_OUT"); dest != "" {
		b, err := json.MarshalIndent(wires, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dest, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
