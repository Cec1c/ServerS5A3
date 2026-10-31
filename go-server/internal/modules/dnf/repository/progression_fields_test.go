package repository

import (
	"context"
	"testing"
)

type readOnlyNarrowCharacter struct {
	CharacterRepository
	narrow bool
}

func (r *readOnlyNarrowCharacter) Load(context.Context, string) (CharacterRecord, bool, error) {
	return CharacterRecord{Name: "preserved", Job: "4"}, true, nil
}
func (r *readOnlyNarrowCharacter) LoadProgressionCharacter(context.Context, string) (CharacterRecord, bool, error) {
	r.narrow = true
	return CharacterRecord{}, true, nil
}

type readOnlyNarrowSkill struct {
	SkillRepository
	narrow bool
}

func (r *readOnlyNarrowSkill) Load(context.Context, string) (SkillRecord, bool, error) {
	return SkillRecord{Trees: SkillTreeState{Expanded: true}}, true, nil
}
func (r *readOnlyNarrowSkill) LoadProgressionSkill(context.Context, string) (SkillRecord, bool, error) {
	r.narrow = true
	return SkillRecord{}, true, nil
}

func TestProgressionPartialReadersRequireMatchingWriters(t *testing.T) {
	c := &readOnlyNarrowCharacter{}
	character, ok, err := LoadProgressionCharacter(context.Background(), c, "c")
	if err != nil || !ok || c.narrow || character.Name != "preserved" {
		t.Fatalf("unsafe character partial read: %+v %v", character, err)
	}
	s := &readOnlyNarrowSkill{}
	skill, ok, err := LoadProgressionSkill(context.Background(), s, "c")
	if err != nil || !ok || s.narrow || !skill.Trees.Expanded {
		t.Fatalf("unsafe skill partial read: %+v %v", skill, err)
	}
}
