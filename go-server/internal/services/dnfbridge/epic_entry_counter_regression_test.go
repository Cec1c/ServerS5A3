package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
	"time"
)

func TestEpicBenefitsActualPreparedEntryCounterIsInvitationConsumption(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	actual, _, _, _ := prepareMonthlyBridge(t)
	ctx := context.Background()
	table, e := worldmap.LoadSource(ctx, actual.initialEquipmentArchive, worldmap.Options{})
	if e != nil {
		t.Fatal(e)
	}
	resolver, e := worldmap.NewResolver(table)
	if e != nil {
		t.Fatal(e)
	}
	monsters, e := newPVFDungeonMonsterCatalog(actual.initialEquipmentArchive)
	if e != nil {
		t.Fatal(e)
	}
	ai, e := newPVFDungeonAICharacterCatalog(actual.initialEquipmentArchive)
	if e != nil {
		t.Fatal(e)
	}
	for i, id := range []uint32{6063, 6064, 6065} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, a, old := newBackToVillageRuntime(t)
			if e := old.Session.Abandon(); e != nil {
				t.Fatal(e)
			}
			g, _ := s.repositoryGroup()
			c, _, e := g.Character.Load(ctx, "99")
			if e != nil {
				t.Fatal(e)
			}
			c.Level = 90
			c.Stats["town_id"] = 50
			c.Stats["area_id"] = 0
			c.Stats["pos_x"] = 441
			c.Stats["pos_y"] = 207
			if e = g.Character.Save(ctx, c); e != nil {
				t.Fatal(e)
			}
			need := int64(30 + i*15)
			if e = g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{"0:1": {ItemID: 3330, Count: need}, "0:2": {ItemID: 3329, Count: 7}}}); e != nil {
				t.Fatal(e)
			}
			s.options.currentClientVersion = "official-24.9"
			s.worldMapTable = table
			s.worldMapResolver = resolver
			s.dungeonMonsterTable = monsters
			s.dungeonAICharacterTable = ai
			s.initialEquipmentArchive = actual.initialEquipmentArchive
			s.bindGameSessionCharacter(a, 99)
			bindDungeonSelectorOriginForTestAt(t, s, a, 50, 0, 441, 207)
			runtime, _, e := s.prepareDungeonRuntime(ctx, a, dungeoncmd.SelectDungeonRequest{DungeonID: id})
			if e != nil {
				t.Fatal("actual epic prepare", e)
			}
			v, _, _ := g.Inventory.Load(ctx, "99")
			if v.Slots["0:1"].Count != need {
				t.Fatal("prepare charged", v.Slots["0:1"].Count)
			}
			before, _, _ := g.Character.Load(ctx, "99")
			if e = freezeCurrentDungeonTownReturnOrigin(a, runtime); e != nil {
				t.Fatal(e)
			}
			if e = s.commitDungeonRuntime(a, runtime); e != nil {
				t.Fatal("commit", e)
			}
			after, _, _ := g.Character.Load(ctx, "99")
			day := s.gameplayNow().In(onlineevent.ChinaCalendar).Add(-6 * time.Hour)
			anchor := time.Date(day.Year(), day.Month(), day.Day(), 6, 0, 0, 0, onlineevent.ChinaCalendar)
			key := "event_epic_road_v1:" + anchor.UTC().Format("20060102T150405Z") + ":invitations"
			if after.Stats[key] != need {
				t.Fatalf("successful paid entry counter=%d want actual invitation consumption=%d", after.Stats[key], need)
			}
			v, _, _ = g.Inventory.Load(ctx, "99")
			if a.dungeon.runtime != runtime || v.Slots["0:1"].Count != 0 || v.Slots["0:2"].Count != 7 || after.Stats["fatigue"] != before.Stats["fatigue"]-1 || after.Stats["epic_entry_sequence"] != 1 {
				t.Fatalf("actual atomic admission before%v after%v bag%v", before.Stats, after.Stats, v.Slots)
			}
			if e = s.commitDungeonRuntime(a, runtime); e == nil {
				t.Fatal("repeat active admission accepted")
			}
			final, _, _ := g.Character.Load(ctx, "99")
			if final.Stats["fatigue"] != after.Stats["fatigue"] || final.Stats["epic_entry_sequence"] != 1 {
				t.Fatal("repeat charged")
			}
		})
	}
}
