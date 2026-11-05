package dnfbridge

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/progression"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingCapsuleExperiencePVFUseAndEmptySourceReplay(t *testing.T) {
	s, peers, repos := weddingProfileFixture(t)
	ctx := context.Background()
	source := currentExperienceItemTestPVF()
	const path = "stackable/event/chn_wedding_system_2014/chn_stk_couple_growth_capsule.stk"
	source["stackable/stackable.lst"] += "10002910 `event/chn_wedding_system_2014/chn_stk_couple_growth_capsule.stk`\n"
	source[path] = "[name]\n`爱情成长胶囊`\n[attach type]\n`[account]`\n[stackable type]\n`[etc]` 0\n[minimum level]\n30\n[maximum level]\n84\n[stack limit]\n1000\n[item group name]\n`potion`\n[increase status type]\n`[expUp]` 30000\n[/increase status type]\n"
	var exp, sp strings.Builder
	sp.WriteString("[sp table]\n")
	for level := 1; level <= 90; level++ {
		fmt.Fprintf(&exp, "%d ", level*1000000)
		fmt.Fprintf(&sp, "%d 30\n", level)
	}
	sp.WriteString("[/sp table]\n[tp table]\n50 1\n[/tp table]\n")
	source[progression.ExperienceTablePath] = exp.String()
	source[progression.SkillPointTablePath] = sp.String()
	source[progression.QuestParameterPath] = "[difficulty]\n`N` 100\n[/difficulty]\n[exp reward table]\n100 -1\n[gold reward table]\n10\n[green level penalty]\n80\n[grey level penalty]\n30\n"
	tables, e := progression.Load(ctx, source)
	if e != nil {
		t.Fatal(e)
	}
	catalog, e := newPVFDungeonDropCatalog(source)
	if e != nil {
		t.Fatal(e)
	}
	s.pvfItemCatalog = catalog
	s.questFinishResourceLoader = func(context.Context) (*progression.Tables, *pvfDungeonDropCatalog, error) {
		return tables, catalog, nil
	}
	s.wedding.extensionRules = &wedding.ExtensionRules{Capsule: wedding.CapsuleRules{ItemID: 10002910, MaximumOwn: 10, MaximumTaken: 10, Gain: 2, MaximumGauge: 10}}
	definition, e := s.resolveCurrentWeddingCapsuleExperience(10002910)
	if e != nil {
		t.Fatal(e)
	}
	if definition.Amount != 30000 || definition.MinimumLevel != 30 || definition.MaximumUseLevel != 84 {
		t.Fatal(definition)
	}
	slots := definition.Slots.EmptySlots()
	if len(slots) == 0 {
		t.Fatal("capsule has no real PVF target slots")
	}
	slot := slots[0]
	id := fmt.Sprint(peers[0].selectedCharacterID)
	c, _, _ := repos.Character.Load(ctx, id)
	c.Level = 30
	c.Stats["exp"] = 100
	if e = r.SaveCharacterFields(ctx, repos.Character, c, r.CharacterFieldBase, r.CharacterFieldStats); e != nil {
		t.Fatal(e)
	}
	if e = repos.Skill.Save(ctx, r.SkillRecord{CharacterID: id, Points: r.SkillPointState{SyncedLevel: 30}}); e != nil {
		t.Fatal(e)
	}
	if e = repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{experienceItemMainSlotKey(slot): {ItemID: 10002910, Count: 1}}}); e != nil {
		t.Fatal(e)
	}
	sequence := uint64(10)
	request := gameplayRequest{Opcode: 30, Body: currentExperienceItemTestBody(slot), Sequence: &sequence}
	if e = currentWeddingCapsuleUseOrExperienceItemHandler(s, peers[0], request); e != nil {
		t.Fatal(e)
	}
	c, _, _ = repos.Character.Load(ctx, id)
	bag, _, _ := repos.Inventory.Load(ctx, id)
	if c.Stats["exp"] != 30100 || bag.Slots[experienceItemMainSlotKey(slot)].Count != 0 {
		t.Fatalf("capsule not applied %+v %+v", c, bag)
	}
	if e = currentWeddingCapsuleUseOrExperienceItemHandler(s, peers[0], request); e != nil {
		t.Fatal(e)
	}
	c, _, _ = repos.Character.Load(ctx, id)
	if c.Stats["exp"] != 30100 {
		t.Fatal("empty-source replay granted experience twice")
	}
}
