package dnfbridge

import "testing"

func TestRealPVFLukeTeamMaterialFilterRequiresOwnedMultiplayerBinding(t *testing.T) {
	f, session, _ := normalLukeFixture(t)
	r := reviewLukeRuntimeFor(t, session, 5054)
	if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, r, nil); err != nil {
		t.Fatal(err)
	}
	catalog, _ := actualAntonRewardFixture(t)
	item, err := catalog.ResolveItem(10159142)
	if err != nil {
		t.Fatal(err)
	}
	if !f.service.currentLukeTeamMonsterMaterialSuppressed(r, item) {
		t.Fatal("real Luke material not suppressed in multiplayer binding", item.StackableType)
	}
	e := r.raidEntry
	for _, kind := range []string{"normal", "single", "other_instance", "other_attempt", "other_channel", "equipment", "gold", "quest"} {
		old := *e
		oldSingle := f.state.Battle.Single
		changedItem := item
		switch kind {
		case "normal":
			r.raidEntry = nil
		case "single":
			f.state.Battle.Single = true
		case "other_instance":
			e.InstanceID = "other"
		case "other_attempt":
			e.Attempt++
		case "other_channel":
			e.ChannelID++
		case "equipment":
			changedItem.Kind = dungeonDropItemEquipment
		case "gold":
			changedItem.StackableType = ""
		case "quest":
			changedItem.StackableType = "[quest]"
		}
		if f.service.currentLukeTeamMonsterMaterialSuppressed(r, changedItem) {
			t.Fatal("material filter escaped owned team scope", kind)
		}
		*e = old
		r.raidEntry = e
		f.state.Battle.Single = oldSingle
	}
}
