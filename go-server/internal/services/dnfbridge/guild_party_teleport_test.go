package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/party"
	"longheng.io/server/internal/modules/dnf/protocol"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func guildTeleportFixture(t *testing.T, n int) (*Service, []*gameSession, r.Group) {
	t.Helper()
	s, p, repos := guildTextFixture(t)
	town, template, _ := newTownMoveTest(t)
	s.townCatalog, s.premiumCatalog = town.townCatalog, town.premiumCatalog
	s.runtimePartyManager = party.NewRuntimePartyManager()
	if n == 4 {
		c := r.CharacterRecord{CharacterID: "4", AccountID: "a4", Name: "角色4", Job: "1", Level: 90, Stats: map[string]int64{}}
		if err := repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		peer := &gameSession{conn: &bufferConn{}, accountID: "a4", selectedCharacterID: 4, characterGeneration: 4, townSceneReadyCharacterID: 4}
		p = append(p, peer)
		s.gameSessions[4] = peer
	}
	p = p[:n]
	for _, peer := range p {
		peer.channel, peer.residentChannel = template.channel, template.residentChannel
		peer.townActorOwnerChannel, peer.connectionTownActorOwnerChannel = 19, 19
		c, _, _ := repos.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
		if c.Stats == nil {
			c.Stats = map[string]int64{}
		}
		c.Stats["town_id"], c.Stats["area_id"], c.Stats["pos_x"], c.Stats["pos_y"], c.Stats["direction"], c.Stats["area_state"] = 38, 0, 100, 200, 5, 3
		if err := repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		enableTownMoveSkillProjection(t, s, repos, c.CharacterID)
	}
	reg, _ := guild.Load(context.Background(), repos.Settings)
	reg.Guilds[1].Benefits = map[byte]guild.Benefit{7: {Type: 7, Value: 2}}
	if err := guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	if n > 1 {
		member := func(peer *gameSession) party.RuntimePartyMember {
			return party.RuntimePartyMember{UserID: peer.selectedCharacterID, SessionGeneration: peer.characterGeneration, State: alignedcmd.PartyMemberState{UserID: peer.selectedCharacterID}}
		}
		created := s.runtimePartyManager.Create(member(p[0]), alignedcmd.PartyState{MaxMembers: 4})
		if !created.OK {
			t.Fatal(created)
		}
		for _, peer := range p[1:] {
			if got := s.runtimePartyManager.Join(created.Party.ID, member(peer)); !got.OK {
				t.Fatal(got)
			}
		}
		snapshot, _ := s.runtimePartyManager.SnapshotByID(created.Party.ID)
		for _, peer := range p {
			storeRuntimePartyState(peer, snapshot.StateFor(peer.selectedCharacterID))
		}
	}
	t.Cleanup(func() {
		s.guildTeleportMu.Lock()
		defer s.guildTeleportMu.Unlock()
		for _, peer := range p {
			if pending := peer.guild.teleport; pending != nil {
				s.clearGuildTeleport(pending, 2)
			}
		}
	})
	return s, p, repos
}

func sendGuildTeleport(t *testing.T, s *Service, p *gameSession, op, seq uint16, body []byte) {
	t.Helper()
	wire, err := protocol.BuildChannelPacket(op, body, seq, 1)
	if err != nil {
		t.Fatal(err)
	}
	packets, remaining, skipped, err := protocol.SplitLatestGameStream(wire, 65535)
	if err != nil || len(packets) != 1 || len(remaining) != 0 || skipped != 0 {
		t.Fatal("packet", err)
	}
	if err := s.handleGameStreamPacket(p, packets[0]); err != nil {
		t.Fatal(err)
	}
}

func teleportRequestBody(town, area byte) []byte { return []byte{town, area, 0xf4, 1, 0x2c, 1, 5, 1} }

func TestGuildPartyTeleportFullConsentCommitsAndReplaysOnce(t *testing.T) {
	for _, n := range []int{1, 2, 4} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, p, repos := guildTeleportFixture(t, n)
			sendGuildTeleport(t, s, p[0], 434, 10, teleportRequestBody(39, 0))
			if got := guildReply(t, p[0], 434); !bytes.Equal(got, []byte{1}) {
				t.Fatalf("start ACK %x", got)
			}
			if n > 1 {
				body := guildReply(t, p[1], 377)
				if len(body) != 30 || body[1] != 0 || binary.LittleEndian.Uint16(body[3:5]) != 0 {
					t.Fatalf("prompt=%x", body)
				}
				reg, _ := guild.Load(context.Background(), repos.Settings)
				if reg.Guilds[1].Benefits[7].Value != 2 {
					t.Fatal("charged before consent")
				}
			}
			for _, peer := range p[1:] {
				sendGuildTeleport(t, s, peer, 435, 20, []byte{1})
			}
			for _, peer := range p {
				c, _, err := repos.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
				if err != nil || c.Stats["town_id"] != 39 || c.Stats["pos_x"] != 500 {
					t.Fatal("not moved", c, err)
				}
				guildReply(t, peer, 24)
				if n > 1 && guildReply(t, peer, 377)[1] != 1 {
					t.Fatal("no completed state")
				}
			}
			reg, err := guild.Load(context.Background(), repos.Settings)
			if err != nil || reg.Guilds[1].Benefits[7].Value != 1 || len(reg.Teleports) != 1 {
				t.Fatal("use/receipt", err)
			}
			sendGuildTeleport(t, s, p[0], 434, 10, teleportRequestBody(39, 0))
			for _, peer := range p[1:] {
				sendGuildTeleport(t, s, peer, 435, 20, []byte{1})
			}
			reg, _ = guild.Load(context.Background(), repos.Settings)
			if reg.Guilds[1].Benefits[7].Value != 1 || len(reg.Teleports) != 1 {
				t.Fatal("repeat charge")
			}
		})
	}
}

