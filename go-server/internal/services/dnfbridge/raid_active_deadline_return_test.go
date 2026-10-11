package dnfbridge

import (
	"testing"
	"time"
)

// Existing in-memory town-return fixture, not an actual Anton dungeon clear.
// Use its real synthetic dungeon ID consistently; no actual-PVF claim.
func deadlineReturnOwnerFixture(t *testing.T) (*Service, *gameSession, *runtimeDungeonState, *currentRaidDeadlineReturn) {
	t.Helper()
	s, session, r, b := raidLifeRuntimeFixture(t)
	r.Dungeon.ID = r.Session.Snapshot().Run.DungeonID
	b.TimedOut = true
	b.Deadline = time.Now().Add(-time.Second)
	state := s.raids[r.raidEntry.RaidKey]
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		t.Fatal("existing fixture must be canonically bound")
	}
	o := &currentRaidDeadlineReturn{Raid: state, Battle: b, Identity: identity, Runtime: r, InstanceID: b.InstanceID, Phase: b.Phase, Attempt: r.raidEntry.Attempt, RunID: r.raidEntry.RunID, DungeonID: uint32(r.Dungeon.ID), AccountID: r.AccountID, ChannelID: b.ChannelID, ChannelType: b.ChannelType, Deadline: b.Deadline}
	b.Participation = map[uint16]currentRaidParticipation{identity.character: {Session: session, Runtime: r, Generation: identity.generation, RunID: o.RunID, DungeonID: o.DungeonID, Phase: o.Phase, Attempt: o.Attempt}}
	return s, session, r, o
}

func TestRaidDeadlineOwnerRequiresExactActiveIdentity(t *testing.T) {
	for _, kind := range []string{"valid", "generation", "account", "channel", "type", "runtime", "run", "instance", "phase", "deadline", "not_timeout", "accepted", "registration", "registered_generation"} {
		t.Run(kind, func(t *testing.T) {
			s, session, r, o := deadlineReturnOwnerFixture(t)
			switch kind {
			case "generation":
				session.characterGeneration++
			case "account":
				session.accountID = "foreign"
			case "channel":
				session.channel.ID++
			case "type":
				session.channel.Type++
			case "runtime":
				copy := *r
				session.dungeon.runtime = &copy
			case "run":
				r.raidEntry.RunID = "foreign"
			case "instance":
				o.Battle.InstanceID = "foreign"
			case "phase":
				o.Battle.Phase++
			case "deadline":
				o.Battle.Deadline = o.Battle.Deadline.Add(time.Second)
			case "not_timeout":
				o.Battle.TimedOut = false
			case "registration":
				delete(o.Battle.Participation, o.Identity.character)
			case "registered_generation":
				entry := o.Battle.Participation[o.Identity.character]
				entry.Generation++
				o.Battle.Participation[o.Identity.character] = entry
			case "accepted":
				o.Battle.ClearedRuns = map[string]currentRaidClearedRun{o.RunID: {DungeonID: o.DungeonID}}
			}
			session.dungeon.mu.Lock()
			valid := s.currentRaidDeadlineReturnValid(o)
			session.dungeon.mu.Unlock()
			if valid != (kind == "valid") {
				t.Fatalf("%s validity=%v", kind, valid)
			}
		})
	}
}
