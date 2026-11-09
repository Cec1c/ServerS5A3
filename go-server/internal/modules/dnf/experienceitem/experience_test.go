package experienceitem

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/progression"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type experienceConsumableSource map[string]string

func (s experienceConsumableSource) ReadText(path string) (string, error) {
	for k, v := range s {
		if strings.EqualFold(k, path) {
			return v, nil
		}
	}
	return "", fmt.Errorf("missing %s", path)
}
func experienceConsumableFixture(t *testing.T) (testFixture, ExperienceCommand) {
	t.Helper()
	f := newTestFixture(t, map[string]r.ItemStack{"0:65": {ItemID: 10002910, Count: 2, Extra: map[string]string{"identity": "kept"}, RawEntry: []byte{1}}}, map[string]int64{"exp": 90}, r.SkillPointState{SyncedLevel: 1}, true)
	c, _, _ := f.repositories.Character.Load(context.Background(), testCharacterID)
	c.Level = 1
	if e := f.repositories.Character.Save(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	tables, e := progression.Load(context.Background(), experienceConsumableSource{
		progression.ExperienceTablePath: "100 250 500 900\n",
		progression.SkillPointTablePath: "[sp table]\n1 0\n2 30\n3 30\n4 40\n5 50\n[/sp table]\n[tp table]\n3 2\n4 1\n5 1\n[/tp table]\n",
		progression.QuestParameterPath:  "[difficulty]\n`N` 100\n[/difficulty]\n[exp reward table]\n100 -1\n200 -1\n300 -1\n400 -1\n500 -1\n[gold reward table]\n10\n[green level penalty]\n80\n[grey level penalty]\n30\n",
	})
	if e != nil {
		t.Fatal(e)
	}
	cmd := ExperienceCommand{AccountID: testAccountID, CharacterID: testCharacterID, Slot: 65, Definition: ExperienceConsumable{ItemID: 10002910, Amount: 130, MinimumLevel: 1, MaximumUseLevel: 4, StackLimit: 1000, PVFPath: "stackable/capsule.stk", Slots: itemplacement.SlotPolicy{ListType: r.MainInventoryListType, Start: 65, End: 120}}, Tables: tables, PlayableCap: 5, Now: time.Unix(1793600000, 0), Session: strings.Repeat("ab", 32), Sequence: 1, LevelStats: func(r.CharacterRecord, int) (map[string]int64, error) { return map[string]int64{"strength": 123}, nil }}
	return f, cmd
}
func TestExperienceConsumableAtomicLevelSPAndReplay(t *testing.T) {
	f, cmd := experienceConsumableFixture(t)
	ctx := context.Background()
	first, e := f.owner.UseExperience(ctx, cmd)
	if e != nil || first.Character.Level != 2 || first.Character.Stats["exp"] != 220 || first.Points.RemainingSP != 30 || first.Remaining != 1 || first.Character.Stats["strength"] != 123 {
		t.Fatalf("use %+v %v", first, e)
	}
	replay, e := f.owner.UseExperience(ctx, cmd)
	if e != nil || !replay.Replayed || replay.Remaining != 1 || replay.Character.Stats["exp"] != 220 {
		t.Fatalf("replay %+v %v", replay, e)
	}
	bag, _, _ := f.repositories.Inventory.Load(ctx, testCharacterID)
	if bag.Slots["0:65"].Extra["identity"] != "kept" || bag.Slots["0:65"].RawEntry != nil {
		t.Fatal("source identity lost")
	}
}
func TestExperienceConsumableRetiredBindingRollsBackAllWrites(t *testing.T) {
	f, cmd := experienceConsumableFixture(t)
	ctx := context.Background()
	beforeBag, beforeCharacter, beforeSkill := f.load(t)
	calls := 0
	cmd.Revalidate = func() bool { calls++; return calls == 1 }
	if _, e := f.owner.UseExperience(ctx, cmd); e == nil {
		t.Fatal("retired binding committed")
	}
	a, b, c := f.load(t)
	if !reflect.DeepEqual(a, beforeBag) || !reflect.DeepEqual(b, beforeCharacter) || !reflect.DeepEqual(c, beforeSkill) {
		t.Fatal("partial experience consumption")
	}
	_ = ctx
}
func TestExperienceConsumableRejectsUseLevelLockExpiryAndClosedSlot(t *testing.T) {
	for _, kind := range []string{"level", "locked", "expired", "closed"} {
		t.Run(kind, func(t *testing.T) {
			f, cmd := experienceConsumableFixture(t)
			ctx := context.Background()
			bag, _, _ := f.repositories.Inventory.Load(ctx, testCharacterID)
			stack := bag.Slots["0:65"]
			switch kind {
			case "level":
				cmd.Definition.MinimumLevel = 2
			case "locked":
				stack.Extra["equipment_lock_state"] = "locked"
			case "expired":
				stack.ExpireAt = cmd.Now
			case "closed":
				cmd.Slot = 120
				delete(bag.Slots, "0:65")
				cmd.Definition.Slots.Start, cmd.Definition.Slots.End = 120, 120
			}
			bag.Slots[slotKey(0, cmd.Slot)] = stack
			if e := f.repositories.Inventory.Save(ctx, bag); e != nil {
				t.Fatal(e)
			}
			before, _, _ := f.repositories.Inventory.Load(ctx, testCharacterID)
			if _, e := f.owner.UseExperience(ctx, cmd); e == nil {
				t.Fatal("invalid asset accepted")
			}
			after, _, _ := f.repositories.Inventory.Load(ctx, testCharacterID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected asset debited")
			}
		})
	}
}
