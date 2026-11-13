package dnfbridge

import (
	"encoding/binary"
	"testing"
)

func TestPrivateFeatureCombatInspectionUsesTargetAndDoesNotExposePresets(t *testing.T) {
	s, a, b, ac, _, _ := partyDungeonSyncFixture(t)
	q := make([]byte, 24)
	copy(q, "CFQ1")
	binary.LittleEndian.PutUint16(q[4:], 2)
	q[6] = 6
	binary.LittleEndian.PutUint16(q[16:], b.selectedCharacterID)
	if err := s.handlePrivateCombatInspection(a, 1, q); err != nil {
		t.Fatal(err)
	}
	frames := splitUpperFrames(t, ac.frames())
	if len(frames) != 1 || frames[0].msgID != privateClientFeaturesOpcode {
		t.Fatalf("inspection response missing: %d frames", len(frames))
	}
	body := frames[0].body
	if len(body) != 512 || body[6] != 6 || binary.LittleEndian.Uint32(body[16:]) != uint32(b.selectedCharacterID) {
		t.Fatal("inspection returned wrong identity")
	}
	for i, v := range body[256:416] {
		if v != 0 {
			t.Fatalf("private presets or inventory leaked at %d", i+256)
		}
	}
	before := len(ac.frames())
	for _, id := range []uint16{0, a.selectedCharacterID, 65535} {
		binary.LittleEndian.PutUint16(q[16:], id)
		if err := s.handlePrivateCombatInspection(a, 1, q); err != nil {
			t.Fatal(err)
		}
	}
	if len(ac.frames()) != before {
		t.Fatal("invalid target emitted inspection")
	}
	binary.LittleEndian.PutUint16(q[16:], b.selectedCharacterID)
	q[18] = 1
	if err := s.handlePrivateCombatInspection(a, 1, q); err != nil {
		t.Fatal(err)
	}
	if len(ac.frames()) != before {
		t.Fatal("malformed target request accepted")
	}
}
