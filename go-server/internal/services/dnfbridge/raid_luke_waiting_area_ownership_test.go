package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Resource identity and reported positions come from the supplied real PVF.
// The movable rectangle midpoint is a command fixture, not an official spawn.
func realLukeWaitingFixture39(t *testing.T, targetArea ...byte) (*Service, *gameSession, dnfrepo.Group, raid.AdmissionRule, []byte) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires fixed real PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raid.LoadLukeProgram(a); err != nil {
		t.Fatal(err)
	}
	rule, err := raid.LoadAdmissionRule(a, "etc/raid/luke/luke.etc")
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), a, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	town, found := table.Find(int64(rule.WaitingTown))
	if !found {
		t.Fatal("Luke WAITING ROOM town missing")
	}
	areaID := rule.WaitingArea
	if len(targetArea) != 0 {
		areaID = targetArea[0]
	}
	area, found := table.FindArea(int64(rule.WaitingTown), int64(areaID))
	if !found {
		t.Fatal("Luke WAITING ROOM area missing")
	}
	identity, _ := json.Marshal(struct {
		Town dnftown.Town
		Rule raid.AdmissionRule
	}{town, rule})
	t.Logf("real Luke resource identity: %s", identity)
	text, err := a.ReadText(town.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual %s:\n%s", town.Path, text)
	m, found := currentTownAreaMapDocument(a, area)
	if !found || len(m.VirtualMovableArea) < 4 {
		t.Fatal("actual waiting map movable rectangle missing")
	}
	r := m.VirtualMovableArea[:4]
	x, y := r[0]+r[2]/2, r[1]+r[3]/2
	if r[2] <= 0 || r[3] <= 0 || x < 0 || y < 0 || x > 32767 || y > 32767 {
		t.Fatal("actual map cannot provide a bounded reported-position fixture", r)
	}
	t.Logf("actual waiting map=%s movable=%v reported-position fixture=(%d,%d)", area.MapPath, r, x, y)
	s, session, repos := newTownMoveTest(t)
	s.options.currentClientVersion = "official-24.9"
	s.townCatalog = table
	s.seriaRoomDoorMapReader = func(area dnftown.Area) (worldmap.Map, bool) { return currentTownAreaMapDocument(a, area) }
	s.onlinePlayers = newOnlinePlayerManager()
	session.channel = channelcatalog.Channel{ID: 32, Type: 32}
	session.accountID, session.loginPacketSeen, session.accountFromToken = "dnf:1", true, true
	s.bindGameSessionCharacter(session, 29)
	c, found, err := repos.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatal("fixture character missing", err)
	}
	c.Level = 90
	c.Stats["town_id"], c.Stats["area_id"] = int64(rule.WaitingTown), 1
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return s, session, repos, rule, buildTownMoveRequest(rule.WaitingTown, areaID, int16(x), int16(y), 5)
}

func TestRealPVFLukeWaiting39ResourceGateEvidence(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires fixed real PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	wm, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{SkipMaps: true, SkipDungeons: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{32, 40} {
		area, found := wm.FindArea(id)
		if !found {
			t.Fatal("actual worldmap gate missing", id)
		}
		raw, err := a.ReadText(area.Path)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("gate %d actual %s:\n%s", id, area.Path, raw)
	}
	config, err := raid.LoadBattleConfiguration(a, "etc/raid/luke/luke.etc")
	if err != nil {
		t.Fatal(err)
	}
	lukeGate, _ := wm.FindArea(40)
	for _, phase := range config.PhaseDungeons {
		for _, dungeon := range phase {
			found := false
			for _, entry := range lukeGate.Dungeons {
				found = found || entry.DungeonID == int64(dungeon.ID)
			}
			if !found {
				t.Fatal("program dungeon absent from gate40", dungeon)
			}
		}
	}
}

func TestRealPVFLukeWaiting39CastleGateRemainsPublic(t *testing.T) {
	s, session, repos, _, body := realLukeWaitingFixture39(t, 2)
	lukeWaitingPublicOp36(t, s, session, body)
	c, _, err := repos.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	p, found := s.onlinePlayers.PlayerForCharacter(29)
	if c.Stats["area_id"] != 2 || !found || p.Instance != 0 || session.conn.(*bufferConn).write.Len() == 0 {
		t.Fatalf("ordinary CastleoftheDead gate was restricted: stats=%v presenceFound=%v presence=%+v", c.Stats, found, p)
	}
}

func lukeWaitingPublicOp36(t *testing.T, s *Service, session *gameSession, body []byte) {
	t.Helper()
	h, found := townGameplayModule().UpperHandlers[36]
	if !found {
		t.Fatal("public Upper op36 missing")
	}
	if err := h(s, session, gameplayRequest{Body: body, Classification: dnfproto.DefaultChannelClassification}); err != nil {
		t.Fatal(err)
	}
}

func TestRealPVFLukeWaiting39PublicOp36RejectsNonmember(t *testing.T) {
	s, session, repos, rule, body := realLukeWaitingFixture39(t)
	if rule.WaitingTown != 32 || rule.WaitingArea != 4 {
		t.Fatal("fixed Luke resource changed", rule)
	}
	lukeWaitingPublicOp36(t, s, session, body)
	c, _, err := repos.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	p, found := s.onlinePlayers.PlayerForCharacter(29)
	if c.Stats["area_id"] != 1 || found || session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("public op36 admitted nonmember: stats=%v presenceFound=%v presence=%+v sentBytes=%d", c.Stats, found, p, session.conn.(*bufferConn).write.Len())
	}
}

