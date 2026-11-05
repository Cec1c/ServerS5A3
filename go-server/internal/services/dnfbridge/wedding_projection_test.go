package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingReservationClosedRecipientStillSchedulesAndNotifiesSpouse(t *testing.T) {
	s, first, second, _ := weddingRoomLeaseFixture(t)
	defer s.stopCurrentWeddingCeremonyTimers()
	result := weddingCeremonyRecords()
	repos, _ := s.repositoryGroup()
	for _, c := range []r.CharacterRecord{result.Actor, result.Partner} {
		if err := r.SaveCharacterFields(context.Background(), repos.Character, c, append(r.AllCharacterFields(), r.CharacterFieldWedding)...); err != nil {
			t.Fatal(err)
		}
	}
	first.conn = &raidProjectionFailedConn{bufferConn: &bufferConn{}}
	a, _ := s.boundGameSessionCharacterSnapshot(first)
	b, _ := s.boundGameSessionCharacterSnapshot(second)
	if err := s.afterCurrentWeddingReservation(a, b, result); err == nil {
		t.Fatal("closed socket error missing")
	}
	key, _ := currentWeddingCeremonyKey(result.Actor, result.Partner)
	if s.wedding.ceremonies[key] == nil || s.wedding.ceremonies[key].timer == nil {
		t.Fatal("socket failure prevented durable reservation scheduling")
	}
	if body := guildReply(t, second, 1000); len(body) != 15 || binary.LittleEndian.Uint32(body) != 29 {
		t.Fatalf("spouse marriage state %x", body)
	}
	if body := guildReply(t, second, 1004); len(body) != 79 || binary.LittleEndian.Uint32(body) != 29 {
		t.Fatalf("spouse reservation state %x", body)
	}
}

func TestWeddingPeerInfoRefreshesOldActorWithoutBreakingGenericProjection(t *testing.T) {
	s, _, target, _ := weddingRoomLeaseFixture(t)
	repos, _ := s.repositoryGroup()
	ctx := context.Background()
	old, _, _ := repos.Character.Load(ctx, "29")
	s.wedding.rules.Rings = append(s.wedding.rules.Rings, wedding.Ring{Stage: 1})
	for _, id := range []string{"29", "30"} {
		c, _, _ := repos.Character.Load(ctx, id)
		c.Stats["wedding_v1_ring"] = 1
		if err := r.SaveCharacterFields(ctx, repos.Character, c, r.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
	}
	body, err := s.currentWeddingPeerInfo(ctx, old)
	if err != nil || len(body) != 14 || body[9] != 1 {
		t.Fatalf("old actor snapshot did not resolve current pair: %x %v", body, err)
	}
	// A damaged or concurrently changing relationship may defer its own
	// supplement, but cannot abort the base actor/party projection caller.
	c, _, _ := repos.Character.Load(ctx, "30")
	c.Stats["wedding_v1_ring"] = 0
	if err := r.SaveCharacterFields(ctx, repos.Character, c, r.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	if err := s.sendCurrentWeddingPeerInfo(ctx, target, old); err != nil {
		t.Fatalf("supplemental marriage state blocked generic projection: %v", err)
	}
}

func TestWeddingPeerRefreshStaysInsideConcreteVisibilityScope(t *testing.T) {
	for _, scenario := range []string{"same_room", "other_instance", "other_channel"} {
		t.Run(scenario, func(t *testing.T) {
			s, first, second, _ := weddingRoomLeaseFixture(t)
			p, _ := s.onlinePlayers.PlayerForCharacter(30)
			if scenario == "other_instance" {
				p.Instance++
			}
			if scenario == "other_channel" {
				second.channel.ID++
				second.residentChannel = second.channel
			}
			s.onlinePlayers.EnterArea(&p)
			a, _ := s.boundGameSessionCharacterSnapshot(first)
			repos, _ := s.repositoryGroup()
			c, _, _ := repos.Character.Load(context.Background(), "29")
			if err := s.broadcastCurrentWeddingPeerInfo(a, c); err != nil {
				t.Fatal(err)
			}
			ops, bodies := cargoReplies(t, second)
			if scenario == "same_room" {
				if len(ops) != 1 || ops[0] != 1013 || len(bodies[0]) != 14 || binary.LittleEndian.Uint32(bodies[0]) != 29 {
					t.Fatalf("visible actor refresh %v %x", ops, bodies)
				}
			} else if len(ops) != 0 {
				t.Fatalf("out-of-scope actor received marriage state: %v", ops)
			}
		})
	}
}
