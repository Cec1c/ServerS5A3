package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func guildLifecycleHasPacket(t *testing.T, p *gameSession, class byte, op uint16, body []byte) bool {
	t.Helper()
	for wire := p.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if packet.Header.Classification == class && packet.Header.MsgID == op && bytes.Equal(packet.Body, body) {
			return true
		}
	}
	return false
}

func TestGuildLifecycleSelfExitNativeAndRelog(t *testing.T) {
	for _, region := range []byte{0, 7} {
		s, p, repos := guildTextFixture(t)
		ctx := context.Background()
		reg, _ := dnfguild.Load(ctx, repos.Settings)
		member := reg.Guilds[1].Members["2"]
		member.ServerGroup = 7
		reg.Guilds[1].Members["2"] = member
		c, _, _ := repos.Character.Load(ctx, "2")
		c.Stats["server_group_id"] = 7
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		if err := dnfguild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
			t.Fatal(err)
		}
		p[0].guild.memberListRequested = true
		p[1].guild.cargoLoadedGuild = 1
		body := []byte{region, 0, 0, 0, 0}
		guildDispatch(t, s, p[1], 153, body)
		want := append([]byte{1, 1}, guildWireString("测试公会")...)
		want = append(want, 0)
		want = append(want, guildWireString("角色2")...)
		if got := guildReply(t, p[1], 153); !bytes.Equal(got, want) {
			t.Fatalf("native ACK=%x want=%x", got, want)
		}
		if !guildLifecycleHasPacket(t, p[1], 0, 151, nil) {
			t.Fatal("missing native model reset")
		}
		if !guildLifecycleHasPacket(t, p[1], 0, 127, []byte{100}) {
			t.Fatal("grade not cleared")
		}
		if p[1].guild.cargoLoadedGuild != 0 {
			t.Fatal("cargo access cache survived exit")
		}
		if p[2].conn.(*bufferConn).write.Len() != 0 {
			t.Fatal("cross-guild notification")
		}
		if p[0].conn.(*bufferConn).write.Len() == 0 {
			t.Fatal("remaining roster not refreshed")
		}
		reg, err := dnfguild.Load(ctx, repos.Settings)
		if err != nil || len(reg.Guilds[1].Members) != 1 || reg.Departures["2"].Owner.AccountID != "a2" {
			t.Fatalf("reloaded state=%+v err=%v", reg, err)
		}
		before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
		p[1].conn.(*bufferConn).write.Reset()
		guildDispatch(t, s, p[1], 153, body)
		if got := guildReply(t, p[1], 153); !bytes.Equal(got, []byte{0, 34}) {
			t.Fatalf("replay ACK=%x", got)
		}
		after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("replay mutated persisted state")
		}
		fresh := &Service{options: s.options, repositoryProvider: s.repositoryProvider, gameSessions: map[uint16]*gameSession{}}
		relog := &gameSession{conn: &bufferConn{}, accountID: "a2", selectedCharacterID: 2, characterGeneration: 90, townSceneReadyCharacterID: 2, townActorOwnerChannel: 10}
		fresh.gameSessions[2] = relog
		g, err := fresh.currentGuildForSelectedCharacter(relog)
		if err != nil || g != nil {
			t.Fatal("membership survived relog", err)
		}
	}
}

