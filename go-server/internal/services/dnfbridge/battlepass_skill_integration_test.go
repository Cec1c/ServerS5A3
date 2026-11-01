package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	"testing"
)

func TestBattlePassSkillNativeIdentityLearnedAndReplay(t *testing.T) {
	s, p, _, _ := playerStatsGameFixture(t)
	g, _ := s.repositoryGroup()
	_, dp, _, _ := prepareDungeonDeathBurstTest(t, true)
	ch, found, err := g.Character.Load(nil, "2")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	ch.Job = "0"
	ch.Level = 90
	if err := g.Character.Save(nil, ch); err != nil {
		t.Fatal(err)
	}
	r := dp.dungeon.runtime
	r.Character = ch
	r.AccountID = "a"
	p.dungeon.runtime = r
	source := finishBridgePVFSource{dnfskill.DefaultList: "0 `SwordmanSkill.lst`", "skill/SwordmanSkill.lst": "190 `Swordman/QuickStanding.skl` 81 `Swordman/OutRageBreak.skl`", "skill/Swordman/QuickStanding.skl": "[name]\n`受身蹲伏`\n[type]\n`[active]`\n[required level]\n1", "skill/Swordman/OutRageBreak.skl": "[name]\n`崩山裂地斩`\n[type]\n`[active]`\n[required level]\n45\n[consume item]\n3037 2 2\n[/consume item]"}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfskill.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	s.skillCatalog, err = dnfskill.Load(context.Background(), index, dnfskill.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec := repo.SkillRecord{CharacterID: "2", Skills: map[int64]repo.SkillState{190: {Level: 1, Enabled: true}, 81: {Level: 1, Enabled: true}}}
	if err := g.Skill.Save(nil, rec); err != nil {
		t.Fatal(err)
	}
	call := func(id uint16, seq uint64, class byte) {
		t.Helper()
		body := make([]byte, 10)
		binary.LittleEndian.PutUint16(body[1:], id)
		binary.LittleEndian.PutUint16(body[8:], 1)
		playerStatsEdit(t, s, p, func() {
			// Native dispatch observes the wire sequence before invoking handlers.
			serial := p.guild.purchaseSequence.observe(uint16(seq))
			if err := s.handleCurrentBattlePassUseSkill(p, gameplayRequest{Opcode: 38, Classification: class, Body: body, Sequence: &serial}); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := g.AccountInventory.Save(context.Background(), repo.AccountInventoryRecord{AccountID: "a", Slots: map[string]repo.ItemStack{"0:358": {ItemID: 3037, Count: 50}}}); err != nil {
		t.Fatal(err)
	}
	call(190, 1, proto.DefaultChannelClassification)
	call(190, 1, proto.DefaultChannelClassification)
	for seq := uint64(2); seq <= 6; seq++ {
		call(81, seq, proto.DefaultChannelClassification)
	}
	call(81, 6, proto.DefaultChannelClassification)
	call(81, 7, proto.DefaultChannelClassification+1)
	rec.Skills[81] = repo.SkillState{Level: 0}
	if err := g.Skill.Save(nil, rec); err != nil {
		t.Fatal(err)
	}
	call(81, 8, proto.DefaultChannelClassification)
	cube, _, _ := g.AccountInventory.Load(context.Background(), "a")
	if cube.Slots["0:358"].Count != 40 {
		t.Fatal("技能无色未按实际个数扣除", cube)
	}
	activity, err := g.Activity.Summary(context.Background(), repo.ActivityQuery{AccountID: "a"})
	if err != nil || activity.Metrics[repo.ActivityColorless] != 10 {
		t.Fatal(activity, err)
	}
	ch, _, _ = g.Character.Load(nil, "2")
	day := repo.ActivityDay(s.gameplayNow())
	if ch.Stats[fmt.Sprintf("%s%s:4:190", repo.BattlePassCounterPrefix, day)] != 1 || ch.Stats[fmt.Sprintf("%s%s:15:3037", repo.BattlePassCounterPrefix, day)] != 5 {
		t.Fatal("原生技能重放、分类或未学技能误计数", ch.Stats)
	}
}
