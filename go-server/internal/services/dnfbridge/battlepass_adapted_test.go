package dnfbridge

import (
	"encoding/hex"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	"testing"
)

func TestBattlePassNativeSkillShapeAndClassification(t *testing.T) {
	raw, _ := hex.DecodeString("0047001c255a05001b00")
	if id, level, ok := decodeBattlePassSkill(raw); !ok || id != 71 || level != 27 {
		t.Fatal(id, level, ok)
	}
	if _, _, ok := decodeBattlePassSkill(append(raw, 1, 2, 3, 4)); ok {
		t.Fatal("未剥离的传输尾部被当作消息")
	}
	if len(stripLegacyTransportTrailer(append(raw, 1, 2, 3, 4), 10)) != 10 {
		t.Fatal("技能旧传输规格错误")
	}
	if got := battlePassSkillKind(dnfskill.Skill{Active: true, Path: "skill/swordman/QuickStanding.skl"}); got != 4 {
		t.Fatal(got)
	}
	if got := battlePassSkillKind(dnfskill.Skill{Active: true, UsesColorlessCube: true}); got != 15 {
		t.Fatal(got)
	}
	if got := battlePassSkillKind(dnfskill.Skill{Active: false, UsesColorlessCube: true}); got != 0 {
		t.Fatal("被动技能误计")
	}
}

func TestBattlePassAdaptedDungeonOnlyFinalSuccess(t *testing.T) {
	for _, row := range []struct {
		id   int64
		kind uint32
	}{{316, 24}, {5097, 8}, {3570, 8}, {7100, 44}, {7165, 44}, {7179, 44}, {3572, 0}, {5098, 0}, {2018, 0}, {310, 0}} {
		r := &runtimeDungeonState{}
		r.Dungeon.ID = row.id
		r.ordinaryFinalRoomClearAccepted = true
		if got := currentBattlePassAdaptedDungeonKind(r); got != row.kind {
			t.Fatal(row, got)
		}
		r.ordinaryFinalRoomClearAccepted = false
		if currentBattlePassAdaptedDungeonKind(r) != 0 {
			t.Fatal("未通关便计数")
		}
		r.ordinaryFinalRoomClearAccepted = true
		r.IgnoreDefaultDungeonClear = true
		if currentBattlePassAdaptedDungeonKind(r) != 0 {
			t.Fatal("独立玩法串普通通关")
		}
	}
}
