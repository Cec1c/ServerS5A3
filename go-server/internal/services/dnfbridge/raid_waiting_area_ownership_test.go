package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func antonWaitingAreaFixture(t *testing.T) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	s, session, repos := newTownMoveTest(t)
	s.options.currentClientVersion = "official-24.9"
	session.channel = channelcatalog.Channel{ID: 23, Type: 23}
	session.accountID = "dnf:1"
	session.loginPacketSeen = true
	session.accountFromToken = true
	s.bindGameSessionCharacter(session, 29)
	c, _, _ := repos.Character.Load(context.Background(), "29")
	c.Stats["town_id"] = 19
	c.Stats["area_id"] = 1
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), townMoveTestSource{
		"town/town.lst":            "19 `BattleShip_Raid.twn`",
		"town/BattleShip_Raid.twn": "[area]\n0 `Town/BattleShip/Gate_BattleShip.map` `[gate]` 474 234\n[/area]\n[area]\n1 `Town/BattleShip/BattleShip_Main.map` `[normal]`\n[/area]\n[area]\n2 `Town/BattleShip/BattleShip_Anton.map` `[dungeon gate]` 27\n[/area]\n[name]\n`Anton`",
	}, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog = table
	s.onlinePlayers = newOnlinePlayerManager()
	return s, session, repos
}

func TestAntonWaitingAreaActualOp36RequiresOwnedRaidAndIsolatesRoster(t *testing.T) {
	for _, name := range []string{"not_member", "member", "started", "missing_battle", "empty_instance", "wrong_battle_channel", "wrong_battle_account", "ordinary_channel", "channel_alias", "wrong_channel", "wrong_route", "replaced_login"} {
		t.Run(name, func(t *testing.T) {
			s, session, repos := antonWaitingAreaFixture(t)
			key := uint32(23<<16 | 29)
			state := &runtimeRaidState{RaidKey: key, LeaderID: 29, RouteOrRaidType: 0, CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 29}}}
			s.raids = map[uint32]*runtimeRaidState{key: state}
			switch name {
			case "not_member":
				s.raids = nil
			case "wrong_channel":
				session.channel.ID = 24
			case "wrong_route":
				state.RouteOrRaidType = 1
			case "started", "missing_battle", "empty_instance", "wrong_battle_channel", "wrong_battle_account":
				state.Started = true
				state.Battle = &runtimeRaidBattle{InstanceID: "real-battle-owner", ChannelID: 23, ChannelType: 23, MemberAccounts: map[uint16]string{29: "dnf:1"}}
				switch name {
				case "missing_battle":
					state.Battle = nil
				case "empty_instance":
					state.Battle.InstanceID = ""
				case "wrong_battle_channel":
					state.Battle.ChannelID = 24
				case "wrong_battle_account":
					state.Battle.MemberAccounts[29] = "foreign"
				}
			case "ordinary_channel":
				session.channel.Type = 1
			case "channel_alias":
				session.channel.ID = 279
			case "replaced_login":
				peer := &gameSession{selectedCharacterID: 29}
				s.bindGameSessionCharacter(peer, 29)
			}
			if err := s.handleTownSetUserArea(session, buildTownMoveRequest(19, 2, 100, 200, 5)); err != nil {
				t.Fatal(err)
			}
			c, _, err := repos.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			p, found := s.onlinePlayers.PlayerForCharacter(29)
			if name == "member" || name == "started" {
				if c.Stats["area_id"] != 2 || !found || p.Instance != key {
					t.Fatalf("valid member waiting scope missing: stats=%v found=%v presence=%+v", c.Stats, found, p)
				}
			} else if c.Stats["area_id"] != 1 || found {
				t.Fatalf("unowned waiting transition admitted: stats=%v presence=%+v", c.Stats, p)
			}
		})
	}
}

func TestAntonWaitingActualRosterSeesOwnRaidOnly(t *testing.T) {
	s, session, _ := antonWaitingAreaFixture(t)
	key := uint32(23<<16 | 29)
	s.raids = map[uint32]*runtimeRaidState{key: {RaidKey: key, LeaderID: 29, CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 29}, {CharID: 30}}}}
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 30, TownID: 19, AreaID: 2, ChannelID: 23, Instance: key})
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 31, TownID: 19, AreaID: 2, ChannelID: 23, Instance: key + 1})
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 32, TownID: 19, AreaID: 2, ChannelID: 24, Instance: key})
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 33, TownID: 19, AreaID: 2, ChannelID: 23})
	if err := s.handleTownSetUserArea(session, buildTownMoveRequest(19, 2, 100, 200, 5)); err != nil {
		t.Fatal(err)
	}
	raw := session.conn.(*bufferConn).write.Bytes()
	found := false
	for len(raw) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, raw)
		raw = rest
		if packet.Header.MsgID == 24 {
			found = true
			if len(packet.Body) != 22 || binary.LittleEndian.Uint16(packet.Body[2:4]) != 2 || binary.LittleEndian.Uint16(packet.Body[4:6]) != 30 || binary.LittleEndian.Uint16(packet.Body[13:15]) != 29 {
				t.Fatalf("foreign raid/channel/shared actor leaked into op24: %x", packet.Body)
			}
		}
	}
	if !found || !s.onlinePlayers.PeerInSameArea(29, 30) || s.onlinePlayers.PeerInSameArea(29, 31) || s.onlinePlayers.PeerInSameArea(29, 32) || s.onlinePlayers.PeerInSameArea(29, 33) {
		t.Fatal("raid roster/scope is not isolated")
	}
}

