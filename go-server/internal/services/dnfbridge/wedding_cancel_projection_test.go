package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func weddingCancellationProjectionFixture(t *testing.T) (*Service, []*gameSession, wedding.Owner, wedding.PairRequest) {
	t.Helper()
	s, p, repos := guildFoundationFixture(t)
	s.options.currentClientVersion = "official-24.9"
	s.onlinePlayers = newOnlinePlayerManager()
	for i, peer := range p[:2] {
		peer.channel = channelcatalog.Channel{ID: 10}
		c, _, _ := repos.Character.Load(context.Background(), strconv.Itoa(i+1))
		c.Job = []string{"0", "11"}[i]
		if err := repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(context.Background(), r.InventoryRecord{CharacterID: c.CharacterID, Slots: map[string]r.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Equipment.Save(context.Background(), r.EquipmentRecord{CharacterID: c.CharacterID, Entries: map[string]r.EquipmentEntry{}}); err != nil {
			t.Fatal(err)
		}
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: peer.selectedCharacterID, Session: peer, TownID: 1, AreaID: 2, ChannelID: 10})
	}
	o := wedding.Owner{Repositories: repos, Rules: &wedding.Rules{MinimumLevel: 30, EngagementCancellationGold: 500000, QuestIDs: []int64{20737, 20738}}}
	req := wedding.PairRequest{Actor: wedding.Actor{AccountID: "a1", CharacterID: "1"}, Partner: wedding.Actor{AccountID: "a2", CharacterID: "2"}, Now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	if _, err := o.Engage(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := o.CancelEngagement(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for _, peer := range p {
		peer.conn.(*bufferConn).write.Reset()
	}
	return s, p, o, req
}

func TestWeddingCancellationProjectionUsesNewEngagementAfterReceiverCollection(t *testing.T) {
	s, p, o, req := weddingCancellationProjectionFixture(t)
	actor, _ := s.boundGameSessionCharacterSnapshot(p[0])
	receivers := s.currentWeddingPeerReceivers(actor)
	if len(receivers) != 1 {
		t.Fatalf("receivers=%v", receivers)
	}
	// The cancellation has committed, but its peer projection has not. A new
	// engagement wins the wedding mutex before the delayed projection resumes.
	req.Now = req.Now.Add(time.Second)
	s.wedding.mu.Lock()
	_, err := o.Engage(context.Background(), req)
	s.wedding.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.publishCurrentWeddingCancellationPeerState(actor, receivers); err != nil {
		t.Fatal(err)
	}
	ops, bodies := cargoReplies(t, p[1])
	if len(ops) != 1 || ops[0] != 1013 || len(bodies[0]) != 14 || bodies[0][8] != wedding.Engaged || bodies[0][9] != 0 || binary.LittleEndian.Uint32(bodies[0][4:]) != 2 {
		t.Fatalf("stale cancellation overwrote new relationship %v %x", ops, bodies)
	}
}

func TestWeddingCancellationProjectionRejectsRetiredActorAndReceiver(t *testing.T) {
	for _, change := range []struct {
		name       string
		who        int
		disconnect bool
	}{{"actor-generation", 0, false}, {"receiver-generation", 1, false}, {"actor-disconnected", 0, true}, {"receiver-disconnected", 1, true}} {
		t.Run(change.name, func(t *testing.T) {
			s, p, _, _ := weddingCancellationProjectionFixture(t)
			actor, _ := s.boundGameSessionCharacterSnapshot(p[0])
			receivers := s.currentWeddingPeerReceivers(actor)
			s.mu.Lock()
			if change.disconnect {
				delete(s.gameSessions, p[change.who].selectedCharacterID)
			} else {
				p[change.who].characterGeneration++
			}
			s.mu.Unlock()
			if err := s.publishCurrentWeddingCancellationPeerState(actor, receivers); err != nil {
				t.Fatal(err)
			}
			ops, bodies := cargoReplies(t, p[1])
			if len(ops) != 0 {
				t.Fatalf("retired projection delivered %v %x", ops, bodies)
			}
		})
	}
}

func TestWeddingCancellationProjectionDoesNotAcquireDungeonLock(t *testing.T) {
	s, p, _, _ := weddingCancellationProjectionFixture(t)
	actor, _ := s.boundGameSessionCharacterSnapshot(p[0])
	receivers := s.currentWeddingPeerReceivers(actor)
	p[0].dungeon.mu.Lock()
	defer p[0].dungeon.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- s.publishCurrentWeddingCancellationPeerState(actor, receivers) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("serialized state publish tried to acquire dungeon lock")
	}
}