func TestGuildPartyTeleportRetryDestinationAndWireReplay(t *testing.T) {
	s, p, repos := guildTeleportFixture(t, 2)
	sendGuildTeleport(t, s, p[0], 434, 10, teleportRequestBody(39, 0))
	sendGuildTeleport(t, s, p[1], 435, 20, []byte{0})
	retry := []byte{0, 0, 0, 0, 0, 0, 5, 0}
	sendGuildTeleport(t, s, p[0], 434, 30, retry)
	sendGuildTeleport(t, s, p[1], 435, 40, []byte{1})
	sendGuildTeleport(t, s, p[0], 434, 30, retry)
	if got := guildReply(t, p[0], 434); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("retry wire replay ACK %x", got)
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil || reg.Guilds[1].Benefits[7].Value != 1 || len(reg.Teleports) != 1 {
		t.Fatal("retry charged twice", err)
	}
	for _, peer := range p {
		c, _, err := repos.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
		if err != nil || c.Stats["town_id"] != 39 || c.Stats["pos_x"] != 500 {
			t.Fatal("retry destination", err)
		}
	}
}

func TestGuildPartyTeleportRejectTimeoutAndChangedMembershipKeepLocations(t *testing.T) {
	for _, mode := range []string{"reject", "timeout", "expired", "leave", "leader", "session", "channel", "guild", "location", "dungeon"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := guildTeleportFixture(t, 2)
			sendGuildTeleport(t, s, p[0], 434, 10, teleportRequestBody(39, 0))
			pending := p[0].guild.teleport
			if pending == nil {
				t.Fatal("no pending")
			}
			answer := byte(1)
			switch mode {
			case "reject":
				answer = 0
			case "timeout":
				answer = 3
			case "expired":
				pending.deadline = s.gameplayNow().Add(-time.Second)
			case "leave":
				s.runtimePartyManager.Leave(2, p[1].characterGeneration)
			case "leader":
				s.runtimePartyManager.TransferLeader(1, p[0].characterGeneration, 2, p[1].characterGeneration)
			case "session":
				p[0].characterGeneration++
			case "channel":
				p[1].channel.ID = 20
				p[1].townActorOwnerChannel = 20
			case "guild":
				reg, _ := guild.Load(context.Background(), repos.Settings)
				delete(reg.Guilds, 1)
				if err := guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			case "location":
				c, _, _ := repos.Character.Load(context.Background(), "2")
				c.Stats["town_id"] = 0
				_ = repos.Character.Save(context.Background(), c)
			case "dungeon":
				p[1].dungeon.runtime = &runtimeDungeonState{}
			}
			sendGuildTeleport(t, s, p[1], 435, 20, []byte{answer})
			reg, err := guild.Load(context.Background(), repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			if len(reg.Teleports) != 0 {
				t.Fatal("rejected trip committed")
			}
			if g := reg.Guilds[1]; g != nil && g.Benefits[7].Value != 2 {
				t.Fatal("rejected trip charged")
			}
			for _, peer := range p {
				c, _, _ := repos.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
				if c.Stats["town_id"] == 39 {
					t.Fatal("partial move")
				}
			}
		})
	}
}

func TestGuildPartyTeleportDestinationQuestAndNoUsesRejectBeforePrompt(t *testing.T) {
	for _, mode := range []string{"quest", "no_uses", "unknown", "private", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := guildTeleportFixture(t, 2)
			body := teleportRequestBody(39, 0)
			switch mode {
			case "quest":
				body = teleportRequestBody(40, 0)
			case "no_uses":
				reg, _ := guild.Load(context.Background(), repos.Settings)
				reg.Guilds[1].Benefits[7] = guild.Benefit{Type: 7}
				_ = guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow())
			case "unknown":
				body[1] = 99
			case "private":
				body = teleportRequestBody(38, 1)
			case "malformed":
				body = body[:7]
			}
			sendGuildTeleport(t, s, p[0], 434, 10, body)
			ack := guildReply(t, p[0], 434)
			if len(ack) != 8 || ack[0] != 0 {
				t.Fatalf("native failure=%x", ack)
			}
			if p[0].guild.teleport != nil || p[1].guild.teleport != nil {
				t.Fatal("invalid request prompted")
			}
		})
	}
}

