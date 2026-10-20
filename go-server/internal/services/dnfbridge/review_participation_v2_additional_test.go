package dnfbridge

import "testing"

// Exercises the production deferred placement release boundary using an owned
// active runtime in the in-memory world-map fixture, not a live raid UI claim.
func TestReviewParticipationDeferredPlacementActuallyRegistersAfterRelease(t *testing.T) {
	s, session, r, b := raidLifeRuntimeFixture(t)
	session.postStartMapPlayerStateSent = false
	session.pendingPostStartMapPlacement = &pendingPostStartMapPlacement{Runtime: r, Source: "select_dungeon_after_op29_commit"}
	s.recordCurrentRaidParticipation(session, r)
	if len(b.Participation) != 0 {
		t.Fatal("entry tail registered actor before deferred placement committed")
	}
	if err := s.sendPendingPostStartMapPlayerPlacement(session, "party_dungeon_shared_loading_barrier"); err != nil {
		t.Fatal(err)
	}
	if !session.postStartMapPlayerStateSent || session.pendingPostStartMapPlacement != nil {
		t.Fatal("actual successful placement release was not exercised")
	}
	if len(b.Participation) != 1 || b.Participation[session.selectedCharacterID].Runtime != r {
		t.Fatal("owned actor was never registered after deferred placement actually succeeded")
	}
}

func TestReviewParticipationActualOldShutdownCannotEraseFreshCommittedOwner(t *testing.T) {
	s, old, r, b := raidLifeRuntimeFixture(t)
	old.postStartMapPlayerStateSent = true
	s.recordCurrentRaidParticipation(old, r)
	_, fresh, replacement := newBackToVillageRuntime(t)
	fresh.accountID, fresh.loginPacketSeen, fresh.accountFromToken = "account-1", true, true
	fresh.channel.ID, fresh.channel.Type = 23, 23
	s.bindGameSessionCharacter(fresh, 99)
	e := *r.raidEntry
	replacement.raidEntry = &e
	replacement.Dungeon, replacement.AccountID, replacement.Character = r.Dungeon, r.AccountID, r.Character
	fresh.postStartMapPlayerStateSent = false
	fresh.pendingPostStartMapPlacement = &pendingPostStartMapPlacement{Runtime: replacement, Source: "select_dungeon_after_op29_commit"}
	if err := s.sendPendingPostStartMapPlayerPlacement(fresh, "party_dungeon_shared_loading_barrier"); err != nil {
		t.Fatal(err)
	}
	// Explicit recording is the control here; the separate deferred test verifies
	// automatic entry registration rather than hiding its absence.
	s.recordCurrentRaidParticipation(fresh, replacement)
	if b.Participation[99].Session != fresh || b.Participation[99].Runtime != replacement {
		t.Fatal("fresh placement did not provide a valid committed owner control")
	}
	old.dungeon.runtime = nil
	s.shutdownGameSessionEvents(old, false)
	if b.Participation[99].Session != fresh || b.Participation[99].Runtime != replacement {
		t.Fatal("actual shutdown of old nil-runtime socket erased fresh owner")
	}
}
