package dnfbridge

import (
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestBuildCurrentAdventureRepresentCharactersBodyGroupsExactRosterEntries(t *testing.T) {
	characters := []dnfrepo.CharacterRecord{
		{
			CharacterID: "267",
			Slot:        1,
			Name:        "pou.ut",
			Job:         "11",
			Level:       90,
			Stats: map[string]int64{
				"grow_type":       0x21,
				"server_group_id": 0,
			},
		},
		{
			CharacterID: "300",
			Slot:        0,
			Name:        "other",
			Job:         "11",
			// 收集簿只收满级角色（PVF etc/etc.chn.str adv_collection_tooltip00：
			// "Lv90级以上且二次觉醒的角色"），所以这一行必须是满级，本用例才只检验
			// "按 server group 分组"这一件事。
			Level: 90,
			Stats: map[string]int64{
				"grow_type":       0x21,
				"server_group_id": 2,
			},
		},
		{
			CharacterID: "301",
			Name:        "wrong-grow",
			Job:         "11",
			Level:       90,
			Stats:       map[string]int64{"grow_type": 0x22},
		},
		{
			CharacterID: "302",
			Name:        "wrong-job",
			Job:         "12",
			Level:       90,
			Stats:       map[string]int64{"grow_type": 0x21},
		},
	}

	body := buildCurrentAdventureRepresentCharactersBody(11, 1, characters)
	const wantLength = 1 + 2*(1+1) + 2*currentAdventureRepresentEntrySize
	if len(body) != wantLength {
		t.Fatalf("body length=%d want=%d: %x", len(body), wantLength, body)
	}
	if body[0] != 2 {
		t.Fatalf("group count=%d", body[0])
	}

	offset := 1
	if body[offset] != 1 || body[offset+1] != 1 {
		t.Fatalf("first group header=%x", body[offset:offset+2])
	}
	offset += 2
	first := body[offset : offset+currentAdventureRepresentEntrySize]
	if first[currentAdventureRepresentGroupOffset] != 1 {
		t.Fatalf("first row server group=%d", first[currentAdventureRepresentGroupOffset])
	}
	if got := binary.LittleEndian.Uint32(first[currentAdventureRepresentStyleOffset:]); got != 0 {
		t.Fatalf("first row presentation style=%d", got)
	}
	if got := binary.LittleEndian.Uint32(first[currentAdventureRepresentLevelOffset:]); got != 90 {
		t.Fatalf("first character level=%d", got)
	}
	if got := binary.LittleEndian.Uint32(first[currentAdventureRepresentIDOffset:]); got != 267 {
		t.Fatalf("first character id=%d", got)
	}
	if got := string(first[currentAdventureRepresentNameOffset : currentAdventureRepresentNameOffset+6]); got != "pou.ut" {
		t.Fatalf("first character name=%q", got)
	}

	offset += currentAdventureRepresentEntrySize
	if body[offset] != 2 || body[offset+1] != 1 {
		t.Fatalf("second group header=%x", body[offset:offset+2])
	}
	offset += 2
	second := body[offset : offset+currentAdventureRepresentEntrySize]
	if second[currentAdventureRepresentGroupOffset] != 2 {
		t.Fatalf("second row server group=%d", second[currentAdventureRepresentGroupOffset])
	}
	if got := binary.LittleEndian.Uint32(second[currentAdventureRepresentIDOffset:]); got != 300 {
		t.Fatalf("second character id=%d", got)
	}
	if got := binary.LittleEndian.Uint32(second[currentAdventureRepresentLevelOffset:]); got != 90 {
		t.Fatalf("second character level=%d", got)
	}
}

// TestBuildCurrentAdventureRepresentCharactersExcludesUnderCapCollectBookCards is
// the live-report regression: the 角色收集簿 (op1467) listed the account's Lv40
// 「菲八番茄」. The runtime PVF states the collection condition in
// etc/etc.chn.str key adv_collection_tooltip00 ("Lv90级以上且二次觉醒的角色"),
// so an under-cap character must never reach the op1467 row set even when its
// job/grow match the request exactly.
func TestBuildCurrentAdventureRepresentCharactersExcludesUnderCapCollectBookCards(t *testing.T) {
	underCap := dnfrepo.CharacterRecord{
		CharacterID: "2",
		Slot:        6,
		Name:        "菲八番茄",
		Job:         "0",
		Level:       40,
		Stats:       map[string]int64{"grow_type": 0x21},
	}
	atCap := dnfrepo.CharacterRecord{
		CharacterID: "900",
		Slot:        1,
		Name:        "满级角色",
		Job:         "0",
		Level:       currentAdventureCharacterLevelCap,
		Stats:       map[string]int64{"grow_type": 0x21},
	}
	characters := []dnfrepo.CharacterRecord{underCap, atCap}
	for _, packed := range []int64{0, 1, 0x11, 0x31, 0x121, -1} {
		ineligible := atCap
		ineligible.Stats = map[string]int64{"grow_type": packed}
		characters = append(characters, ineligible)
		if currentAdventureCollectBookEligible(ineligible) {
			t.Fatalf("non-second-awakened/invalid grow type %#x was eligible", packed)
		}
	}
	missingGrow := atCap
	missingGrow.Stats = nil
	characters = append(characters, missingGrow)
	if currentAdventureCollectBookEligible(missingGrow) {
		t.Fatal("missing authoritative grow type was eligible")
	}

	if currentAdventureCollectBookEligible(underCap) {
		t.Fatal("under-cap character was reported as collect-book eligible")
	}
	if !currentAdventureCollectBookEligible(atCap) {
		t.Fatal("max-level character was not reported as collect-book eligible")
	}
	if got := countCurrentAdventureRepresentCharacters(0, 1, characters); got != 1 {
		t.Fatalf("collect-book candidate count=%d want=1", got)
	}

	body := buildCurrentAdventureRepresentCharactersBody(0, 1, characters)
	if body[0] != 1 {
		t.Fatalf("group count=%d want=1", body[0])
	}
	row := body[3 : 3+currentAdventureRepresentEntrySize]
	if got := binary.LittleEndian.Uint32(row[currentAdventureRepresentIDOffset:]); got != 900 {
		t.Fatalf("collect-book row character id=%d want=900", got)
	}
	if got := binary.LittleEndian.Uint32(row[currentAdventureRepresentLevelOffset:]); got != currentAdventureCharacterLevelCap {
		t.Fatalf("collect-book row level=%d want=%d", got, currentAdventureCharacterLevelCap)
	}
	if wantLength := 1 + 2 + currentAdventureRepresentEntrySize; len(body) != wantLength {
		t.Fatalf("body length=%d want=%d: %x", len(body), wantLength, body)
	}
}

func TestAdventureGameplayModuleOwnsRepresentCharacterLookupOnBothTransports(t *testing.T) {
	module := adventureGameplayModule()
	opcode := currentAdventureRepresentCharactersMsgID
	if module.LegacyHandlers[opcode] == nil {
		t.Fatal("legacy op1467 handler is missing")
	}
	if module.UpperHandlers[opcode] == nil {
		t.Fatal("upper op1467 handler is missing")
	}
}
