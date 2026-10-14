package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"testing"
)

func TestRealPVFVagabondCenterButtonSwap(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		main, sub bool
		job       string
		growth    int64
		level     int
		count     uint32
		ok        bool
	}{
		{"both", true, true, "11", 4, 90, 0, true}, {"main_only", true, false, "11", 0x14, 90, 0, true}, {"sub_only", false, true, "11", 0x24, 90, 0, true}, {"other_job", true, true, "0", 4, 90, 0, false}, {"other_branch", true, true, "11", 1, 90, 0, false}, {"low_level", true, true, "11", 4, 1, 0, false}, {"invalid_count", true, true, "11", 4, 90, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoginWearBridgeFixture(t)
			f.service.initialEquipmentArchive = archive
			f.service.options.currentClientVersion = "official-24.9"
			f.session.op19QuietResolving = true
			ctx := context.Background()
			c, _, _ := f.group.Character.Load(ctx, "19")
			c.Job = tc.job
			c.Level = tc.level
			c.Stats = map[string]int64{dnfrepo.CharacterGrowTypeStatKey: tc.growth}
			if err := f.group.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			e, _, _ := f.group.Equipment.Load(ctx, "19")
			base := e.Entries["12"]
			e.Entries = map[string]dnfrepo.EquipmentEntry{}
			if tc.main {
				main := base
				main.RawEntry = loginWearAnchorRawEntry(12, main.ItemID, 203004656, 48)
				main.RawEntry[0x0A] = 12
				main.Extra = map[string]string{"item_kind": "equipment", "equipment_type": "[weapon]", "quality_seed": "203004656", "note": "main"}
				e.Entries["12"] = main
			}
			if tc.sub {
				sub := base
				sub.SlotIndex = 24
				sub.RawEntry = loginWearAnchorRawEntry(24, sub.ItemID, 70238363, 48)
				sub.RawEntry[0x0A] = 9
				sub.Extra = map[string]string{"item_kind": "equipment", "equipment_type": "[weapon]", "quality_seed": "70238363", "note": "sub"}
				e.Entries["24"] = sub
			}
			if err := f.group.Equipment.Save(ctx, e); err != nil {
				t.Fatal(err)
			}
			before, _, _ := f.group.Equipment.Load(ctx, "19")
			src, dst := int32(0), int32(0)
			if tc.main {
				src = loginWearBridgeWeaponItemID
			}
			if tc.sub {
				dst = loginWearBridgeWeaponItemID
			}
			body := loginWearBridgeBody(3, 12, src, 3, 24, dst)
			binary.LittleEndian.PutUint32(body[7:11], tc.count)
			body[26], body[27] = 0, 0
			handled, err := f.service.handleAlignedGameCommand(f.session, 1, 19, body)
			if err != nil || !handled {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			after, _, _ := f.group.Equipment.Load(ctx, "19")
			if !tc.ok {
				if !reflect.DeepEqual(before, after) {
					t.Fatal("rejected button changed equipment")
				}
				return
			}
			ack, _ := splitLongHengGameServerUpperPacket(t, f.buffer.write.Bytes())
			if len(ack.Body) != 11 || ack.Body[0] != 1 || ack.Body[1] != 3 || binary.LittleEndian.Uint16(ack.Body[2:4]) != 12 || binary.LittleEndian.Uint32(ack.Body[4:8]) != 0 || ack.Body[8] != 3 || binary.LittleEndian.Uint16(ack.Body[9:11]) != 24 {
				t.Fatalf("ACK endpoints/count=%x", ack.Body)
			}
			for _, pair := range [][2]string{{"12", "24"}, {"24", "12"}} {
				old, found := before.Entries[pair[0]]
				got := after.Entries[pair[1]]
				if found {
					if got.ItemID != old.ItemID || !bytes.Equal(got.RawEntry[2:], old.RawEntry[2:]) || got.Extra["note"] != old.Extra["note"] {
						t.Fatalf("lost instance %s -> %s", pair[0], pair[1])
					}
				} else if got.ItemID != 0 {
					t.Fatalf("empty slot acquired item %s", pair[1])
				}
			}
		})
	}
}
