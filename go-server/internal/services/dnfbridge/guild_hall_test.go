package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// The fixture uses the paired PVF's guild town/map identities. Coordinates are
// test inputs, not a replacement for runtime PVF landing validation.
func guildHallFixture(t *testing.T) (*Service, map[uint16]*gameSession, dnfrepo.Group) {
	t.Helper()
	s, original, repos := newTownMoveTest(t)
	ctx := context.Background()
	table, err := dnftown.Load(ctx, townMoveTestSource{
		"town/town.lst":          "8 `GuildAgit.twn` 38 `new_Elvengard.twn`",
		"town/GuildAgit.twn":     "[area]\n0 `Guildagit/GuildAgit_W.map` `[normal]`\n[/area]\n[area]\n1 `Guildagit/GuildAgit_1.map` `[dungeon gate]` 37\n[/area]\n[name]\n`公会大厅`",
		"town/new_Elvengard.twn": "[area]\n0 `new_Elvengard.map` `[normal]`\n[/area]\n[area]\n1 `new_seria_room.map` `[gate]` 450 234\n[/area]\n[name]\n`Elvengard`",
	}, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog = table
	s.onlinePlayers = newOnlinePlayerManager()
	s.gameSessions = make(map[uint16]*gameSession)
	s.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	s.options.currentClientVersion = "official-24.9"
	base, _, _ := repos.Character.Load(ctx, "29")
	reg := dnfguild.NewRegistry()
	var first *dnfguild.Guild
	for id := uint16(29); id <= 33; id++ {
		c := dnfrepo.CloneCharacter(base)
		c.CharacterID, c.AccountID, c.Name = fmt.Sprint(id), fmt.Sprintf("hall:%d", id), fmt.Sprintf("角色%d", id)
		c.Stats["town_id"], c.Stats["area_id"] = 8, 0
		c.Stats["pos_x"], c.Stats["pos_y"] = 500, 220
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		enableTownMoveSkillProjection(t, s, repos, c.CharacterID)
		p := &gameSession{conn: &bufferConn{}, accountID: c.AccountID, selectedCharacterID: id,
			characterGeneration: uint64(id), channel: original.channel, residentChannel: original.channel,
			townSceneReadyCharacterID: id, connectionTownActorOwnerChannel: byte(original.channel.ID),
			townActorOwnerChannel: byte(original.channel.ID),
			townPositionSnapshot:  currentTownPositionSnapshot{TownID: 8, AreaID: 0, PositionX: 500, PositionY: 220, MovementCode: 5, PositionValid: true},
		}
		s.gameSessions[id] = p
		switch id {
		case 29:
			first, err = reg.Create(c, "一号公会", s.gameplayNow())
		case 30, 33:
			_, err = reg.JoinDirect(first.ID, c, s.gameplayNow())
		case 31:
			_, err = reg.Create(c, "二号公会", s.gameplayNow())
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	s.gameSessions[33].channel.ID++
	if err := dnfguild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	return s, s.gameSessions, repos
}

func TestGuildHallPresenceIsScopedOnLoginAndDungeonReturn(t *testing.T) {
	for _, route := range []string{"login_reconnect", "dungeon_return"} {
		t.Run(route, func(t *testing.T) {
			s, peers, _ := guildHallFixture(t)
			for _, id := range []uint16{29, 31, 30, 32, 33} {
				if route == "dungeon_return" {
					s.rebindTownCopresenceAfterDungeonReturn(peers[id], 8, 0, "guild_hall_test")
				} else {
					s.publishTownPresenceInArea(peers[id], 8, 0, 500, 220, 5, 3, "guild_hall_test")
				}
			}
			if !s.onlinePlayers.PeerInSameArea(29, 30) {
				t.Error("same guild members cannot meet")
			}
			if s.onlinePlayers.PeerInSameArea(29, 31) {
				t.Error("different guilds share the hall")
			}
			if s.onlinePlayers.PeerInSameArea(29, 33) {
				t.Error("different channels share the hall")
			}
			if _, found := s.onlinePlayers.PlayerForCharacter(32); found {
				t.Error("nonmember published in guild hall")
			}
			if shared := s.onlinePlayers.GetAreaPlayers(8, 0, peers[29].channel.ID); len(shared) != 0 {
				t.Errorf("guild hall leaked %d actors into public instance", len(shared))
			}
		})
	}
}

func TestGuildHallTransitionRejectsNonmemberWithoutChangingLocation(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	p := peers[32]
	before, _, _ := repos.Character.Load(context.Background(), "32")
	if err := s.handleTownSetUserArea(p, buildTownMoveRequest(8, 1, 510, 230, 5)); err != nil {
		t.Fatal(err)
	}
	after, _, _ := repos.Character.Load(context.Background(), "32")
	if after.Stats["area_id"] != before.Stats["area_id"] || after.Stats["pos_x"] != before.Stats["pos_x"] {
		t.Fatal("nonmember moved into guild area")
	}
	if _, found := s.onlinePlayers.PlayerForCharacter(32); found {
		t.Fatal("nonmember registered in guild area")
	}
}

func TestGuildHallTransitionRosterContainsOnlyOwnGuild(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	for _, id := range []uint16{30, 31} {
		s.publishTownPresenceInArea(peers[id], 8, 0, 500, 220, 5, 3, "guild_hall_test")
	}
	p := peers[29]
	if err := s.handleTownSetUserArea(p, buildTownMoveRequest(8, 0, 510, 230, 5)); err != nil {
		t.Fatal(err)
	}
	want, err := buildCurrentSceneTransitionBody(8, 0, []currentSceneTransitionRow{
		{ObjectOrResourceKey: 30, Value1: 500, Value2: 220, Value3: 5, Value4: 3},
		{ObjectOrResourceKey: 29, Value1: 510, Value2: 230, Value3: 5, Value4: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for wire := p.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if packet.Header.MsgID == currentSceneTransitionMsgID && packet.Header.Classification == 0 {
			found = true
			if !bytes.Equal(packet.Body, want) {
				t.Errorf("transition includes wrong guild roster: got=%x want=%x", packet.Body, want)
			}
		}
	}
	if !found {
		t.Fatal("missing guild hall transition")
	}
	if s.onlinePlayers.PeerInSameArea(29, 31) {
		t.Fatal("transition published into other guild")
	}
}

func TestGuildHallLoginRedirectsFormerMemberToPVFSafeLocation(t *testing.T) {
	for _, preserve := range []string{"", "preserve"} {
		t.Run("fallback_"+preserve, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_LOGIN_PERSISTED_TOWN", "keep")
			t.Setenv("DNFBRIDGE_LOGIN_FALLBACK_KEEPS_TOWN", preserve)
			s, peers, repos := guildHallFixture(t)
			c, _, _ := repos.Character.Load(context.Background(), "32")
			_, town, area, row, _, err := s.currentCharacterListLoginTransition(context.Background(), peers[32], 32, c)
			if err != nil {
				t.Fatal(err)
			}
			if town != 38 || area != 1 || row.Value1 != 450 || row.Value2 != 234 {
				t.Fatalf("invalid guild login retained: %d/%d %v", town, area, row)
			}
			stored, _, _ := repos.Character.Load(context.Background(), "32")
			if stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 1 {
				t.Fatal("invalid guild location left persistent")
			}
			if peers[32].townSeriaRoomEntrySnapshot.PositionValid && peers[32].townSeriaRoomEntrySnapshot.TownID == 8 {
				t.Fatal("Seria return origin points at inaccessible guild hall")
			}
		})
	}
}

func TestGuildHallPartyFallbackCannotRecreateAnotherGuildActor(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	for _, id := range []uint16{29, 31} {
		s.publishTownPresenceInArea(peers[id], 8, 0, 500, 220, 5, 3, "test")
	}
	peers[29].conn.(*bufferConn).write.Reset()
	state := alignedcmd.PartyState{PartyID: 1, Members: []alignedcmd.PartyMemberState{{UserID: 29}, {UserID: 31}}}
	if err := s.sendTownRemotePartyActors(peers[29], state); err != nil {
		t.Fatal(err)
	}
	if peers[29].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("party fallback sent another guild's hall actor")
	}
}

func TestGuildHallDefaultLoginDiscardsFormerMemberReturnOrigin(t *testing.T) {
	for _, persisted := range []bool{false, true} {
		t.Run(fmt.Sprint(persisted), func(t *testing.T) {
			t.Setenv("DNFBRIDGE_LOGIN_PERSISTED_TOWN", "")
			s, peers, repos := guildHallFixture(t)
			p := peers[32]
			c, _, _ := repos.Character.Load(context.Background(), "32")
			origin := currentTownPositionSnapshot{CharacterID: 32, TownID: 8, AreaID: 0, PositionX: 500, PositionY: 220, MovementCode: 5, PositionValid: true}
			currentTownPrevVillageApplyOriginStats(c.Stats, origin)
			p.townSeriaRoomEntrySnapshot, p.townPrevVillageSnapshot = origin, origin
			if persisted {
				c.Stats["town_id"], c.Stats["area_id"] = 38, 1
			}
			if err := repos.Character.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			_, town, area, _, _, err := s.currentCharacterListLoginTransition(context.Background(), p, 32, c)
			if err != nil {
				t.Fatal(err)
			}
			if town != 38 || area != 1 {
				t.Fatalf("login=%d/%d", town, area)
			}
			if p.townSeriaRoomEntrySnapshot.PositionValid || p.townPrevVillageSnapshot.PositionValid {
				t.Fatal("former member retains hall return origin after default login")
			}
			stored, _, _ := repos.Character.Load(context.Background(), "32")
			if stored.Stats[currentTownPrevVillageTownStat] == 8 {
				t.Fatal("stale hall return origin remains persistent")
			}
		})
	}
}

func TestGuildHallSeriaExitRevalidatesMembership(t *testing.T) {
	for _, mode := range []string{"entry_town", "prev_village"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(currentTownSeriaExitDoorEnv, mode)
			s, peers, repos := guildHallFixture(t)
			p := peers[32]
			c, _, _ := repos.Character.Load(context.Background(), "32")
			c.Stats["town_id"], c.Stats["area_id"] = 38, 1
			if err := repos.Character.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			origin := currentTownPositionSnapshot{CharacterID: 32, TownID: 8, AreaID: 0, PositionX: 500, PositionY: 220, MovementCode: 5, PositionValid: true}
			p.townSeriaRoomEntrySnapshot, p.townPrevVillageSnapshot = origin, origin
			s.seriaRoomDoors = map[currentSeriaRoomDoorKey]currentSeriaRoomDoorEntry{
				{TownID: 38, AreaID: 1, EntryTownID: -1, EntryAreaID: -1}: {Found: true, Door: currentSeriaRoomDoor{TownID: 38, AreaID: 0, PositionX: 622, PositionY: 196}},
			}
			if err := s.handleCurrentPrevVillage(p, nil); err != nil {
				t.Fatal(err)
			}
			stored, _, _ := repos.Character.Load(context.Background(), "32")
			if stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 0 {
				t.Fatalf("former member returned to %v", stored.Stats)
			}
			packet, _ := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if packet.Header.MsgID != currentSceneTransitionMsgID || !bytes.Equal(packet.Body[:2], []byte{38, 0}) {
				t.Fatalf("wrong return response: %x", packet.Body)
			}
		})
	}
}

type guildHallFailedSettings struct{ dnfrepo.SettingsRepository }

func (guildHallFailedSettings) Load(context.Context, string) (dnfrepo.SettingsRecord, bool, error) {
	return dnfrepo.SettingsRecord{}, false, errors.New("registry unavailable")
}

func TestGuildHallCannotBypassIdentityOrRegistryChecks(t *testing.T) {
	for _, mode := range []string{"account", "replaced_session", "registry_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos := guildHallFixture(t)
			p := peers[29]
			switch mode {
			case "account":
				p.accountID = "not-owner"
			case "replaced_session":
				s.gameSessions[29] = &gameSession{selectedCharacterID: 29, characterGeneration: 300}
			case "registry_failure":
				repos.Settings = guildHallFailedSettings{repos.Settings}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			}
			if err := s.handleTownSetUserArea(p, buildTownMoveRequest(8, 1, 510, 230, 5)); err != nil {
				t.Fatal(err)
			}
			c, _, _ := repos.Character.Load(context.Background(), "29")
			if c.Stats["area_id"] != 0 {
				t.Fatal("failed guild identity moved")
			}
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if packet.Header.MsgID != 36 || !bytes.Equal(packet.Body, []byte{0, 1, 8, 1}) || len(rest) != 0 {
				t.Fatalf("native rejection shape: %+v %x rest=%x", packet.Header, packet.Body, rest)
			}
		})
	}
}