func TestRealPVFLukeWaiting39PublicOp36OwnershipMatrix(t *testing.T) {
	for _, name := range []string{"member", "started", "not_member", "ordinary_channel", "anton_channel", "wrong_channel", "channel_alias", "wrong_server", "wrong_route", "legacy_layout", "unauthenticated", "replaced_login", "missing_battle", "empty_instance", "wrong_battle_channel", "wrong_battle_type", "wrong_battle_account"} {
		t.Run(name, func(t *testing.T) {
			s, session, repos, _, body := realLukeWaitingFixture39(t)
			key := uint32(32<<16 | 29)
			state := &runtimeRaidState{RaidKey: key, LeaderID: 29, RouteOrRaidType: 1, CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 29}}}
			s.raids = map[uint32]*runtimeRaidState{key: state}
			switch name {
			case "not_member":
				s.raids = nil
			case "ordinary_channel":
				session.channel.Type = 1
			case "anton_channel":
				session.channel.Type = 23
			case "wrong_channel":
				session.channel.ID = 33
			case "channel_alias":
				session.channel.ID = 288
			case "wrong_server":
				state.RaidKey |= 1 << 24
			case "wrong_route":
				state.RouteOrRaidType = 0
			case "legacy_layout":
				state.CurrentLayout = false
			case "unauthenticated":
				session.accountFromToken = false
			case "replaced_login":
				s.bindGameSessionCharacter(&gameSession{selectedCharacterID: 29}, 29)
			case "started", "missing_battle", "empty_instance", "wrong_battle_channel", "wrong_battle_type", "wrong_battle_account":
				state.Started = true
				state.Battle = &runtimeRaidBattle{InstanceID: "luke-waiting39-owned-battle", Mode: 2, ChannelID: 32, ChannelType: 32, MemberAccounts: map[uint16]string{29: "dnf:1"}}
				switch name {
				case "missing_battle":
					state.Battle = nil
				case "empty_instance":
					state.Battle.InstanceID = ""
				case "wrong_battle_channel":
					state.Battle.ChannelID = 33
				case "wrong_battle_type":
					state.Battle.ChannelType = 23
				case "wrong_battle_account":
					state.Battle.MemberAccounts[29] = "foreign"
				}
			}
			lukeWaitingPublicOp36(t, s, session, body)
			c, _, err := repos.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			p, found := s.onlinePlayers.PlayerForCharacter(29)
			if name == "member" || name == "started" {
				battle := ""
				if state.Started {
					battle = state.Battle.InstanceID
				}
				if c.Stats["area_id"] != 4 || !found || p.Instance != key || p.RaidBattleInstance != battle {
					t.Fatal("valid owned Luke waiting scope missing", c.Stats, p)
				}
			} else if c.Stats["area_id"] != 1 || found || session.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("unowned Luke waiting transition admitted", c.Stats, p)
			}
		})
	}
}