func TestGuildPartyTeleportNativeWireExport(t *testing.T) {
	path := os.Getenv("DNF_GUILD_TELEPORT_WIRES")
	if path == "" {
		t.Skip("set DNF_GUILD_TELEPORT_WIRES for native handoff")
	}
	rows := map[string]string{}
	for _, mode := range []string{"complete", "cancel", "timeout"} {
		s, p, _ := guildTeleportFixture(t, 2)
		sendGuildTeleport(t, s, p[0], 434, 10, teleportRequestBody(39, 0))
		rows["request"] = hex.EncodeToString(guildReply(t, p[1], 377))
		answer := byte(1)
		if mode == "cancel" {
			answer = 0
		}
		if mode == "timeout" {
			answer = 3
		}
		sendGuildTeleport(t, s, p[1], 435, 20, []byte{answer})
		rows[mode] = hex.EncodeToString(guildReply(t, p[1], 377))
	}
	s, p, _ := guildTeleportFixture(t, 2)
	sendGuildTeleport(t, s, p[0], 434, 10, teleportRequestBody(255, 255))
	rows["failure"] = hex.EncodeToString(guildReply(t, p[0], 434))
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

type guildTeleportFailedSend struct{ bufferConn }

func (*guildTeleportFailedSend) Write([]byte) (int, error) {
	return 0, errors.New("connection lost after commit")
}

func TestGuildPartyTeleportSendFailureKeepsCommittedDestination(t *testing.T) {
	s, p, repos := guildTeleportFixture(t, 2)
	sendGuildTeleport(t, s, p[0], 434, 10, teleportRequestBody(39, 0))
	p[1].conn = &guildTeleportFailedSend{}
	seq := uint64(65556)
	if err := s.handleCurrentGuildTeleportConfirm(p[1], gameplayRequest{Body: []byte{1}, Sequence: &seq}); err == nil {
		t.Fatal("write failure hidden")
	}
	for _, peer := range p {
		c, _, err := repos.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
		if err != nil || c.Stats["town_id"] != 39 {
			t.Fatal("committed destination rolled back", c, err)
		}
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil || reg.Guilds[1].Benefits[7].Value != 1 || len(reg.Teleports) != 1 {
		t.Fatal("completed trip not retained", err)
	}
	// A later move must win over a deferred projection of the old receipt.
	c, _, _ := repos.Character.Load(context.Background(), "1")
	c.Stats["town_id"] = 40
	_ = repos.Character.Save(context.Background(), c)
	p[0].conn.(*bufferConn).write.Reset()
	if err := s.handleTownSetUserAreaAfterGuildTeleport(p[0], buildCurrentSoloTeleportMoveBody(currentSoloTeleportRequest{TownID: 39, AreaID: 0, PositionX: 500, PositionY: 300, Direction: 5}, 39), guild.TeleportLocation{Town: 39, Area: 0, X: 500, Y: 300, Direction: 5}); err != nil {
		t.Fatal(err)
	}
	c, _, _ = repos.Character.Load(context.Background(), "1")
	if c.Stats["town_id"] != 40 || p[0].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("old projection overwrote newer scene")
	}
}

func TestGuildPartyTeleportPurchaseThreeTiersThroughNative761(t *testing.T) {
	s, p, repos := guildTeleportFixture(t, 2)
	source := cargoQuerySource()
	source["etc/(r)guild.etc"] = "[guild contents list]\n13 7 10 9999 1 10153534 0 300 `10`\n14 7 100 9999 1 10153535 0 2850 `100`\n15 7 1000 9999 1 10153536 0 27000 `1000`\n[/guild contents list]"
	source["etc/(r)guild.etc"] += "\n[additional guild contents list]\n22 21 300 1 0 100 0 1000 `contract` 1\n[/additional guild contents list]\n[guild cargo size limit]\n56"
	cat, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = cat
	reg, _ := guild.Load(context.Background(), repos.Settings)
	reg.Guilds[1].Mileage = 40000
	reg.Guilds[1].Benefits = nil
	if err := guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	for i, want := range []uint32{10, 110, 1110} {
		sendGuildPurchase(t, s, p[0], uint32(13+i), uint16(10+i))
		if ack := guildReply(t, p[0], 761); !bytes.Equal(ack, []byte{1}) {
			t.Fatalf("purchase %d ACK %x", i, ack)
		}
		reg, _ = guild.Load(context.Background(), repos.Settings)
		if reg.Guilds[1].Benefits[7].Value != want {
			t.Fatal("wrong uses")
		}
	}
	sendGuildPurchase(t, s, p[0], 15, 12)
	reg, _ = guild.Load(context.Background(), repos.Settings)
	if reg.Guilds[1].Mileage != 9850 || reg.Guilds[1].Benefits[7].Value != 1110 {
		t.Fatal("purchase replay charged again")
	}
	sendGuildTeleport(t, s, p[0], 434, 30, teleportRequestBody(39, 0))
	sendGuildTeleport(t, s, p[1], 435, 31, []byte{1})
	reg, _ = guild.Load(context.Background(), repos.Settings)
	if reg.Guilds[1].Benefits[7].Value != 1109 {
		t.Fatal("purchased entitlement not consumed")
	}
}
