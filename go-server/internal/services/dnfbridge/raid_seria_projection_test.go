package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

func TestOrdinaryChannelReconnectFromRaidUsesOrdinaryPrivateSeriaRoom(t *testing.T) {
	table, err := dnftown.Load(context.Background(), initialEquipmentMemSource{
		"town/town.lst":     "38 `ordinary.twn`\n",
		"town/ordinary.twn": "[area]\n1 `Cataclysm/Town/Elvengard/new_seria_room.map`\n`[gate]` 450 234\n[/area]\n",
	}, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, raidTown := range []int64{19, 32} {
		service, session, repos := pvpReturnTownFixture(t, table)
		original, _, err := repos.Character.Load(context.Background(), "29")
		if err != nil {
			t.Fatal(err)
		}
		original.Stats["town_id"], original.Stats["area_id"], original.Stats["pos_x"], original.Stats["pos_y"] = raidTown, 2, 670, 368
		if err := repos.Character.Save(context.Background(), original); err != nil {
			t.Fatal(err)
		}
		session.channel = channelcatalog.Channel{Type: 11}
		projected, town, area, row, path, err := service.currentChannelReconnectTownTransition(context.Background(), session, 29, original)
		if err != nil {
			t.Fatal(err)
		}
		if town != 38 || area != 1 || row.Value1 != 450 || row.Value2 != 234 || projected.Stats["town_id"] != 38 || original.Stats["town_id"] != raidTown || townAreaInstanceForMapPath(path, 29) != 29 {
			t.Fatalf("raid town %d retained in ordinary channel: town=%d area=%d row=%+v path=%s", raidTown, town, area, row, path)
		}
	}
}

func TestRegisteredRaidSeriaMapsArePrivateAndDungeonGatesRemainShared(t *testing.T) {
	for _, path := range []string{"Town/BattleShip/Gate_BattleShip.map", "Town/Jelva_raid/Gate_Jelva.map"} {
		if townAreaInstanceForMapPath(path, 6) != 6 || townAreaInstanceForMapPath(path, 4) != 4 {
			t.Fatalf("raid Seria room is shared: %s", path)
		}
	}
	for _, path := range []string{"Town/BattleShip/BattleShip_Anton.map", "Town/Jelva_raid/2_Jelva_Waiting.map"} {
		if townAreaInstanceForMapPath(path, 6) != 0 {
			t.Fatalf("raid dungeon gate is private: %s", path)
		}
	}
}
