package dnfbridge

import "testing"

func TestReviewParticipationDisconnectDuringReversibleRetryRemovesPosition(t *testing.T) {
	s, session, r, b := raidLifeRuntimeFixture(t)
	session.postStartMapPlayerStateSent = true
	s.recordCurrentRaidParticipation(session, r)
	if len(b.Participation) != 1 {
		t.Fatal("committed actor registration not exercised")
	}
	completePartyRetryFixture(t, s, session)
	r.settlementClearRewardSent = true
	r.settlementPhase = currentDungeonSettlementPhaseResultShown
	e := r.raidEntry
	b.ClearedRuns = map[string]currentRaidClearedRun{e.RunID: {DungeonID: uint32(r.Dungeon.ID), Phase: e.Phase, Attempt: e.Attempt, Participants: e.Participants}}
	retired, err := s.retireCompletedDungeonForTownAction(session, true, r)
	if err != nil || !retired || session.dungeon.runtime != nil || len(b.Participation) != 1 {
		t.Fatal("actual reversible retry retirement not exercised", retired, err)
	}
	s.shutdownGameSessionEvents(session, false)
	if len(b.Participation) != 0 {
		t.Fatal("disconnected actor remains in 584 journal because retry window has nil active runtime")
	}
}

func TestReviewParticipationOldDetachedRuntimeCannotRemoveReplacementSameRunOwner(t *testing.T) {
	s, session, old, b := raidLifeRuntimeFixture(t)
	session.postStartMapPlayerStateSent = true
	s.recordCurrentRaidParticipation(session, old)
	replacement := *old
	session.dungeon.runtime = &replacement
	s.recordCurrentRaidParticipation(session, &replacement)
	s.releaseCurrentRaidParticipation(old, session.selectedCharacterID)
	if len(b.Participation) != 1 {
		t.Fatal("detach of old object removed position belonging to replacement runtime")
	}
}

func TestReviewParticipationIgnoresUnownedGroupAndWrongFrozenChannel(t *testing.T) {
	for _, fault := range []string{"participant", "channel_type", "account"} {
		t.Run(fault, func(t *testing.T) {
			s, session, r, b := raidLifeRuntimeFixture(t)
			session.postStartMapPlayerStateSent = true
			switch fault {
			case "participant":
				r.raidEntry.Participants = []uint16{100}
			case "channel_type":
				b.ChannelType++
			case "account":
				b.MemberAccounts[session.selectedCharacterID] = "foreign"
			}
			s.recordCurrentRaidParticipation(session, r)
			if len(b.Participation) != 0 {
				t.Fatal("unowned actor appeared in raid positions")
			}
		})
	}
}