func TestGuildHallMoveWriteFailurePreservesLocation(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			s, peers, repos := guildHallFixture(t)
			wantErr := errors.New("hall transition write failed")
			peers[29].conn = &failNthDungeonWriteConn{failAt: failAt, err: wantErr}
			if err := s.handleTownSetUserArea(peers[29], buildTownMoveRequest(8, 1, 510, 230, 5)); !errors.Is(err, wantErr) {
				t.Fatalf("write error=%v", err)
			}
			c, _, _ := repos.Character.Load(context.Background(), "29")
			if c.Stats["area_id"] != 0 || c.Stats["pos_x"] != 500 || c.Stats["pos_y"] != 220 {
				t.Fatal("failed write kept new hall location")
			}
			if _, found := s.onlinePlayers.PlayerForCharacter(29); found {
				t.Fatal("failed transition published new presence")
			}
		})
	}
}

func TestGuildHallMembershipLossRetiresOldPresence(t *testing.T) {
	for _, route := range []string{"publish", "return", "move"} {
		t.Run(route, func(t *testing.T) {
			s, peers, repos := guildHallFixture(t)
			for _, id := range []uint16{29, 30} {
				s.publishTownPresenceInArea(peers[id], 8, 0, 500, 220, 5, 3, "before")
			}
			reg, err := dnfguild.Load(context.Background(), repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			g, _, _ := reg.GuildForCharacter("30")
			delete(g.Members, "30")
			if err := dnfguild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			peers[29].conn.(*bufferConn).write.Reset()
			switch route {
			case "publish":
				s.publishTownPresenceInArea(peers[30], 8, 0, 500, 220, 5, 3, "after")
			case "return":
				s.rebindTownCopresenceAfterDungeonReturn(peers[30], 8, 0, "after")
			case "move":
				if err := s.handleTownSetUserArea(peers[30], buildTownMoveRequest(8, 0, 510, 220, 5)); err != nil {
					t.Fatal(err)
				}
			}
			if _, found := s.onlinePlayers.PlayerForCharacter(30); found {
				t.Fatal("former member remains in hall roster")
			}
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, peers[29].conn.(*bufferConn).write.Bytes())
			if packet.Header.MsgID != currentTownUserLeaveMsgID || len(rest) != 0 {
				t.Fatal("old peer did not remove former member")
			}
		})
	}
}

