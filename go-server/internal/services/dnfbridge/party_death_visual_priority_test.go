package dnfbridge

import (
	"testing"
)

// A busy leader can have reward callbacks ahead of the mailbox wakeup. All
// already committed visual notices must be written before those callbacks,
// without advancing the recipient's scene or awarding anything early.
func TestPartyDeathVisualsPassOlderRewardCallbacksOnOwner(t *testing.T) {
	for _, leaderReceives := range []bool{false, true} {
		s, leader, member, runtime, _ := partyDungeonDeathFixture(t, 3)
		from, peer := leader, member
		if leaderReceives {
			from, peer = member, leader
			runtime = member.dungeon.runtime
		}
		c := peer.conn.(*bufferConn)
		c.write.Reset()
		release := holdPartyDeathRecipient(t, s, peer, nil)
		observed := make(chan int, 1)
		if err := s.tryPostGameSessionCharacterEvent(peer, "older-reward-work", peer.selectedCharacterID, peer.characterGeneration, func() error {
			observed <- len(splitUpperFrames(t, c.write.Bytes()))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for _, monster := range runtime.Room.Snapshot().Monsters[:2] {
			body, err := buildCurrentDungeonDeathNotificationBody(monster.ObjectKey, currentDungeonDeathResponseMonster)
			if err != nil {
				t.Fatal(err)
			}
			if s.broadcastPartyDungeonInstanceDeathNotification(from, runtime, monster.ObjectKey, body, "priority-test", partyDeathVisualOnly) != 1 {
				t.Fatal("not queued")
			}
		}
		release()
		drainPartyDeathRecipient(t, s, peer)
		if n := <-observed; n != 2 {
			t.Fatalf("leader=%t: only %d visual notices preceded older reward callback", leaderReceives, n)
		}
		frames := splitUpperFrames(t, c.write.Bytes())
		if len(frames) != 2 || frames[0].msgID != 38 || frames[1].msgID != 38 {
			t.Fatal("missing/duplicate death")
		}
		scene, ok := peer.dungeon.runtime.Session.Scene()
		if !ok || scene.Cleared || len(scene.DefeatedObjects) != 0 {
			t.Fatal("visual notice changed scene progression")
		}
	}
}