func TestAntonWaitingBattleRebindAndLeaveCleanup(t *testing.T) {
	for _, action := range []string{"start_scope", "leave", "channel_change"} {
		t.Run(action, func(t *testing.T) {
			s, session, _ := antonWaitingAreaFixture(t)
			key := uint32(23<<16 | 29)
			s.raids = map[uint32]*runtimeRaidState{key: {RaidKey: key, LeaderID: 29, CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 29}, {CharID: 30}}}}
			if err := s.handleTownSetUserArea(session, buildTownMoveRequest(19, 2, 100, 200, 5)); err != nil {
				t.Fatal(err)
			}
			s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 30, TownID: 19, AreaID: 2, ChannelID: 23, Instance: key})
			switch action {
			case "start_scope":
				s.onlinePlayers.bindAntonWaitingBattleInstance(key, "frozen-battle")
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 31, TownID: 19, AreaID: 2, ChannelID: 23, Instance: key, RaidBattleInstance: "other-battle"})
				p, _ := s.onlinePlayers.PlayerForCharacter(29)
				if p.RaidBattleInstance != "frozen-battle" || !s.onlinePlayers.PeerInSameArea(29, 30) || s.onlinePlayers.PeerInSameArea(29, 31) {
					t.Fatal("battle scopes did not rebind together/isolate", p)
				}
			case "leave":
				if err := s.handleOnlineLeaveRaid(session, 665, []byte{0, 0}); err != nil {
					t.Fatal(err)
				}
				p, found := s.onlinePlayers.PlayerForCharacter(29)
				if !found || p.TownID != 19 || p.AreaID != 0 || p.Instance != 29 || s.onlinePlayers.PeerInSameArea(29, 30) {
					t.Fatal("departed member retained waiting presence")
				}
				if err := s.handleTownSetUserArea(session, buildTownMoveRequest(19, 2, 100, 200, 5)); err != nil {
					t.Fatal(err)
				}
				if p, _ := s.onlinePlayers.PlayerForCharacter(29); p.AreaID == 2 {
					t.Fatal("departed member reentered waiting area")
				}
			case "channel_change":
				session.channel.ID = 24
				s.publishTownPresenceInArea(session, 19, 1, 100, 200, 5, 3, "channel_change_regression")
				p, found := s.onlinePlayers.PlayerForCharacter(29)
				if !found || p.ChannelID != 24 || p.Instance != 0 || p.RaidBattleInstance != "" || s.onlinePlayers.PeerInSameArea(29, 30) {
					t.Fatal("channel transition retained raid waiting scope", p)
				}
			}
		})
	}
}

func TestRealPVFAntonWaitingAreaOwnedOp36(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires formal PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), a, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []bool{false, true} {
		t.Run(fmt.Sprint(member), func(t *testing.T) {
			s, session, repos := antonWaitingAreaFixture(t)
			s.townCatalog = table
			if !s.currentAntonWaitingArea(19, 2) {
				t.Fatal("formal PVF does not match observed Anton waiting gate")
			}
			// Fixture positions are in-memory command inputs, not proposed resource coordinates.
			c, _, _ := repos.Character.Load(context.Background(), "29")
			c.Level = 90
			if err := repos.Character.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if member {
				key := uint32(23<<16 | 29)
				s.raids = map[uint32]*runtimeRaidState{key: {RaidKey: key, LeaderID: 29, CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 29}}}}
			}
			if err := s.handleTownSetUserArea(session, buildTownMoveRequest(19, 2, 100, 200, 5)); err != nil {
				t.Fatal(err)
			}
			c, _, _ = repos.Character.Load(context.Background(), "29")
			if member && c.Stats["area_id"] != 2 || !member && c.Stats["area_id"] != 1 {
				t.Fatal("real waiting admission failed", member, c.Stats)
			}
		})
	}
}

type antonWaitingRaceConn struct {
	bufferConn
	onTransition func()
	fired        bool
}

