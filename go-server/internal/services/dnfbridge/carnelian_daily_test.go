package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func carnelianBridgeCatalog(t *testing.T, area int64) *dnfquest.Catalog {
	t.Helper()
	source := bridgePVFSource{dnfquest.DefaultList: "13056 `carnelian.qst`\n", "n_quest/carnelian.qst": fmt.Sprintf("[name]\n`地区每日使命`\n[grade]\n`[auto daily mission]`\n[npc index]\n582\n[complete npc index]\n-1\n[level]\n1 99\n[job]\n`[all]`\n[type]\n`[hunt monster]`\n[int data]\n-4 %d -1 -1 150 -4 %d -1 -3 3\n[/int data]\n[reward type]\n`[item]`\n[reward int data]\n10163959 15\n", area, area)}
	idx, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := dnfquest.Load(context.Background(), idx)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func carnelianBridgeAccept(t *testing.T, s *Service, p *gameSession) {
	t.Helper()
	p.initialTownRouteCharacterID = p.selectedCharacterID
	p.initialTownRouteStage = currentInitialTownRoutePlayerStateSent
	body := []byte{31, 0, 0, 0}
	binary.LittleEndian.PutUint16(body[2:], 13056)
	if err := s.handleCurrentAcceptQuest(p, body); err != nil {
		t.Fatal(err)
	}
	g, _ := s.repositoryGroup()
	s.options.currentClientVersion = "official-24.9"
	q, _, err := g.Quest.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
	if err != nil || q.States[13056].ProgressValue != 1686 || q.States[13056].Status != "active" {
		t.Fatalf("accept state=%+v err=%v", q.States[13056], err)
	}
}

func carnelianBridgeState(t *testing.T, g r.Group, id uint16) r.QuestState {
	t.Helper()
	q, ok, err := g.Quest.Load(context.Background(), strconv.Itoa(int(id)))
	if err != nil || !ok {
		t.Fatalf("quest load=%t err=%v", ok, err)
	}
	return q.States[13056]
}

func TestCarnelianBridgeDeathPartyAndClientEcho(t *testing.T) {
	s, leader, peer, rt, _ := partyDungeonDeathFixture(t, func(s *Service, l, p *gameSession) {
		s.questCatalog = carnelianBridgeCatalog(t, 1)
		carnelianBridgeAccept(t, s, l)
		carnelianBridgeAccept(t, s, p)
	})
	g, _ := s.repositoryGroup()
	body := []byte{33, 0, 0, 0, 16, 0}
	binary.LittleEndian.PutUint16(body[2:], 13056)
	echo := func(p *gameSession, want int64) {
		t.Helper()
		conn := p.conn.(*bufferConn)
		conn.write.Reset()
		if err := s.handleCurrentSetQuestTrigger(p, body); err != nil {
			t.Fatal(err)
		}
		ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if ack.Header.MsgID != 33 || len(rest) != 0 || len(ack.Body) != 7 || ack.Body[0] != 1 || int64(binary.LittleEndian.Uint32(ack.Body[3:])) != want {
			t.Fatalf("echo ack=%+v rest=%x", ack, rest)
		}
		if got := carnelianBridgeState(t, g, p.selectedCharacterID).ProgressValue; got != want {
			t.Fatalf("echo changed remaining=%d", got)
		}
		conn.write.Reset()
		if err := s.handleCurrentSetQuestTrigger(p, body); err != nil {
			t.Fatal(err)
		}
		if conn.write.Len() != 0 {
			t.Fatal("unchanged echo repeated a feedback packet")
		}
	}
	echo(leader, 1686)
	monster := rt.Room.Snapshot().Monsters[0]
	if err := qst017Death(s, leader, 65000); err != nil {
		t.Fatal(err)
	}
	if carnelianBridgeState(t, g, leader.selectedCharacterID).ProgressValue != 1686 {
		t.Fatal("unknown death counted")
	}
	if err := qst017Death(s, leader, monster.ObjectKey); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*gameSession{leader, peer} {
		if got := carnelianBridgeState(t, g, p.selectedCharacterID).ProgressValue; got != 1685 {
			t.Fatalf("member %d remaining=%d", p.selectedCharacterID, got)
		}
		echo(p, 1685)
	}
	// The peer's own report of this shared death must not count it again.
	if err := qst017Death(s, peer, monster.ObjectKey); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*gameSession{leader, peer} {
		if carnelianBridgeState(t, g, p.selectedCharacterID).ProgressValue != 1685 {
			t.Fatal("shared replay changed progress")
		}
	}
}

func TestCarnelianBridgeDeathCategoryAndArea(t *testing.T) {
	for _, tc := range []struct {
		name, rank, suffix string
		area, want         int64
	}{
		{"ordinary", "[normal]", "", 1, 1685},
		{"boss", "[boss]", "", 1, 1174},
		{"boss suffix", "[normal]", "[boss]", 1, 1174},
		{"champion", "[champion]", "", 1, 1686},
		{"unknown", "[unsupported]", "", 1, 1686},
		{"different region", "[normal]", "", 39, 1686},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, g, key := qst017Fixture(t, 1)
			s.questCatalog = carnelianBridgeCatalog(t, tc.area)
			carnelianBridgeAccept(t, s, p)
			rt := p.dungeon.runtime
			rt.Room.mu.Lock()
			rt.Room.monsters[0].Spawn.Rank = tc.rank
			rt.Room.monsters[0].Spawn.SuffixMarker = tc.suffix
			rt.Room.mu.Unlock()
			// Commit a real room death through the runtime, then invoke the same
			// persistence boundary; unknown rank must not become ordinary by default.
			if _, _, err := rt.Room.CommitActorDeathReport(key, rt.Session); err != nil {
				t.Fatal(err)
			}
			if err := s.persistCurrentDungeonHuntMonsterDeathLocked(p, rt, key); err != nil {
				t.Fatal(err)
			}
			if got := carnelianBridgeState(t, g, p.selectedCharacterID).ProgressValue; got != tc.want {
				t.Fatalf("remaining=%d want=%d", got, tc.want)
			}
		})
	}
}

