package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestRealPVFRaidChannelUsesItsRegisteredSeriaRoom(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{townCatalog: table, seriaRoomDoorMapReader: func(area dnftown.Area) (worldmap.Map, bool) { return currentTownAreaMapDocument(archive, area) }}
	original := dnfrepo.CharacterRecord{CharacterID: "29", Stats: map[string]int64{"town_id": 38, "area_id": 1, "pos_x": 500, "pos_y": 300, "direction": 1, "area_state": 0}}
	for _, kind := range []int{int(dnfenum.RaidChannelType), int(dnfenum.HiddenRaidType)} {
		session := &gameSession{channel: channelcatalog.Channel{Type: byte(kind)}}
		projected, town, area, row, mapPath, err := service.currentRaidEntranceTownTransition(session, 29, original)
		if err != nil {
			t.Fatal(err)
		}
		wantTown := byte(19)
		if kind == int(dnfenum.HiddenRaidType) {
			wantTown = 32
		}
		if town != wantTown || area != 0 || row.Value1 != 474 || row.Value2 != 234 || !townMapPathLooksLikeSeria(mapPath) || projected.Stats["town_id"] != int64(wantTown) || original.Stats["town_id"] != 38 {
			t.Fatalf("raid %d: town=%d area=%d row=%+v", kind, town, area, row)
		}
		t.Logf("raid=%d town=%d area=%d spawn=(%d,%d) map=%s", kind, town, area, row.Value1, row.Value2, mapPath)
	}
}