func TestGuildLifecycleKickAndSoleMasterDisband(t *testing.T) {
	for _, disbandOp := range []uint16{153, 303} {
		s, p, repos := guildTextFixture(t)
		body := []byte(nil)
		if disbandOp == 153 {
			body = []byte{0, 0, 0, 0, 0}
		}
		guildDispatch(t, s, p[0], disbandOp, body)
		if got := guildReply(t, p[0], disbandOp); !bytes.Equal(got, []byte{0, 87}) {
			t.Fatalf("multi-member disband ACK=%x", got)
		}
		guildDispatch(t, s, p[0], 153, append([]byte{0}, guildWireString("角色2")...))
		want := append([]byte{1, 2}, guildWireString("测试公会")...)
		want = append(want, 0)
		want = append(want, guildWireString("角色2")...)
		if got := guildReply(t, p[0], 153); !bytes.Equal(got, want) {
			t.Fatalf("kick ACK=%x", got)
		}
		if !guildLifecycleHasPacket(t, p[1], 0, 151, nil) || guildLifecycleHasPacket(t, p[0], 0, 151, nil) {
			t.Fatal("kick cleared wrong member")
		}
		p[0].conn.(*bufferConn).write.Reset()
		guildDispatch(t, s, p[0], disbandOp, body)
		if got := guildReply(t, p[0], disbandOp); len(got) == 0 || got[0] != 1 {
			t.Fatalf("disband ACK=%x", got)
		}
		if !guildLifecycleHasPacket(t, p[0], 0, 151, nil) {
			t.Fatal("master model not reset")
		}
		reg, err := dnfguild.Load(context.Background(), repos.Settings)
		if err != nil || reg.Guilds[1] != nil || reg.Guilds[2] == nil || len(reg.Departures) != 1 {
			t.Fatalf("disband state=%+v err=%v", reg, err)
		}
	}
}

func TestGuildLifecycleRejectsMalformedForeignAndStaleTargets(t *testing.T) {
	for _, mode := range []string{"short", "region250", "trailing", "wrong_region", "foreign_guild", "member_kicks_master", "member_disbands", "named_self_wrong_region", "stale_member", "class0", "disband_body"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := guildTextFixture(t)
			ctx := context.Background()
			actor, op, class, body := p[0], uint16(153), byte(1), append([]byte{0}, guildWireString("角色2")...)
			switch mode {
			case "short":
				body = []byte{0}
			case "region250":
				body[0] = 250
			case "trailing":
				body = append(body, 0)
			case "wrong_region":
				body[0] = 8
			case "foreign_guild":
				body = append([]byte{0}, guildWireString("角色3")...)
			case "member_kicks_master":
				actor = p[1]
				body = append([]byte{0}, guildWireString("角色1")...)
			case "member_disbands":
				actor, op, body = p[1], 303, nil
			case "named_self_wrong_region":
				actor = p[1]
				body[0] = 7
			case "stale_member":
				reg, _ := dnfguild.Load(ctx, repos.Settings)
				m := reg.Guilds[1].Members["2"]
				m.AccountID = "old"
				reg.Guilds[1].Members["2"] = m
				if err := dnfguild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			case "class0":
				class = 0
			case "disband_body":
				op = 303
				body = []byte{0}
			}
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			handled, err := currentGameplayModules.DispatchUpper(s, actor, op, class, body)
			if !handled || err != nil {
				t.Fatalf("dispatch=%v %v", handled, err)
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid request mutated registry")
			}
			if class == 1 {
				if got := guildReply(t, actor, op); len(got) < 2 || got[0] != 0 {
					t.Fatalf("rejection=%x", got)
				}
			}
			for _, peer := range p {
				if guildLifecycleHasPacket(t, peer, 0, 151, nil) {
					t.Fatal("rejected request cleared model")
				}
			}
		})
	}
}

func TestGuildLifecycleLateNotificationKeepsNewMembership(t *testing.T) {
	s, p, repos := guildTextFixture(t)
	ctx := context.Background()
	reg, _ := dnfguild.Load(ctx, repos.Settings)
	m := reg.Guilds[1].Members["2"]
	removed := dnfguild.MemberRemoval{GuildID: 1, GuildName: reg.Guilds[1].Name, Member: m}
	delete(reg.Guilds[1].Members, "2")
	c, _, _ := repos.Character.Load(ctx, "2")
	if _, err := reg.JoinDirect(2, c, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	if err := dnfguild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	if err := s.clearDepartedGuildSession(ctx, removed); err != nil {
		t.Fatal(err)
	}
	if p[1].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("late reset cleared newly joined guild")
	}
	owner := dnfrepo.GuildOwner{CharacterID: "2", AccountID: "a2"}
	reg, _ = dnfguild.Load(ctx, repos.Settings)
	g, _, found := reg.GuildForOwner(owner)
	if !found || g.ID != 2 {
		t.Fatal("new membership lost")
	}
}
