package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/town"
	"testing"
)

func TestEmpireLobbyIsPrivateOnLoginAndAreaMove(t *testing.T) {
	path := "Town/CollosseumofEmpire/CollosseumofEmpire.map"
	table, err := town.Load(context.Background(), townMoveTestSource{"town/town.lst": "48 `empire.twn`\n", "town/empire.twn": "[area]\n0 `" + path + "` `[gate]` 543 255\n[/area]\n"}, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{onlinePlayers: newOnlinePlayerManager(), townCatalog: table}
	for _, id := range []uint16{29, 60} {
		p := &gameSession{selectedCharacterID: id, channel: channelcatalog.Channel{ID: 187, Type: 39}}
		if !s.registerInitialTownPrivateAreaPresence(p, id, r.CharacterRecord{CharacterID: "owner"}, 48, 0, path, currentSceneTransitionRow{}, "empire_test") {
			t.Fatal("login omitted private presence")
		}
		local, ok := s.onlinePlayers.PlayerForCharacter(id)
		if !ok || local.Instance != uint32(id) {
			t.Fatal(local)
		}
		if got := s.onlinePlayers.GetAreaPlayers(48, 0, 187); len(got) != 0 {
			t.Fatal("shared lookup leaked empire owner", got)
		}
		if !s.townPrivateRoomPeerProjectionBlocked(p, onlinePlayerInfo{CharacterID: id + 1, TownID: 48, AreaID: 0}) {
			t.Fatal("stale shared party actor recreated in private lobby")
		}
	}
	for _, p := range []string{path, "map/Town/CollosseumofEmpire/CollosseumofEmpire.map", "Town\\CollosseumofEmpire\\CollosseumofEmpire.map"} {
		if townAreaInstanceForMapPath(p, 29) != 29 || townAreaInstanceForMapPath(p, 60) != 60 {
			t.Fatal("area move kept shared instance", p)
		}
	}
	if townMapPathLooksLikeSeria(path) {
		t.Fatal("empire privacy changed Seria portal semantics")
	}
	for _, p := range []string{"Town/CollosseumofEmpire/Other.map", "Town/Hendon.map"} {
		if townAreaInstanceForMapPath(p, 29) != 0 {
			t.Fatal("unrelated map became private", p)
		}
	}
}
