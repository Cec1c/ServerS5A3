package dnfbridge

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func guildMemberProfileFixture(t *testing.T) (*townPartyActorHPFixture, dnfrepo.Group) {
	t.Helper()
	f := newTownPartyActorHPFixture(t, false)
	s := f.service
	f.target.accountID, f.actor.accountID = "dnf:1", "dnf:2"
	s.options.channelAdvertiseID = 1
	f.actor.channel.ID, f.actor.connectionTownActorOwnerChannel = 11, 11
	s.adventureGroupTable = loadAdventureGroupTestTables(t)
	repos, _ := s.repositoryGroup()
	ctx := context.Background()
	r := dnfguild.NewRegistry()
	v, _, _ := repos.Character.Load(ctx, "29")
	v.AccountID, v.Name, v.Level = "dnf:1", "viewer", 90
	if err := repos.Character.Save(ctx, v); err != nil {
		t.Fatal(err)
	}
	a, _, _ := repos.Character.Load(ctx, "17")
	g, err := r.Create(v, "资料公会", s.gameplayNow())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.JoinDirect(g.ID, a, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "dnf:2", State: "active", RepresentAccountName: "成员冒险团", Metadata: map[string]string{adventureGroupCreatedDateMetadataKey: "2026-07-16", adventuregroup.DailyLoginStateMetadataKey: "2026-07-16|7"}}); err != nil {
		t.Fatal(err)
	}
	f.reset()
	return f, repos
}

