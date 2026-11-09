package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
	pvf "longheng.io/server/internal/platform/pvf"
)

func TestPlayerUtilityInventoryRefreshRejectsWornRowsBeforeSending(t *testing.T) {
	s, p, _, _ := utilityFixture(t, map[string]repo.ItemStack{"0:9": {ItemID: 700, Count: 1}})
	for _, rows := range [][]alignedcmd.ItemSlotRefresh{
		{{ListType: 3, SlotIndex: 14}},
		{{ListType: 0, SlotIndex: 9}, {ListType: 3, SlotIndex: 14}},
	} {
		p.conn.(*bufferConn).write.Reset()
		if err := s.sendSelectedIncrementalItemSlotRefreshes(p, "worn-row-regression", rows); err == nil || p.conn.(*bufferConn).write.Len() != 0 {
			t.Fatal("inventory projection must reject worn rows without sending an empty sentinel", err)
		}
	}
}

func TestPlayerUtilityRealRepairProjectsWornRowsWithoutRemovingEquipment(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	source, err := pvf.LoadArchive(pvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"worn-only", "worn-and-quickbar", "insufficient", "stale"} {
		t.Run(mode, func(t *testing.T) {
			s, p, g, id := utilityFixture(t, map[string]repo.ItemStack{"0:9": {ItemID: 10018, Count: 1}})
			s.options.pvfPath, s.initialEquipmentArchive = path, source
			s.pvfItemCatalog, err = newPVFDungeonDropCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			c, _, _ := g.Character.Load(ctx, "19")
			c.Stats["gold"] = 10000000
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			eq := repo.EquipmentRecord{CharacterID: "19", Entries: map[string]repo.EquipmentEntry{}}
			for _, target := range []struct {
				slot int16
				item int64
			}{{12, 10018}, {14, 100070550}} {
				raw := make([]byte, 0x77)
				binary.LittleEndian.PutUint16(raw[:2], uint16(target.slot))
				binary.LittleEndian.PutUint32(raw[2:6], uint32(target.item))
				binary.LittleEndian.PutUint32(raw[6:10], 314159)
				raw[0x0a] = 12
				binary.LittleEndian.PutUint16(raw[0x0b:0x0d], 1)
				binary.LittleEndian.PutUint32(raw[0x0e:0x12], 490702716)
				raw[0x2b], raw[0x36] = 37, 9
				key := strconv.Itoa(int(target.slot))
				eq.Entries[key] = repo.EquipmentEntry{SlotIndex: target.slot, ItemID: target.item, Bind: true, RawEntry: raw, Extra: map[string]string{"current_exe_equipment_type": key, "durability": "1", "packed_flag_byte": "12", "upgrade_level": "12", "value_a": "490702716"}}
			}
			if err := g.Equipment.Save(ctx, eq); err != nil {
				t.Fatal(err)
			}
			if mode == "worn-and-quickbar" {
				inv, _, _ := g.Inventory.Load(ctx, "19")
				inv.Slots["0:4"] = repo.ItemStack{ItemID: 10018, Count: 1, Extra: map[string]string{"durability": "1"}}
				if err := g.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
			}
			v := utilityPreview(t, s, id, "repair", 0, 0)
			wantCount := 2
			if mode == "worn-and-quickbar" {
				wantCount++
			}
			if v.Count != wantCount || v.Gold >= 0 {
				t.Fatal(v)
			}
			unchanged, _, _ := g.Equipment.Load(ctx, "19")
			if !reflect.DeepEqual(unchanged, eq) {
				t.Fatal("preview changed worn equipment")
			}
			if mode == "insufficient" {
				c.Stats["gold"] = 0
				_ = g.Character.Save(ctx, c)
			}
			if mode == "stale" {
				e := eq.Entries["12"]
				e.Extra["durability"] = "2"
				eq.Entries["12"] = e
				_ = g.Equipment.Save(ctx, eq)
			}
			beforeEq, _, _ := g.Equipment.Load(ctx, "19")
			beforeInv, _, _ := g.Inventory.Load(ctx, "19")
			beforeChar, _, _ := g.Character.Load(ctx, "19")
			request := utilityCommit(id, v)
			out, commitErr := s.applyHelperUtility(ctx, id, request)
			afterEq, _, _ := g.Equipment.Load(ctx, "19")
			afterInv, _, _ := g.Inventory.Load(ctx, "19")
			afterChar, _, _ := g.Character.Load(ctx, "19")
			conn := p.conn.(*bufferConn)
			if mode == "insufficient" || mode == "stale" {
				if commitErr == nil || !reflect.DeepEqual(beforeEq, afterEq) || !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeChar, afterChar) || conn.write.Len() != 0 {
					t.Fatal("rejected repair mutated assets or sent a refresh", commitErr)
				}
				return
			}
			if commitErr != nil || !out.Committed || strings.Contains(out.Message, "刷新失败") {
				t.Fatal(out, commitErr)
			}
			if afterChar.Stats["gold"] != beforeChar.Stats["gold"]+v.Gold || len(afterEq.Entries) != len(beforeEq.Entries) || !reflect.DeepEqual(beforeInv.Slots["0:9"], afterInv.Slots["0:9"]) {
				t.Fatal("repair removed an item or charged the wrong amount")
			}
			var projected []currentItemListEntry
			for _, key := range []string{"12", "14"} {
				before, after := beforeEq.Entries[key], repo.CloneEquipment(afterEq).Entries[key]
				def, e := s.pvfItemCatalog.ResolveItem(uint32(after.ItemID))
				if e != nil || def.Durability <= 1 || binary.LittleEndian.Uint16(after.RawEntry[0x0b:0x0d]) != def.Durability {
					t.Fatal("durability not repaired", key, e)
				}
				after.RawEntry = append([]byte(nil), after.RawEntry...)
				copy(after.RawEntry[0x0b:0x0d], before.RawEntry[0x0b:0x0d])
				after.Extra["durability"] = before.Extra["durability"]
				if !reflect.DeepEqual(before, after) {
					t.Fatal("repair changed identity, enhancement, enchantment or opaque data", key)
				}
				row, ok := currentItemListEntryFromEquipment(afterEq.Entries[key])
				if !ok {
					t.Fatal("unprojectable worn row")
				}
				projected = append(projected, row)
			}
			want := buildCurrentItemUpdateBody(3, projected)
			found := 0
			for remaining := conn.write.Bytes(); len(remaining) > 0; {
				packet, tail := splitGameServerUpperPacket(t, remaining)
				remaining = tail
				if packet.Header.MsgID == 14 && len(packet.Body) == len(want) && packet.Body[0] == 3 {
					found++
					if !bytes.Equal(packet.Body, want) {
						t.Fatalf("worn update must contain committed complete rows, never empty sentinels: %x", packet.Body)
					}
				}
			}
			if found != 1 {
				t.Fatal("missing authoritative worn projection", found)
			}
			conn.write.Reset()
			if again, e := s.applyHelperUtility(ctx, id, request); e != nil || !reflect.DeepEqual(again, out) || conn.write.Len() != 0 {
				t.Fatal("repair replay emitted another mutation", again, e)
			}
			replayed, _, _ := g.Character.Load(ctx, "19")
			if replayed.Stats["gold"] != afterChar.Stats["gold"] {
				t.Fatal("repair charged twice")
			}
		})
	}
}
