package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// These requests use the existing op1420 decoder contract. They are isolated
// server replays, not a capture of character 123 or native-client acceptance.
func TestDoc136RealPVFCatalystHandler(t *testing.T) {
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
	for _, tc := range []struct {
		name      string
		count     uint32
		available int64
		carry     int64
		level     int
		ok        bool
	}{
		{"quest_reward_800", 800, 800, 0, 55, true},
		{"exact_750", 750, 800, 0, 55, true},
		{"saved_points", 700, 800, 5000, 55, true},
		{"below_points", 749, 800, 0, 55, false},
		{"short_stack", 800, 799, 0, 55, false},
		{"below_level", 800, 800, 0, 54, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			character := dnfrepo.CharacterRecord{CharacterID: "123", AccountID: "catalyst-test", Level: tc.level, Stats: map[string]int64{currentEpicProductionCarryGroup2Stat: tc.carry}}
			inv := dnfrepo.InventoryRecord{CharacterID: "123", Slots: map[string]dnfrepo.ItemStack{"0:125": {ItemID: 10302667, Count: tc.available}}}
			if err := g.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			if err := g.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 123, accountID: "catalyst-test"}
			s := &Service{initialEquipmentArchive: archive, pvfItemCatalog: items, repositoryProvider: func() (dnfrepo.Group, bool) { return g, true }, options: options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
			body := make([]byte, 18)
			binary.LittleEndian.PutUint32(body, 10163130)
			binary.LittleEndian.PutUint16(body[4:], 1)
			binary.LittleEndian.PutUint32(body[6:], 10302667)
			binary.LittleEndian.PutUint32(body[10:], 125)
			binary.LittleEndian.PutUint32(body[14:], tc.count)
			if err := epicProductionGameplayModule().UpperHandlers[1420](s, session, gameplayRequest{Opcode: 1420, Classification: dnfproto.DefaultChannelClassification, Body: body}); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.Classification != 1 || binary.LittleEndian.Uint16(conn.write.Bytes()[1:3]) != 1420 {
				t.Fatalf("unexpected ACK: %+v", ack)
			}
			afterI, _, err := g.Inventory.Load(ctx, "123")
			if err != nil {
				t.Fatal(err)
			}
			afterC, _, err := g.Character.Load(ctx, "123")
			if err != nil {
				t.Fatal(err)
			}
			if !tc.ok {
				if len(ack.Body) != 2 || ack.Body[0] != 0 || len(rest) != 0 {
					t.Fatalf("rejection wire: ack=%x rest=%x", ack.Body, rest)
				}
				if !reflect.DeepEqual(inv, afterI) || !reflect.DeepEqual(character, afterC) {
					t.Fatal("rejection mutated assets")
				}
				return
			}
			carry := uint32((tc.carry + int64(tc.count)*100) % 75000)
			wantAck := []byte{1, 1, 0, 0, 0, 2, 0, 0, 0, 0}
			binary.LittleEndian.PutUint32(wantAck[6:], carry)
			if !bytes.Equal(ack.Body, wantAck) {
				t.Fatalf("ACK=%x want=%x", ack.Body, wantAck)
			}
			if afterI.Slots["0:125"].Count != tc.available-int64(tc.count) || afterC.Stats[currentEpicProductionCarryGroup2Stat] != int64(carry) {
				t.Fatalf("wrong persisted materials/carry: %+v %+v", afterI, afterC.Stats)
			}
			var output int64
			for _, stack := range afterI.Slots {
				if stack.ItemID == 10163130 {
					output += stack.Count
				}
			}
			if output != 1 {
				t.Fatalf("output=%d want1", output)
			}
			if len(rest) < 3 || binary.LittleEndian.Uint16(rest[1:3]) != 14 {
				t.Fatalf("missing post-ACK item refresh: %x", rest)
			}
			update, tail := splitLongHengGameServerUpperPacket(t, rest)
			if update.Header.Classification != 0 || len(tail) != 0 || len(update.Body) < 3 || update.Body[0] != 0 {
				t.Fatalf("unexpected item refresh: %+v tail=%x", update, tail)
			}
			rows := int(binary.LittleEndian.Uint16(update.Body[1:3]))
			if len(update.Body) != 3+rows*119 {
				t.Fatalf("bad row width: %x", update.Body)
			}
			seen := map[int16]bool{}
			for offset := 3; offset < len(update.Body); offset += 119 {
				row := update.Body[offset : offset+119]
				slot := int16(binary.LittleEndian.Uint16(row))
				stack, exists := afterI.Slots[currentCeraShopInventorySlotKey(0, slot)]
				itemID := uint32(stack.ItemID)
				if !exists {
					itemID = 0xffffffff
				} // Native empty-slot sentinel.
				if binary.LittleEndian.Uint32(row[2:6]) != itemID || binary.LittleEndian.Uint32(row[6:10]) != uint32(stack.Count) {
					t.Fatalf("wire/store mismatch slot=%d row=%x stack=%+v", slot, row, stack)
				}
				seen[slot] = true
			}
			if !seen[125] || len(seen) != 2 {
				t.Fatalf("missing consumed or granted row: %v", seen)
			}
		})
	}
}

func TestEpicProductionCompoundRejectsMissingSession(t *testing.T) {
	body := make([]byte, 18)
	binary.LittleEndian.PutUint32(body, 10163130)
	binary.LittleEndian.PutUint16(body[4:], 1)
	binary.LittleEndian.PutUint32(body[6:], 10302667)
	binary.LittleEndian.PutUint32(body[10:], 125)
	binary.LittleEndian.PutUint32(body[14:], 800)
	for _, name := range []string{"nil_session", "unselected_character"} {
		t.Run(name, func(t *testing.T) {
			// An invalid session must be rejected before loading/caching a PVF
			// failure or reaching the asset repository, as start/change already do.
			s := &Service{options: options{pvfPath: filepath.Join(t.TempDir(), "absent.pvf"), gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (dnfrepo.Group, bool) {
				t.Fatal("invalid session reached repository")
				return dnfrepo.Group{}, false
			}}
			var session *gameSession
			conn := &bufferConn{}
			if name == "unselected_character" {
				session = &gameSession{conn: conn, accountID: "catalyst-test"}
			}
			err := s.handleCurrentEpicProductionCompound(session, body)
			if s.initialEquipmentLoadErr != nil || s.initialEquipmentArchive != nil {
				t.Errorf("invalid session accessed PVF: %v", s.initialEquipmentLoadErr)
			}
			if session == nil {
				if !errors.Is(err, errCurrentEpicProductionInvalid) {
					t.Fatalf("missing session err=%v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				ack, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
				if !bytes.Equal(ack.Body, []byte{0, 3}) || len(rest) != 0 {
					t.Fatalf("missing-character response=%x tail=%x", ack.Body, rest)
				}
			}
		})
	}
}
