package dnfbridge

import (
	"bytes"
	"sync"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func TestPartyCardReservationsSerializeConcurrentCollisions(t *testing.T) {
	t.Setenv(dungeonPartyCardFlipProjectionEnv, "vendor")
	instance := &dungeonPartyInstance{}
	var wait sync.WaitGroup
	var got [4]byte
	for index := range got {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			card, ok := reserveCurrentDungeonPartyCard(instance, dungeonCardSideFree, 0, uint16(index+1))
			if !ok {
				t.Errorf("member %d reservation refused", index)
			}
			got[index] = card
		}(index)
	}
	wait.Wait()
	seen := map[byte]bool{}
	for index, card := range got {
		if seen[card] {
			t.Fatalf("card %d reserved twice: %v", card, got)
		}
		seen[card] = true
		if replay, ok := reserveCurrentDungeonPartyCard(instance, dungeonCardSideFree, 3, uint16(index+1)); !ok || replay != card {
			t.Fatalf("member %d replay moved from %d to %d", index, card, replay)
		}
	}
	if _, ok := reserveCurrentDungeonPartyCard(instance, dungeonCardSideFree, 0, 5); ok {
		t.Fatal("fifth free card reserved")
	}
	if card, ok := reserveCurrentDungeonPartyCard(instance, dungeonCardSidePaid, 0, 5); !ok || card != 0 {
		t.Fatal("paid row not independent")
	}
}

func TestPartyAutomaticCardsPreserveBothOwnersAndTheirRewards(t *testing.T) {
	for _, collide := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "same_manual_card"}[collide], func(t *testing.T) {
			t.Setenv(dungeonPartyCardFlipProjectionEnv, "vendor")
			t.Setenv(dungeonPartyCardFlipRowModelEnv, "vendor")
			t.Setenv(dungeonPartyCardFreeRowRewardEnv, "on")
			service, leader, follower, leaderRuntime, followerRuntime, scene := partyDungeonInstanceFixture(t, 41, 464)
			leader.dungeon.runtime, follower.dungeon.runtime = leaderRuntime, followerRuntime
			leader.connID, follower.connID = "card-leader", "card-follower"
			instance := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene)
			if instance == nil || service.adoptPartyDungeonInstance(follower, followerRuntime, scene) != instance {
				t.Fatal("missing shared instance")
			}
			queue := newFakeCurrentDungeonDeathTimerQueue()
			service.gameplayTimers = queue
			for index, runtime := range []*runtimeDungeonState{leaderRuntime, followerRuntime} {
				state, err := newDungeonCardState(dungeonCardRewardPlan{
					ID: []string{"leader-card", "follower-card"}[index], CharacterID: []string{"1001", "1002"}[index],
					Source: "party_card_reservation_test",
					Sides:  [dungeonCardSideCount]dungeonCardRewardBundle{{Gold: int64(117 + 110*index)}},
				})
				if err != nil {
					t.Fatal(err)
				}
				runtime.settlementCardRewardState = state
				runtime.settlementCardLayoutSent, runtime.settlementClearRewardSent = true, true
				runtime.advanceSettlementPhase(currentDungeonSettlementPhaseCardsRevealed)
			}
			for _, session := range []*gameSession{leader, follower} {
				session.conn.(*bufferConn).write.Reset()
			}
			if collide {
				for _, session := range []*gameSession{leader, follower} {
					if err := service.handleDungeonSelectCard(session, []byte{0, 0}); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				service.armCurrentDungeonCardAutoFlipLocked(leader, leaderRuntime)
				service.armCurrentDungeonCardAutoFlipLocked(follower, followerRuntime)
				queue.fire(queue.task(t, 0), false)
				queue.fire(queue.task(t, 1), false)
			}
			if leaderRuntime.settlementCardSideMember[0] != 0 || followerRuntime.settlementCardSideMember[0] != 1 {
				t.Fatalf("both members selected the same card: %v/%v", leaderRuntime.settlementCardSideMember, followerRuntime.settlementCardSideMember)
			}
			slots := currentDungeonPartyCardFlipSlots(instance)
			if slots[0].StateA != 0 || slots[1].StateA != 1 || len(slots[0].Rewards) != 1 || len(slots[1].Rewards) != 1 {
				t.Fatalf("owner or reward lost: %+v", slots)
			}
			want, err := buildCurrentDungeonOp71SuccessBody(slots)
			if err != nil {
				t.Fatal(err)
			}
			for _, session := range []*gameSession{leader, follower} {
				stream := session.conn.(*bufferConn).write.Bytes()
				var last []byte
				for len(stream) > 0 {
					packet, tail := splitGameServerUpperPacket(t, stream)
					if packet.Header.MsgID == uint16(dnfenum.CmdPacketSelectCard) {
						last = packet.Body
					}
					stream = tail
				}
				if !bytes.Equal(last, want) {
					t.Fatalf("char %d has inconsistent final card face: %x want %x", session.selectedCharacterID, last, want)
				}
			}
			if slots[0].Rewards[0].ValueB != 117 || slots[1].Rewards[0].ValueB != 227 {
				t.Fatalf("rewards overwritten: %+v", slots)
			}
		})
	}
}
