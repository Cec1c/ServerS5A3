package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestHatcheryFourthEntryPreservesWornAvatarContainerFraming(t *testing.T) {
	s, session, _, target := actualAntonHatchWarpOwnerFor(t, 224)
	repos, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("missing isolated repository")
	}
	character := strconv.Itoa(int(session.selectedCharacterID))
	equipment := dnfrepo.EquipmentRecord{CharacterID: character, Entries: map[string]dnfrepo.EquipmentEntry{
		"0":  {SlotIndex: 0, ItemID: 42601, Extra: map[string]string{"current_exe_equipment_type": "0", "item_kind": "avatar"}},
		"1":  {SlotIndex: 1, ItemID: 42603, Extra: map[string]string{"current_exe_equipment_type": "1", "item_kind": "avatar"}},
		"12": {SlotIndex: 12, ItemID: 101040493, Extra: map[string]string{"current_exe_equipment_type": "12", "durability": "48"}},
	}}
	if err := repos.Equipment.Save(context.Background(), equipment); err != nil {
		t.Fatal(err)
	}
	conn := session.conn.(*bufferConn)
	conn.write.Reset()
	if err := s.handleDungeonSelectUpper(session, encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: target, Difficulty: 2})); err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, packet := range splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes()) {
		if packet.Header.Classification == 0 && packet.Header.MsgID == 13 && len(packet.Body) >= 3 && packet.Body[0] == 3 {
			if body != nil {
				t.Fatal("duplicate worn container")
			}
			body = packet.Body
		}
	}
	if len(body) != 3+3*119+2*8 || binary.LittleEndian.Uint16(body[1:3]) != 3 {
		t.Fatalf("fourth hatchery worn container length=%d", len(body))
	}
	at := 3
	for _, slot := range []uint16{0, 1, 12} {
		if binary.LittleEndian.Uint16(body[at:at+2]) != slot {
			t.Fatalf("worn container cursor %d expected slot %d", at, slot)
		}
		at += 119
		if slot <= 11 {
			for n := 0; n < 2; n++ {
				if binary.LittleEndian.Uint32(body[at:at+4]) != 0 {
					t.Fatal("isolated avatar unexpectedly has extension payload")
				}
				at += 4
			}
		}
	}
	if at != len(body) {
		t.Fatal("worn container did not end at exact row boundary")
	}
	if output := os.Getenv("DNF_WORN_AVATAR_PACKET_OUTPUT"); output != "" {
		data, err := json.Marshal(struct {
			Body        []byte   `json:"body"`
			AvatarItems []uint32 `json:"avatarItems"`
		}{body, []uint32{42601, 42603}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
