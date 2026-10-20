package memory

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type comboSettingsWriteThenFail struct {
	dnfrepo.SettingsRepository
	fail bool
}

func (r *comboSettingsWriteThenFail) Save(ctx context.Context, record dnfrepo.SettingsRecord) error {
	if err := r.SettingsRepository.Save(ctx, record); err != nil {
		return err
	}
	if r.fail {
		r.fail = false
		return errors.New("连招配置写入后模拟失败")
	}
	return nil
}

func TestComboSettingsWriteFailureRestoresSkillLayoutAndBothPages(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc", Job: "9"}
	oldSkill := dnfrepo.SkillRecord{CharacterID: "77", Layouts: map[int]dnfrepo.SkillLayout{0: {0: 118, 198: 8}}}
	oldSettings := dnfrepo.SettingsRecord{Scope: "character:77:dark_knight_combo", Values: map[string]string{"page_0": "old0", "page_1": "old1"}}
	if err := r.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := r.Skill.Save(ctx, oldSkill); err != nil {
		t.Fatal(err)
	}
	if err := r.Settings.Save(ctx, oldSettings); err != nil {
		t.Fatal(err)
	}
	u := &memoryCharacterSettlementUnitOfWork{character: r.Character, quests: r.Quest, inventory: r.Inventory, equipment: r.Equipment, skills: r.Skill, settings: &comboSettingsWriteThenFail{SettingsRepository: r.Settings, fail: true}}
	err := u.WithinCharacterSettlement(ctx, "77", func(tx dnfrepo.Group) error {
		skill, _, err := tx.Skill.Load(ctx, "77")
		if err != nil {
			return err
		}
		skill.Layouts[0] = dnfrepo.SkillLayout{0: 118, 150: 8}
		if err := tx.Skill.Save(ctx, skill); err != nil {
			return err
		}
		settings, _, err := tx.Settings.Load(ctx, oldSettings.Scope)
		if err != nil {
			return err
		}
		settings.Values["page_0"] = "changed"
		return tx.Settings.Save(ctx, settings)
	})
	if err == nil {
		t.Fatal("写入失败被吞掉")
	}
	gotSkill, _, _ := r.Skill.Load(ctx, "77")
	gotSettings, _, _ := r.Settings.Load(ctx, oldSettings.Scope)
	if !reflect.DeepEqual(gotSkill, oldSkill) || !reflect.DeepEqual(gotSettings, oldSettings) {
		t.Fatalf("连招与技能布局未整体回滚: skill=%+v settings=%+v", gotSkill, gotSettings)
	}
}

func TestComboSettingsSettlementRejectsForeignAndUnrelatedScopes(t *testing.T) {
	r := NewMemoryGroup()
	for _, scope := range []string{"character:78:dark_knight_combo", "character:77:skill_commands", "account:acc:settings", "global:guild_registry:v1"} {
		t.Run(scope, func(t *testing.T) {
			err := r.CharacterSettlement.WithinCharacterSettlement(context.Background(), "77", func(tx dnfrepo.Group) error {
				if _, _, err := tx.Settings.Load(context.Background(), scope); !errors.Is(err, dnfrepo.ErrCharacterSettlementTransactionUnavailable) {
					t.Fatalf("越界读取未拒绝: %v", err)
				}
				return tx.Settings.Save(context.Background(), dnfrepo.SettingsRecord{Scope: scope})
			})
			if !errors.Is(err, dnfrepo.ErrCharacterSettlementTransactionUnavailable) {
				t.Fatalf("越界保存未拒绝: %v", err)
			}
		})
	}
}