func TestGuildHallNormalChatStaysWithinGuildAndChannel(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	for _, id := range []uint16{29, 30, 31, 33} {
		s.publishTownPresenceInArea(peers[id], 8, 0, 500, 220, 5, 3, "chat")
	}
	for _, p := range peers {
		p.conn.(*bufferConn).write.Reset()
	}
	if err := s.handleCurrentAreaChat(peers[29], 17, currentPeerChatRequest{MessageType: currentAreaChatMessageType, Message: []byte("hall chat")}); err != nil {
		t.Fatal(err)
	}
	if peers[30].conn.(*bufferConn).write.Len() == 0 {
		t.Fatal("same guild did not receive hall chat")
	}
	for _, id := range []uint16{31, 32, 33} {
		if peers[id].conn.(*bufferConn).write.Len() != 0 {
			t.Fatalf("hall chat leaked to %d", id)
		}
	}
}

func TestRealPVFGuildHallOwnershipAndCatalog(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the paired Script.pvf")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s, peers, repos := guildHallFixture(t)
	s.townCatalog = table
	member, _, _ := repos.Character.Load(context.Background(), "29")
	nonmember, _, _ := repos.Character.Load(context.Background(), "32")
	count := 0
	for _, town := range table.Towns() {
		if town.Path != "town/GuildAgit.twn" {
			continue
		}
		for _, area := range town.Areas {
			count++
			if !townMapPathLooksLikeGuildHall(area.MapPath) {
				t.Fatalf("unrecognized hall map %s", area.MapPath)
			}
			if id, err := s.currentTownAreaInstance(context.Background(), peers[29], member, area.MapPath); err != nil || id == 0 {
				t.Fatalf("member scope=%d err=%v", id, err)
			}
			if _, err := s.currentTownAreaInstance(context.Background(), peers[32], nonmember, area.MapPath); !errors.Is(err, dnfguild.ErrNotMember) {
				t.Fatalf("nonmember admission=%v", err)
			}
		}
	}
	if count == 0 {
		t.Fatal("paired guild hall not found in town catalog")
	}
	t.Logf("verified %d runtime PVF guild areas", count)
	// Exercise the existing source-owned op36 portal shape through the paired
	// catalog in both directions. Native entrance UI emission is a separate
	// acceptance check; no new transport marker is invented for this test.
	member.Stats["town_id"], member.Stats["area_id"] = 38, 0
	if err := repos.Character.Save(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	p := peers[29]
	for _, route := range []struct{ from, town, area byte }{{38, 8, 0}, {8, 8, 1}, {8, 38, 0}} {
		body := buildTownMoveRequest(route.town, route.area, 500, 220, 5)
		if route.from != route.town {
			body = buildTownCrossTownPortalRequest(route.town, route.area, 500, 220, 5, uint16(route.from))
		}
		if err := s.handleTownSetUserArea(p, body); err != nil {
			t.Fatal(err)
		}
		stored, _, _ := repos.Character.Load(context.Background(), "29")
		if stored.Stats["town_id"] != int64(route.town) || stored.Stats["area_id"] != int64(route.area) {
			t.Fatalf("route %+v rejected: %v", route, stored.Stats)
		}
		presence, found := s.onlinePlayers.PlayerForCharacter(29)
		if !found || (route.town == 8 && presence.Instance == 0) || (route.town == 38 && presence.Instance != 0) {
			t.Fatalf("route %+v has wrong presence: %+v", route, presence)
		}
	}
}
