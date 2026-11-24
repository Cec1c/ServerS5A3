package memory

import (
	"context"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPVPComboSettingsDurableWriteFailureRollsBackBothProfiles(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryGroup()
	character := repo.CharacterRecord{CharacterID: "77", AccountID: "acc", Job: "9"}
	if err := r.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	before := repo.SkillRecord{CharacterID: "77", Layouts: map[int]repo.SkillLayout{0: {0: 118}}}
	if err := r.Skill.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	combo := repo.SettingsRecord{Scope: "character:77:pvp_dark_knight_combo_v1", Values: map[string]string{"page_0": "old"}}
	profile := repo.SettingsRecord{Scope: "character:77:pvp_dojo_skills_v1", Values: map[string]string{"profile": "old"}}
	for _, record := range []repo.SettingsRecord{combo, profile} {
		if err := r.Settings.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	u := &memoryCharacterSettlementUnitOfWork{character: r.Character, quests: r.Quest, inventory: r.Inventory,
		equipment: r.Equipment, skills: r.Skill, settings: &comboSettingsWriteThenFail{SettingsRepository: r.Settings, fail: true}}
	err := u.WithinCharacterSettlement(ctx, "77", func(tx repo.Group) error {
		changed := repo.CloneSkill(before)
		changed.Layouts[0][150] = 74
		if err := tx.Skill.Save(ctx, changed); err != nil {
			return err
		}
		for _, record := range []repo.SettingsRecord{combo, profile} {
			record = repo.CloneSettings(record)
			for key := range record.Values {
				record.Values[key] = "changed"
			}
			if err := tx.Settings.Save(ctx, record); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		t.Fatal("durable settings failure was lost")
	}
	after, _, _ := r.Skill.Load(ctx, "77")
	afterCombo, _, _ := r.Settings.Load(ctx, combo.Scope)
	afterProfile, _, _ := r.Settings.Load(ctx, profile.Scope)
	if !reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterCombo, combo) || !reflect.DeepEqual(afterProfile, profile) {
		t.Fatal("partial PK combo/layout/profile commit survived storage failure")
	}
}
