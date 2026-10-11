package dnfbridge

import (
	"fmt"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Fixture uses the ordinary dungeon lifecycle, with completion confined to
// this test's in-memory session. It does not alter player/PVF/weekly data.
func raidCompletedRuntimeFixture(t *testing.T, owner *gameSession, id uint32) *runtimeDungeonState {
	t.Helper()
	_, r := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true, singleMonster: true})
	r.Dungeon.ID = int64(id)
	r.AccountID = owner.accountID
	r.Character = dnfrepo.CharacterRecord{CharacterID: fmt.Sprint(owner.selectedCharacterID), AccountID: owner.accountID}
	if err := r.Session.CompleteCurrentRoom(); err != nil {
		t.Fatal(err)
	}
	r.bossDieCheckAccepted, r.bossDieCheckResponseSent, r.settlementEntrySent = true, true, true
	owner.dungeon.runtime = r
	return r
}

func TestRaidOwnedSingleClearGuardsAndSequence(t *testing.T) {
	for _, name := range []string{"ready", "unfinished", "unaccepted", "unsent_response", "unsent_settlement", "foreign_account", "foreign_instance", "foreign_phase", "foreign_channel", "foreign_runtime", "foreign_attempt", "foreign_participant", "expired", "replaced_login", "closed_map"} {
		t.Run(name, func(t *testing.T) {
			f := raidSafetyFixture(t, nil, 1)
			f.service.options.currentClientVersion = "official-24.9"
			session := f.session()
			session.loginPacketSeen, session.accountFromToken = true, true
			f.service.bindGameSessionCharacter(session, 19)
			now := time.Now()
			b := &runtimeRaidBattle{InstanceID: "00112233445566778899aabbccddeeff", Single: true, Mode: 1, EnterAfter: now.Add(-time.Second), Deadline: now.Add(time.Hour),
				ChannelID: session.channel.ID, ChannelType: session.channel.Type,
				Configuration: raid.BattleConfiguration{WeeklyClearLimit: 2},
				Dungeons:      []raid.DungeonState{{ID: 5314, State: 0}, {ID: 5315, State: 1}}, SingleSequence: []uint32{5314, 5315}, MemberAccounts: map[uint16]string{19: "account-1"}}
			f.state.Started, f.state.Battle = true, b
			r := raidCompletedRuntimeFixture(t, session, 5314)
			if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, r, nil); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "unfinished":
				_, active := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true, singleMonster: true})
				r.Session = active.Session
			case "unaccepted":
				r.bossDieCheckAccepted = false
			case "unsent_response":
				r.bossDieCheckResponseSent = false
			case "unsent_settlement":
				r.settlementEntrySent = false
			case "foreign_account":
				r.AccountID = "other"
			case "foreign_instance":
				r.raidEntry.InstanceID = "other-instance"
			case "foreign_phase":
				r.raidEntry.Phase = 1
			case "foreign_channel":
				session.channel.ID = 24
			case "foreign_runtime":
				session.dungeon.runtime = nil
			case "foreign_attempt":
				r.raidEntry.Attempt = 2
			case "foreign_participant":
				r.raidEntry.Participants = []uint16{20}
			case "expired":
				now = b.Deadline
			case "replaced_login":
				peer := f.session()
				peer.loginPacketSeen, peer.accountFromToken = true, true
				f.service.bindGameSessionCharacter(peer, 19)
			case "closed_map":
				b.Dungeons[0].State = 1
			}
			snapshot, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, r, now)
			if name != "ready" {
				if err == nil || changed || b.SingleIndex != 0 || len(b.ClearedRuns) != 0 || len(b.MemberClears) != 0 {
					t.Fatal("invalid clear mutated instance", changed, err, b)
				}
				return
			}
			if err != nil || !changed || b.SingleIndex != 1 || b.Dungeons[0].State != 1 || b.Dungeons[1].State != 0 || b.MemberClears[19] != 1 || b.PhaseCompleted {
				t.Fatal("clear did not advance only next map", err, b)
			}
			snapshot.Battle.MemberClears[19] = 99
			for runID, run := range snapshot.Battle.ClearedRuns {
				run.Participants[0] = 20
				snapshot.Battle.ClearedRuns[runID] = run
			}
			if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, r, now); err != nil || changed || b.MemberClears[19] != 1 {
				t.Fatal("replayed clear changed progress", changed, err)
			}
			r.raidEntry = nil
			r.Dungeon.ID = 5315
			if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, r, nil); err != nil {
				t.Fatal(err)
			}
			if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, r, now); err != nil || !changed || !b.PhaseCompleted || b.SingleIndex != 2 || b.MemberClears[19] != 2 {
				t.Fatal("terminal single map failed", changed, err, b)
			}
			if f.service.currentRaidDungeonSelectionAllowed(session, 5315) {
				t.Fatal("completed single phase remained enterable")
			}
		})
	}
}

func TestRealPVFRaidPartyClearCountsOnceAndExpiredTimerIsCommitted(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current production PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	program, err := raid.LoadAntonProgram(archive)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, archive, 3)
	f.service.options.currentClientVersion = "official-24.9"
	leader, peer := f.session(), f.session()
	leader.loginPacketSeen, leader.accountFromToken = true, true
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.loginPacketSeen, peer.accountFromToken = true, true
	f.service.bindGameSessionCharacter(leader, 19)
	f.service.bindGameSessionCharacter(peer, 20)
	for i := range f.state.Members {
		f.state.Members[i].GroupIndex = 1
	}
	now := time.Now()
	anton, err := program.BeginPhase(0, now.Add(-time.Second), 87152)
	if err != nil {
		t.Fatal(err)
	}
	b := &runtimeRaidBattle{InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, EnterAfter: now.Add(-time.Second), Deadline: now.Add(time.Hour), Anton: anton,
		ChannelID: leader.channel.ID, ChannelType: leader.channel.Type,
		Dungeons: anton.Dungeons(), MemberAccounts: map[uint16]string{19: "account-1", 20: "account-20", 21: "account-21"}}
	f.state.Started, f.state.Battle = true, b
	for i := 1; i <= 4; i++ {
		l, p := raidCompletedRuntimeFixture(t, leader, 210), raidCompletedRuntimeFixture(t, peer, 210)
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(leader, l, []*runtimePartyPreparedDungeonEntry{{session: peer, runtime: p}}); err != nil {
			t.Fatal(err)
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(leader, l, now); err != nil || !changed {
			t.Fatal("leader clear failed", err)
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(peer, p, now); err != nil || changed {
			t.Fatal("follower counted same party twice", err)
		}
		if b.Anton.ClearCount(210) != uint32(i) || b.MemberClears[19] != uint32(i) || b.MemberClears[20] != uint32(i) || b.MemberClears[21] != 0 {
			t.Fatal("clear credited unrelated raid member", b.MemberClears)
		}
	}
	// Freeze a leg while open. Delaying its completion past the actual quake
	// deadline resets the phase; that elapsed reset must survive rejection.
	r := raidCompletedRuntimeFixture(t, leader, 213)
	if err := f.service.freezeCurrentRaidDungeonGroupEntry(leader, r, nil); err != nil {
		t.Fatal(err)
	}
	_, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(leader, r, now.Add(301*time.Second))
	if err == nil || !changed || b.Anton.Epoch() != 2 || b.Anton.ClearCount(210) != 0 || b.Anton.ClearCount(213) != 0 || b.MemberClears[19] != 4 {
		t.Fatal("expired leg clear failed to commit phase reset", changed, err, b.Anton.Epoch(), b.MemberClears)
	}
}
