package dnfbridge

import "testing"

func TestReviewExitV3NonleaderOtherSelectorRejectsWithoutSuccessAck(t *testing.T) {
	s, leader, peer := reviewExitV2Party(t)
	oldA, oldB := leader.dungeon.runtime, peer.dungeon.runtime
	conn := &bufferConn{}
	peer.conn = conn
	if err := s.handleDungeonEplpCommand(peer, []byte{1, 1}); err != nil {
		t.Fatalf("invalid nonleader selector produced handler error: %v", err)
	}
	if conn.write.Len() != 0 || leader.dungeon.runtime != oldA || peer.dungeon.runtime != oldB {
		t.Fatal("nonleader selector acknowledged or detached group before ownership gate")
	}
}

func TestReviewExitV3ActualRetirementMarksAndRestoresOnlyMatchingOwners(t *testing.T) {
	s, leader, peer := reviewExitV2Party(t)
	oldA, oldB := leader.dungeon.runtime, peer.dungeon.runtime
	leader.preDungeonContextPlayerStateSent = true
	peer.preDungeonContextPlayerStateSent = true
	owners, err := s.captureCurrentRaidRetryReturnOwners(leader, oldA)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range owners {
		if o.retired {
			t.Fatal("capture alone authorized resurrection")
		}
	}
	retired, err := s.retireCompletedPartyForRetry(leader, oldA, owners)
	if err != nil || !retired || leader.dungeon.runtime != nil || peer.dungeon.runtime != nil {
		t.Fatal("actual whole group retirement not exercised", retired, err)
	}
	for _, o := range owners {
		if !o.retired {
			t.Fatal("successful retry retirement lost restoration permit")
		}
	}
	s.restoreCurrentRaidRetryReturnOwners(leader, owners)
	if leader.dungeon.runtime != oldA || peer.dungeon.runtime != oldB || oldA.partyInstance == nil || oldA.partyInstance != oldB.partyInstance || !leader.preDungeonContextPlayerStateSent || !peer.preDungeonContextPlayerStateSent {
		t.Fatal("matching retired owners or dungeon gates not restored")
	}
}

func TestReviewExitV3ReplacementCompletedPeerRuntimeCannotBeRetiredForCapturedOldOwner(t *testing.T) {
	s, leader, peer := reviewExitV2Party(t)
	oldA, oldB := leader.dungeon.runtime, peer.dungeon.runtime
	owners, err := s.captureCurrentRaidRetryReturnOwners(leader, oldA)
	if err != nil {
		t.Fatal(err)
	}
	replacement := *oldB
	peer.dungeon.runtime = &replacement // Same completed metadata and party; distinct owner.
	retired, err := s.retireCompletedPartyForRetry(leader, oldA, owners)
	if retired || err == nil || leader.dungeon.runtime != oldA || peer.dungeon.runtime != &replacement {
		t.Fatal("captured old pointer allowed retirement of replacement runtime", retired, err)
	}
	for _, o := range owners {
		if o.retired {
			t.Fatal("rejected group acquired restoration permits")
		}
	}
}