func TestGuildMemberProfileNativeRequestsAndReadOnlyReplies(t *testing.T) {
	packets := map[string]string{}
	for _, kind := range []byte{0, 4} {
		f, repos := guildMemberProfileFixture(t)
		before, _, _ := repos.Account.Load(context.Background(), "dnf:2")
		reg, err := dnfguild.Load(context.Background(), repos.Settings)
		if err != nil {
			t.Fatal(err)
		}
		guild, _, _ := reg.GuildForCharacter("17")
		packets["roster"] = hex.EncodeToString(buildCurrentGuildMemberListBody(guild, true, false, f.service.currentGuildOnlineChannels(guild, f.target), f.service.gameplayNow(), 1))
		request := []byte{11, 17, 0, kind, 1}
		if err := f.service.handleGameCommand(f.target, byte(dnfenum.GameCmdCommand), currentGuildMemberProfileOpcode, request); err != nil {
			t.Fatal(err)
		}
		wire := f.targetConn.write.Bytes()
		if kind == 0 {
			assertCurrentCrossSceneProfileFramePair(t, wire, f.service.gameUpperHeaderSize(), 17, 1, 11)
		} else {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			if len(rest) != 0 || packet.Header.Classification != 1 || packet.Header.MsgID != 1401 || packet.Body[0] != 1 || packet.Body[1] != 17 || packet.Body[2] != 0 {
				t.Fatalf("adventure header=%+v len=%d", packet.Header, len(packet.Body))
			}
		}
		for len(wire) > 0 {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			name := "adventure"
			if kind == 0 {
				if packet.Body[0] == 8 {
					name = "prime"
				} else {
					name = "detail"
				}
			}
			packets[name] = hex.EncodeToString(packet.Body)
		}
		after, _, _ := repos.Account.Load(context.Background(), "dnf:2")
		if !reflect.DeepEqual(before, after) || f.actorConn.write.Len() != 0 || f.target.rosterRequested {
			t.Fatal("inspection changed account or sent to actor/selector")
		}
	}
	if path := os.Getenv("DNF_GUILD_PROFILE_PACKETS"); path != "" {
		data, err := json.MarshalIndent(packets, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGuildMemberProfileSlowSocketDeadlineReleasesBindingAndLocks(t *testing.T) {
	f, _ := guildMemberProfileFixture(t)
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	conn := &crossSceneProfileObservedConn{Conn: left, writing: make(chan struct{})}
	f.target.conn = conn
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	a, err := f.service.loadCurrentGuildMemberProfileAccess(ctx, f.target, currentGuildMemberProfileRequest{channel: 11, character: 17, directory: 1})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- f.service.sendCurrentGuildMemberProfileFrames(ctx, a, 0, []currentGuildMemberProfileFrame{{2, 0, []byte{8}}})
	}()
	select {
	case <-conn.writing:
	case <-time.After(time.Second):
		t.Fatal("did not reach socket")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("slow write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("slow socket held owner")
	}
	if !f.actor.crossSceneAppearanceMu.TryLock() {
		t.Fatal("appearance lock retained")
	}
	f.actor.crossSceneAppearanceMu.Unlock()
	if !f.target.wireMu.TryLock() {
		t.Fatal("wire lock retained")
	}
	f.target.wireMu.Unlock()
	// A successful new reservation proves that both binding barriers were released.
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	release, err := f.service.reserveCurrentGuildParticipants(ctx2, []boundGameSessionCharacter{a.viewer, a.actor})
	if err != nil {
		t.Fatal(err)
	}
	release()
	if f.actorConn.write.Len() != 0 {
		t.Fatal("slow viewer affected target connection")
	}
}

func TestGuildMemberProfileStalePreparedPeerDoesNotFailViewer(t *testing.T) {
	f, _ := guildMemberProfileFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, err := f.service.loadCurrentGuildMemberProfileAccess(ctx, f.target, currentGuildMemberProfileRequest{channel: 11, character: 17, directory: 1})
	if err != nil {
		t.Fatal(err)
	}
	f.actor.characterGeneration++
	if err := f.service.sendCurrentGuildMemberProfileFrames(ctx, a, 0, []currentGuildMemberProfileFrame{{2, 0, []byte{8}}}); err != nil {
		t.Fatalf("stale peer failed viewer: %v", err)
	}
	if f.targetConn.write.Len() != 0 {
		t.Fatal("stale profile sent")
	}
	v, ok := f.service.boundGameSessionCharacterSnapshot(f.target)
	if !ok {
		t.Fatal("viewer retired")
	}
	r, err := f.service.reserveCurrentCharacterAssets(ctx, v, "dnf:1")
	if err != nil {
		t.Fatal(err)
	}
	f.service.releaseCurrentCharacterAssets(r)
}

func TestGuildMemberProfileRejectsInvalidRequests(t *testing.T) {
	for _, body := range [][]byte{nil, {11, 17, 0, 0}, {11, 17, 0, 0, 1, 0}, {0, 17, 0, 0, 1}, {255, 17, 0, 0, 1}, {12, 17, 0, 0, 1}, {11, 0, 0, 0, 1}, {11, 255, 255, 0, 1}, {11, 17, 0, 3, 1}, {11, 17, 0, 0, 2}, {11, 29, 0, 0, 1}} {
		f, _ := guildMemberProfileFixture(t)
		guildDispatch(t, f.service, f.target, 819, body)
		if f.targetConn.write.Len() != 0 {
			t.Fatalf("accepted %x", body)
		}
	}
}

func TestGuildMemberProfileRevalidatesMembershipIdentityAndOwnership(t *testing.T) {
	for _, mode := range []string{"leave", "rejoin", "guild", "account", "deleted", "reconnect", "generation", "channel", "viewer_channel", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			f, repos := guildMemberProfileFixture(t)
			s := f.service
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			a, err := s.loadCurrentGuildMemberProfileAccess(ctx, f.target, currentGuildMemberProfileRequest{channel: 11, character: 17, directory: 1})
			if err != nil {
				t.Fatal(err)
			}
			r, _ := dnfguild.Load(ctx, repos.Settings)
			g, m, _ := r.GuildForCharacter("17")
			switch mode {
			case "leave":
				delete(g.Members, "17")
			case "rejoin":
				m.JoinedAt = m.JoinedAt.Add(time.Second)
				g.Members["17"] = m
			case "guild":
				delete(g.Members, "17")
				c, _, _ := repos.Character.Load(ctx, "17")
				if _, err := r.Create(c, "另一公会", s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			case "account", "deleted":
				c, _, _ := repos.Character.Load(ctx, "17")
				if mode == "account" {
					c.AccountID = "another"
				} else {
					c.Stats["delete_flag"] = 1
				}
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			case "reconnect":
				delete(s.gameSessions, 17)
			case "generation":
				f.actor.characterGeneration++
			case "channel":
				f.actor.channel.ID = 12
			case "viewer_channel":
				f.target.channel.ID++
			case "revoked":
				f.actor.loginRevoked.Store(true)
			}
			if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.validateCurrentGuildMemberProfileAccess(ctx, a); err == nil {
				t.Fatal("stale access accepted")
			}
			guildDispatch(t, s, f.target, 819, []byte{11, 17, 0, 0, 1})
			// Rejoining with a new request is legitimate; only the old receipt is stale.
			if mode != "rejoin" && mode != "viewer_channel" && mode != "generation" && f.targetConn.write.Len() != 0 {
				t.Fatal("invalid request produced profile")
			}
		})
	}
}

func TestGuildMemberProfileReloadsMembershipAfterWireWait(t *testing.T) {
	f, repos := guildMemberProfileFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, err := f.service.loadCurrentGuildMemberProfileAccess(ctx, f.target, currentGuildMemberProfileRequest{channel: 11, character: 17, directory: 1})
	if err != nil {
		t.Fatal(err)
	}
	f.target.wireMu.Lock()
	done := make(chan error, 1)
	go func() {
		done <- f.service.sendCurrentGuildMemberProfileFrames(ctx, a, 0, []currentGuildMemberProfileFrame{{2, 0, []byte{8}}})
	}()
	r, _ := dnfguild.Load(ctx, repos.Settings)
	g, _, _ := r.GuildForCharacter("17")
	delete(g.Members, "17")
	if err := dnfguild.Save(ctx, repos.Settings, r, f.service.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	f.target.wireMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("wire wait did not finish")
	}
	if f.targetConn.write.Len() != 0 {
		t.Fatal("removed member leaked after wire wait")
	}
}

func TestGuildMemberProfileLocalMenuAndExistingPartyPermission(t *testing.T) {
	for _, kind := range []byte{0, 4} {
		f, _ := guildMemberProfileFixture(t)
		f.actor.channel.ID = f.target.channel.ID
		info := f.actorInfo
		info.AreaID++
		f.service.onlinePlayers.EnterArea(&info)
		if kind == 0 {
			handled, err := f.service.handleCurrentPeerUserInfoRequest(f.target, []byte{17, 0, 3})
			if !handled || err != nil {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
		} else if err := f.service.handleCurrentRequestAdventureInfo(f.target, []byte{17, 0}); err != nil {
			t.Fatal(err)
		}
		if f.targetConn.write.Len() == 0 {
			t.Fatalf("local-context kind=%d ignored", kind)
		}
		if f.service.currentCrossSceneProfileAllowed(context.Background(), f.target, f.actor, 29, 17) {
			t.Fatal("guild access widened party authorization")
		}
	}
}