func (c *antonWaitingRaceConn) Write(data []byte) (int, error) {
	n, err := c.bufferConn.Write(data)
	if !c.fired && len(data) >= 3 && binary.LittleEndian.Uint16(data[1:3]) == 24 {
		c.fired = true
		c.onTransition()
	}
	return n, err
}
func TestAntonWaitingOp36MembershipLostDuringNetworkReturnsOwnedGate(t *testing.T) {
	s, session, repos := antonWaitingAreaFixture(t)
	key := uint32(23<<16 | 29)
	s.raids = map[uint32]*runtimeRaidState{key: {RaidKey: key, LeaderID: 29, CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 29}}}}
	conn := &antonWaitingRaceConn{onTransition: func() { s.removeRuntimeRaidMember(29) }}
	session.conn = conn
	if err := s.handleTownSetUserArea(session, buildTownMoveRequest(19, 2, 100, 200, 5)); err != nil {
		t.Fatal(err)
	}
	c, _, _ := repos.Character.Load(context.Background(), "29")
	p, found := s.onlinePlayers.PlayerForCharacter(29)
	if !conn.fired || c.Stats["town_id"] != 19 || c.Stats["area_id"] != 0 || c.Stats["pos_x"] != 474 || c.Stats["pos_y"] != 234 || !found || p.AreaID != 0 || p.Instance != 29 {
		t.Fatal("in-flight membership loss was published/left self at waiting gate", c.Stats, p)
	}
	raw := conn.write.Bytes()
	lastTown, lastArea := byte(0), byte(255)
	for len(raw) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, raw)
		raw = rest
		if packet.Header.MsgID == 24 {
			lastTown, lastArea = packet.Body[0], packet.Body[1]
		}
	}
	if lastTown != 19 || lastArea != 0 {
		t.Fatal("typed last transition did not restore declared gate", lastTown, lastArea)
	}
}

func TestRealPVFAntonWaitingPersistedLoginAndLeaveUseDeclaredPrivateGate(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires formal PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), a, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s, session, repos := antonWaitingAreaFixture(t)
	s.townCatalog = table
	c, _, _ := repos.Character.Load(context.Background(), "29")
	c.Level = 90
	c.Stats["area_id"] = 2
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	// Covers a stale persisted waiting location after restart/character selection,
	// including the explicit keep-persisted preference: current raid route wins.
	for _, setting := range []string{"", "keep"} {
		t.Setenv("DNFBRIDGE_LOGIN_PERSISTED_TOWN", setting)
		_, town, area, row, path, err := s.currentCharacterListLoginTransition(context.Background(), session, 29, c)
		if err != nil || town != 19 || area != 0 || row.Value1 != 474 || row.Value2 != 234 || path != "Town/BattleShip/Gate_BattleShip.map" {
			t.Fatal("persisted waiting location bypassed raid bootstrap", town, area, row, path, err)
		}
	}
	key := uint32(23<<16 | 29)
	s.raids = map[uint32]*runtimeRaidState{key: {RaidKey: key, LeaderID: 29, CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 29}}}}
	if err := s.handleTownSetUserArea(session, buildTownMoveRequest(19, 2, 100, 200, 5)); err != nil {
		t.Fatal(err)
	}
	if err := s.handleOnlineLeaveRaid(session, 665, []byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	c, _, _ = repos.Character.Load(context.Background(), "29")
	p, found := s.onlinePlayers.PlayerForCharacter(29)
	if c.Stats["area_id"] != 0 || c.Stats["pos_x"] != 474 || c.Stats["pos_y"] != 234 || !found || p.AreaID != 0 || p.Instance != 29 {
		t.Fatal("real leave did not restore PVF declared private gate", c.Stats, p)
	}
}

func TestAntonWaitingMainDeckRemainsPublicAndCrossRaidPartyActorIsNotRecreated(t *testing.T) {
	s, session, _ := antonWaitingAreaFixture(t)
	if err := s.handleTownSetUserArea(session, buildTownMoveRequest(19, 1, 100, 200, 5)); err != nil {
		t.Fatal(err)
	}
	p, found := s.onlinePlayers.PlayerForCharacter(29)
	if !found || p.Instance != 0 || p.AreaID != 1 {
		t.Fatal("public main deck was gated", p)
	}
	key := uint32(23<<16 | 29)
	s.raids = map[uint32]*runtimeRaidState{key: {RaidKey: key, LeaderID: 29, CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 29}}}}
	if err := s.handleTownSetUserArea(session, buildTownMoveRequest(19, 2, 100, 200, 5)); err != nil {
		t.Fatal(err)
	}
	peer := &gameSession{conn: &bufferConn{}, selectedCharacterID: 30, channel: session.channel}
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 30, TownID: 19, AreaID: 2, ChannelID: 23, Instance: key + 1, Session: peer})
	session.conn.(*bufferConn).write.Reset()
	state := alignedcmd.PartyState{PartyID: 1, UserID: 29, Members: []alignedcmd.PartyMemberState{{UserID: 29}, {UserID: 30}}}
	if err := s.sendTownRemotePartyActors(session, state); err != nil {
		t.Fatal(err)
	}
	if session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("cross raid party fallback created foreign actor")
	}
}