func TestCarnelianAcceptWaitsForInitialTownRoute(t *testing.T) {
	g := qst006Repositories(t, context.Background())
	conn, s, p := qst006Bridge(g, carnelianBridgeCatalog(t, 1), nil, nil)
	body := []byte{31, 0, 0, 0}
	binary.LittleEndian.PutUint16(body[2:], 13056)
	if err := s.handleCurrentAcceptQuest(p, body); err != nil {
		t.Fatal(err)
	}
	q, _, _ := g.Quest.Load(context.Background(), "19")
	if _, ok := q.States[13056]; ok || conn.write.Len() != 0 {
		t.Fatal("early auto accept bypassed initial town gate")
	}
	carnelianBridgeAccept(t, s, p)
}

func TestCarnelianForcedCleanupDoesNotCountAsKill(t *testing.T) {
	s, p, g, key := qst017Fixture(t, 1)
	s.questCatalog = carnelianBridgeCatalog(t, 1)
	carnelianBridgeAccept(t, s, p)
	rt := p.dungeon.runtime
	if _, _, err := rt.Room.CommitRemainingAnnouncedHostilesAfterBoss(0, rt.Session); err != nil {
		t.Fatal(err)
	}
	if rt.Room.HasCombatDeath(key) {
		t.Fatal("forced cleanup recorded a combat kill")
	}
	if err := s.persistCurrentDungeonHuntMonsterDeathLocked(p, rt, key); err != nil {
		t.Fatal(err)
	}
	if got := carnelianBridgeState(t, g, 19).ProgressValue; got != 1686 {
		t.Fatalf("forced kill progress=%d", got)
	}
}

func TestCarnelianPartyQueuedKillRevalidatesRecipient(t *testing.T) {
	for _, mode := range []string{"current", "different_room", "different_character"} {
		t.Run(mode, func(t *testing.T) {
			s, l, p, rt, _ := partyDungeonDeathFixture(t, func(s *Service, l, p *gameSession) {
				s.questCatalog = carnelianBridgeCatalog(t, 1)
				carnelianBridgeAccept(t, s, l)
				carnelianBridgeAccept(t, s, p)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.ctx = ctx
			if err := s.startGameSessionEvents(ctx, p); err != nil {
				t.Fatal(err)
			}
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer func() { cancel(); stopTestGameSessionEvents(t, s, p) }()
			if err := s.tryPostGameSessionCharacterEvent(p, "test-hunt-gate", p.selectedCharacterID, p.characterGeneration, func() error {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			waitTestSignal(t, entered, "hunt gate did not start")
			key := rt.Room.Snapshot().Monsters[0].ObjectKey
			if _, _, err := rt.Room.CommitActorDeathReport(key, rt.Session); err != nil {
				t.Fatal(err)
			}
			s.dispatchCurrentDungeonHuntDeathToPeersLocked(l, rt, key)
			p.dungeon.mu.Lock()
			if mode == "different_room" {
				p.dungeon.runtime.Room = &runtimeDungeonRoom{}
			}
			p.dungeon.mu.Unlock()
			if mode == "different_character" {
				s.bindGameSessionCharacter(p, 101)
			}
			if err := s.tryPostGameSessionCharacterEvent(p, "test-hunt-flush", p.selectedCharacterID, p.characterGeneration, func() error { close(done); return nil }); err != nil {
				t.Fatal(err)
			}
			close(release)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("hunt queue did not drain")
			}
			g, _ := s.repositoryGroup()
			want := int64(1686)
			if mode == "current" {
				want--
			}
			if got := carnelianBridgeState(t, g, 100).ProgressValue; got != want {
				t.Fatalf("queued %s progress=%d want=%d", mode, got, want)
			}
		})
	}
}
