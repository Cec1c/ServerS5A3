package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPVPPeerSkillTreesAndMode1Placement(t *testing.T) {
	record := dnfrepo.SkillRecord{Skills: map[int64]dnfrepo.SkillState{
		73: {Level: 12}, 46: {Level: 9, Enabled: true}, 49: {Level: 0},
	}, Trees: dnfrepo.SkillTreeState{Other: dnfrepo.SkillProfile{Skills: map[int64]dnfrepo.SkillState{68: {Level: 24}}}}}
	got, err := buildCurrentPVPPeerSkills(record, 1)
	want := []byte{1, 2, 46, 0, 9, 73, 0, 12, 1, 68, 0, 24}
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("peer skill trees=%x err=%v", got, err)
	}
	reader := csharpLegacyUserInfoReader{}
	character := dnfrepo.CharacterRecord{Level: 90}
	baseline := reader.buildCurrentUserInfoMode1(character, true, 29, 7)
	reader.peerSkills = got
	body := reader.buildCurrentUserInfoMode1(character, true, 29, 7)
	offset := len(baseline) - 23
	if !bytes.Equal(baseline[offset:offset+3], []byte{255, 0, 0}) ||
		!bytes.Equal(body[:offset], baseline[:offset]) || !bytes.Equal(body[offset:offset+len(want)], want) ||
		!bytes.Equal(body[offset+len(want):], baseline[offset+3:]) {
		t.Fatalf("skill block shifted native mode1 fields: %x", body)
	}
	if _, err := buildCurrentPVPPeerSkills(record, 2); err == nil {
		t.Fatal("accepted invalid active tree")
	}
	for _, invalid := range []struct {
		id    int64
		level int
	}{{512, 1}, {-1, 1}, {46, 256}, {46, -1}} {
		bad := dnfrepo.CloneSkill(record)
		bad.Skills[invalid.id] = dnfrepo.SkillState{Level: invalid.level}
		if _, err := buildCurrentPVPPeerSkills(bad, 0); err == nil {
			t.Fatalf("accepted invalid native skill: %+v", invalid)
		}
	}
	for id := int64(0); id < 256; id++ {
		record.Skills[id] = dnfrepo.SkillState{Level: 1}
	}
	if _, err := buildCurrentPVPPeerSkills(record, 0); err == nil {
		t.Fatal("wrapped u8 skill count")
	}

	s := pvpRoomTestService(t)
	for _, id := range []uint16{29, 30} {
		player := &gameSession{selectedCharacterID: id, channel: s.catalog.Channels()[1],
			townPositionSnapshot: currentTownPositionSnapshot{CharacterID: id, PositionValid: true}}
		member, err := s.loadCurrentPVPMember(player)
		if err != nil {
			t.Fatal(err)
		}
		tail := member.mode1[len(member.mode1)-29:]
		if binary.LittleEndian.Uint16(member.mode1[21:]) != id ||
			!bytes.Equal(tail[:9], []byte{0, 1, 46, 0, byte(id - 20), 1, 46, 0, 1}) {
			t.Fatalf("character %d received incorrect bound skills: %x", id, tail)
		}
	}
	repos, _ := s.repositoryGroup()
	character, _, _ = repos.Character.Load(context.Background(), "29")
	record, _, _ = repos.Skill.Load(context.Background(), "29")
	record.Trees.Expanded = true
	record.Trees.Active = 1
	record.Skills, record.Trees.Other.Skills = record.Trees.Other.Skills, record.Skills
	if err := repos.Skill.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if skills, err := s.currentPVPPeerSkills(context.Background(), character); err != nil || skills[0] != 1 {
		t.Fatalf("active tree not restored: %x %v", skills, err)
	}
}
