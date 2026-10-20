package dnfbridge

import (
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Latest v56 22C50xx calls 22BF580 (DSTR/u32/u16), then the event u32,
// v122 u8, 22C1650 disguise pair, and 22BF840 profession/EXP.
// The extra u16 is proven by 22BF699 -> 37E8350 (cursor advances by two).
// A live test1 packet omitted it and decoded profession=3/EXP=36 as disguise.
func TestNativeSceneCursorAfterCreatureAndOrganizationNames(t *testing.T) {
	for _, name := range []string{"", "test", "456", "冒险团"} {
		for _, petName := range []string{"", "宠物"} {
			character := dnfrepo.CharacterRecord{Stats: map[string]int64{"expert_job_type": 3, "expert_job_exp": 133}}
			creature := currentEquippedCreatureSnapshot{}
			if petName != "" {
				creature.itemID, creature.name, creature.aliveState = 401000001, petName, 1
				creature.serialOrHandle = 19
			}
			var writer packetWriter
			writeCurrentSceneObjectEntryTailWithCreatureAndAdventureName(&writer, character, true, creature, name)
			body := writer.bytes()
			position := 33 // Empty equipment summary, through creature template u32.
			readName := func(limit int) []byte {
				length := int(binary.LittleEndian.Uint32(body[position:]))
				position += 4
				if length >= limit || position+length > len(body) {
					t.Fatalf("native name reader rejected %q/%q length=%d", name, petName, length)
				}
				value := body[position : position+length]
				position += length
				return value
			}
			readName(31)
			position += 2 // creature alive and 1C03B80.
			level := body[position]
			position += 1 + 4 // 1C03C90 organization level, then v120 u32.
			organization := readName(64)
			if string(organization) != string(rosterNameBytes(name)) || (level != 0) != (name != "") {
				t.Fatalf("native organization=%x level=%d", organization, level)
			}
			position += 4 // organization auxiliary u32.
			if binary.LittleEndian.Uint16(body[position:]) != 0 {
				t.Fatal("display-only adventure name assigned an organization identifier")
			}
			position += 2 // Latest v56 organization identifier u16.
			if binary.LittleEndian.Uint32(body[position:]) != currentSceneActorStateEventArg {
				t.Fatal("native event argument shifted")
			}
			position += 4 + 1 // event argument, v122.
			if body[position] != 0 || body[position+1] != 0 || body[position+2] != 3 || binary.LittleEndian.Uint32(body[position+3:]) != 133 {
				t.Fatalf("native disguise/profession cursor=%d bytes=%x", position, body[position:position+7])
			}
			position += 2 + 5 + 7 + 4 + 12 // disguise, profession, v125..128, 1C04E00, HonorExpert.
			if body[position] != currentSceneNormalTownVisualState {
				t.Fatalf("native visibility=%x at %d", body[position], position)
			}
		}
	}
}
