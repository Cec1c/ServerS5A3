package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
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
	service := &Service{townCatalog: table}
	for _, raidTown := range []int64{19, 32} {
		original := dnfrepo.CharacterRecord{Stats: map[string]int64{"town_id": raidTown, "area_id": 2, "pos_x": 670, "pos_y": 368}}
		projected, town, area, row, path, err := service.currentChannelReconnectTownTransition(context.Background(), &gameSession{channel: channelcatalog.Channel{Type: 11}}, 6, original)
		if err != nil {
			t.Fatal(err)
		}
		if town != 38 || area != 1 || row.Value1 != 450 || row.Value2 != 234 || projected.Stats["town_id"] != 38 || original.Stats["town_id"] != raidTown || townAreaInstanceForMapPath(path, 6) != 6 {
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
