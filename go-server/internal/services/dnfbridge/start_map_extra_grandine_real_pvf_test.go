package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Real quest MAP definitions feed the production planner/ledger and encoder.
// The optional export is consumed by the independent registered op29 native
// reader. It contains generated packets, not captured player traffic.
func TestRealPVFGrandineAndTimeStoneExtraNativeRows(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("matching real PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{dungeonMonsterTable: catalog}
	type exportedCase struct {
		Name            string      `json:"name"`
		Rows            [][9]uint32 `json:"rows"`
		ExtraAndTailHex string      `json:"extraAndTailHex"`
	}
	var exported []exportedCase
	for _, pair := range []struct {
		dungeon, mapID int64
		item           uint32
		count          int
	}{
		{104, 76617, 3341, 0}, {104, 76628, 3341, 7},
		{104, 76637, 3341, 5}, {104, 76649, 3341, 4}, {104, 76659, 3341, 10},
		{310, 36765, 10158700, 4}, {313, 33753, 10158700, 4},
		{315, 44107, 10158700, 6}, {315, 44113, 10158700, 6},
	} {
		dungeon, ok := table.FindDungeon(pair.dungeon)
		if !ok {
			t.Fatal("dungeon missing", pair.dungeon)
		}
		room, ok := table.FindMap(pair.mapID)
		if !ok {
			t.Fatal("map missing", pair.mapID)
		}
		scene := worldmap.DungeonRoomScene{Map: worldmap.ResolvedMap{Map: room}, SpecialPassiveObjects: room.SpecialPassiveObjects}
		for _, seed := range []uint32{1, 17, 0xdeadbeef} {
			owner := newRuntimeDungeonDropOwner()
			seen := make(map[uint32]bool)
			// These isolated runtimes share the drop ledger. Carry the next
			// object key forward as the production party allocator does.
			nextKey := uint32(401)
			for _, member := range []uint16{19, 20, 21} {
				name := fmt.Sprintf("d%d-map%d-seed%d-member%d", pair.dungeon, pair.mapID, seed, member)
				runtime := &runtimeDungeonState{Dungeon: dungeon, Seed: seed, NextObjectKey: nextKey, DropOwner: owner}
				session := &gameSession{selectedCharacterID: member}
				if err := service.prepareCurrentDungeonPassiveObjectDrops(session, runtime, scene, "native-contract"); err != nil {
					t.Fatal(name, err)
				}
				nextKey = runtime.NextObjectKey
				rows, vendor := currentDungeonPassiveObjectDropEntries(runtime, scene)
				if len(rows) != pair.count || vendor {
					t.Fatalf("%s rows=%+v vendor=%v", name, rows, vendor)
				}
				item := exportedCase{Name: name, Rows: make([][9]uint32, 0, len(rows))}
				for _, row := range rows {
					if row.ItemID != pair.item || row.StackCount != 1 || seen[row.GlobalSequence] {
						t.Fatalf("%s identity/count=%+v", name, row)
					}
					seen[row.GlobalSequence] = true
					item.Rows = append(item.Rows, [9]uint32{uint32(row.PassiveObjectIndex), row.GlobalSequence, row.ItemID, row.StackCount, uint32(row.Endurance), uint32(row.AmplifyType), uint32(row.AmplifyValue), uint32(row.ExtendedValue), uint32(row.ExtendedFlag)})
				}
				if err := service.prepareCurrentDungeonPassiveObjectDrops(session, runtime, scene, "rebuild"); err != nil {
					t.Fatal(name, err)
				}
				again, _ := currentDungeonPassiveObjectDropEntries(runtime, scene)
				if !reflect.DeepEqual(rows, again) {
					t.Fatal(name, "rebuild changed rewards")
				}
				packet := currentDungeonStartMap{MapID: uint32(pair.mapID), ExtraEntries: rows, HellPartyFogFlag: 1, PartyMemberIndex: 0xff}
				body, err := packet.Build()
				if err != nil {
					t.Fatal(name, err)
				}
				// Independently consume row fields and all tail bytes; never use
				// the writer's width helper to derive the expected wire contract.
				reader := bytes.NewReader(body[20:])
				for _, want := range rows {
					var got currentDungeonStartMapExtraEntry
					if err := binary.Read(reader, binary.LittleEndian, &got); err != nil {
						t.Fatal(name, err)
					}
					if got != want {
						t.Fatalf("%s got=%+v want=%+v", name, got, want)
					}
				}
				if len(body) != 23+21*len(rows) || body[19] != byte(len(rows)) || reader.Len() != 3 || !bytes.Equal(body[len(body)-3:], []byte{1, 0, 0xff}) {
					t.Fatalf("%s tail misaligned: %x", name, body)
				}
				item.ExtraAndTailHex = hex.EncodeToString(body[19:])
				exported = append(exported, item)
			}
		}
		t.Logf("dungeon=%d map=%d rows=%d item=%d quantity=1; three seeds and three members", pair.dungeon, pair.mapID, pair.count, pair.item)
	}
	if output := os.Getenv("DNF_START_MAP_EXTRA_EXPORT"); output != "" {
		data, err := json.MarshalIndent(exported, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write(data); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