func TestRealPVFLukeWaiting39RosterAndPartyActorScope(t *testing.T) {
	s, session, _, _, body := realLukeWaitingFixture39(t)
	key := uint32(32<<16 | 29)
	battle := "luke-waiting39-owned-battle"
	s.raids = map[uint32]*runtimeRaidState{key: {RaidKey: key, LeaderID: 29, RouteOrRaidType: 1, CurrentLayout: true, Started: true, Members: []runtimeRaidMemberState{{CharID: 29}, {CharID: 30}}, Battle: &runtimeRaidBattle{InstanceID: battle, Mode: 2, ChannelID: 32, ChannelType: 32, MemberAccounts: map[uint16]string{29: "dnf:1", 30: "dnf:2"}}}}
	for _, p := range []onlinePlayerInfo{
		{CharacterID: 30, TownID: 32, AreaID: 4, ChannelID: 32, Instance: key, RaidBattleInstance: battle},
		{CharacterID: 31, TownID: 32, AreaID: 4, ChannelID: 32, Instance: key + 1, RaidBattleInstance: battle},
		{CharacterID: 32, TownID: 32, AreaID: 4, ChannelID: 33, Instance: key, RaidBattleInstance: battle},
		{CharacterID: 33, TownID: 32, AreaID: 4, ChannelID: 32},
		{CharacterID: 34, TownID: 32, AreaID: 4, ChannelID: 32, Instance: key, RaidBattleInstance: "foreign-battle"},
	} {
		s.onlinePlayers.EnterArea(&p)
	}
	lukeWaitingPublicOp36(t, s, session, body)
	conn := session.conn.(*bufferConn)
	raw, seen := conn.write.Bytes(), false
	for len(raw) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, raw)
		raw = rest
		if packet.Header.MsgID == 24 {
			seen = true
			if len(packet.Body) != 22 || binary.LittleEndian.Uint16(packet.Body[2:4]) != 2 || binary.LittleEndian.Uint16(packet.Body[4:6]) != 30 || binary.LittleEndian.Uint16(packet.Body[13:15]) != 29 {
				t.Fatalf("foreign actor leaked in waiting op24: %x", packet.Body)
			}
		}
	}
	if !seen || !s.onlinePlayers.PeerInSameArea(29, 30) {
		t.Fatal("own roster missing")
	}
	for _, id := range []uint16{31, 32, 33, 34} {
		if s.onlinePlayers.PeerInSameArea(29, id) {
			t.Fatal("foreign waiting peer", id)
		}
	}
	foreign := &gameSession{conn: &bufferConn{}, selectedCharacterID: 31, channel: session.channel}
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 31, TownID: 32, AreaID: 4, ChannelID: 32, Instance: key + 1, Session: foreign})
	conn.write.Reset()
	party := alignedcmd.PartyState{PartyID: 1, UserID: 29, Members: []alignedcmd.PartyMemberState{{UserID: 29}, {UserID: 31}}}
	if err := s.sendTownRemotePartyActors(session, party); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("party fallback recreated foreign raid actor")
	}
}

func TestRealPVFLukeWaiting39StartLeaveAndNetworkScopeLoss(t *testing.T) {
	for _, action := range []string{"start", "leave", "network_scope_loss"} {
		t.Run(action, func(t *testing.T) {
			s, session, repos, _, body := realLukeWaitingFixture39(t)
			key := uint32(32<<16 | 29)
			s.raids = map[uint32]*runtimeRaidState{key: {RaidKey: key, LeaderID: 29, RouteOrRaidType: 1, CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 29}}}}
			if action == "network_scope_loss" {
				conn := &antonWaitingRaceConn{onTransition: func() { s.removeRuntimeRaidMember(29) }}
				session.conn = conn
				lukeWaitingPublicOp36(t, s, session, body)
				if !conn.fired {
					t.Fatal("op24 race hook did not run")
				}
				lastTown, lastArea := byte(0), byte(255)
				raw := conn.write.Bytes()
				for len(raw) > 0 {
					packet, rest := splitCurrentGameServerUpperPacketAuto(t, raw)
					raw = rest
					if packet.Header.MsgID == 24 {
						lastTown, lastArea = packet.Body[0], packet.Body[1]
					}
				}
				if lastTown != 32 || lastArea != 0 {
					t.Fatal("membership loss did not restore final typed Seria scene", lastTown, lastArea)
				}
			} else {
				lukeWaitingPublicOp36(t, s, session, body)
				if action == "start" {
					peer := &gameSession{channel: session.channel, selectedCharacterID: 30, accountID: "dnf:30"}
					s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 30, AccountID: peer.accountID, Session: peer, TownID: 32, AreaID: 4, ChannelID: 32, Instance: key})
					s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 31, TownID: 32, AreaID: 2, ChannelID: 32, Instance: key})
					s.onlinePlayers.bindRaidWaitingBattleInstance(waitingBattleScopeForTest(key, "luke-started-battle", 32, 32, 4, session, peer))
					p, _ := s.onlinePlayers.PlayerForCharacter(29)
					ordinary, _ := s.onlinePlayers.PlayerForCharacter(31)
					if p.RaidBattleInstance != "luke-started-battle" || !s.onlinePlayers.PeerInSameArea(29, 30) || ordinary.RaidBattleInstance != "" {
						t.Fatal("Luke start rebucket incorrect", p, ordinary)
					}
					return
				}
				if err := s.handleOnlineLeaveRaid(session, 665, []byte{0, 1}); err != nil {
					t.Fatal(err)
				}
			}
			c, _, err := repos.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			p, found := s.onlinePlayers.PlayerForCharacter(29)
			if c.Stats["town_id"] != 32 || c.Stats["area_id"] != 0 || c.Stats["pos_x"] != 474 || c.Stats["pos_y"] != 234 || !found || p.AreaID != 0 || p.Instance != 29 {
				t.Fatal("scope loss did not use actual private Seria landing", c.Stats, p)
			}
			lukeWaitingPublicOp36(t, s, session, body)
			p, _ = s.onlinePlayers.PlayerForCharacter(29)
			if p.AreaID != 0 || p.Instance != 29 {
				t.Fatal("departed member reentered waiting", p)
			}
		})
	}
}
