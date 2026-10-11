package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The ordinary active dungeon/town-return fixture is reused only in memory.
// Raid identity and native request acceptance are the boundary under test;
// this fixture is not represented as a player clearing an actual Anton map.
func raidLifeRuntimeFixture(t *testing.T) (*Service, *gameSession, *runtimeDungeonState, *runtimeRaidBattle) {
	t.Helper()
	s, session, r := newBackToVillageRuntime(t)
	s.options.currentClientVersion = "official-24.9"
	session.accountID, session.loginPacketSeen, session.accountFromToken = "account-1", true, true
	session.channel.ID, session.channel.Type = 23, 23
	s.bindGameSessionCharacter(session, 99)
	s.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	r.Dungeon.ID = 210
	now := time.Now()
	b := &runtimeRaidBattle{InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, ChannelID: 23, ChannelType: 23, EnterAfter: now.Add(-time.Second), Deadline: now.Add(time.Hour),
		Configuration: raid.BattleConfiguration{PhaseLimitSeconds: []uint32{3600, 3600}, WeeklyClearLimit: 2}, Dungeons: []raid.DungeonState{{ID: 210, State: 0}}, MemberAccounts: map[uint16]string{99: "account-1"}}
	state := &runtimeRaidState{RaidKey: 23<<24 | 1<<16 | 99, LeaderID: 99, Started: true, CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 99, GroupIndex: 1}}, Battle: b}
	s.raids = map[uint32]*runtimeRaidState{state.RaidKey: state}
	r.raidEntry = &currentRaidDungeonEntry{RaidKey: state.RaidKey, InstanceID: b.InstanceID, Phase: 0, Attempt: 1, ChannelID: 23, ChannelType: 23, RunID: "112233445566778899aabbccddeeff00", Participants: []uint16{99}}
	return s, session, r, b
}

func TestRaidPhaseDeathAcceptedOp40AndCommittedOp41Lifecycle(t *testing.T) {
	s, session, r, b := raidLifeRuntimeFixture(t)
	die := func() {
		t.Helper()
		if err := s.handleDungeonDieCharacter(session, make([]byte, 4)); err != nil {
			t.Fatal(err)
		}
	}
	die()
	key := currentRaidMemberRun{RunID: r.raidEntry.RunID, CharacterID: 99}
	if b.PhaseDeaths != [2]uint32{1, 0} || !b.MemberLives[key].Dead || !r.deathStateSent {
		t.Fatal("accepted death not journaled", b.PhaseDeaths)
	}
	die()
	if b.PhaseDeaths[0] != 1 || b.MemberLives[key].Deaths != 1 {
		t.Fatal("duplicate op40 counted twice")
	}
	wrong := []byte{100, 0}
	if err := s.handleDungeonUseCoin(session, wrong); err != nil {
		t.Fatal(err)
	}
	die()
	if b.PhaseDeaths[0] != 1 || !b.MemberLives[key].Dead {
		t.Fatal("unaccepted revive released death latch")
	}
	body := make([]byte, 2)
	binary.LittleEndian.PutUint16(body, currentSceneActorObjectKey(99))
	if err := s.handleDungeonUseCoin(session, body); err != nil {
		t.Fatal(err)
	}
	if b.MemberLives[key].Dead || r.deathStateSent || r.deathReturnWaiting {
		t.Fatal("committed native revive did not release death latch")
	}
	die()
	if b.PhaseDeaths != [2]uint32{2, 0} || b.MemberLives[key].Deaths != 2 {
		t.Fatal("new death after real revive not counted", b.PhaseDeaths)
	}
	copy := cloneRuntimeRaidBattle(b)
	copy.PhaseDeaths[0] = 90
	copy.MemberLives[key] = currentRaidMemberLife{}
	if b.PhaseDeaths[0] != 2 || !b.MemberLives[key].Dead {
		t.Fatal("snapshot aliased life authority")
	}
	if err := s.handleDungeonDieCharacter(session, nil); err != nil || b.PhaseDeaths[0] != 2 {
		t.Fatal("bad native request counted", err)
	}
}

type raidFailedReviveTransaction struct{}

func (raidFailedReviveTransaction) WithinCharacterItems(context.Context, string, func(dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return errors.New("injected revive transaction failure")
}

func TestRaidPhaseDeathReviveTransactionFailureKeepsLatchUntilCommit(t *testing.T) {
	s, session, r, b := raidLifeRuntimeFixture(t)
	if err := s.handleDungeonDieCharacter(session, make([]byte, 4)); err != nil {
		t.Fatal(err)
	}
	repos, _ := s.repositoryGroup()
	failed := repos
	failed.CharacterItems = raidFailedReviveTransaction{}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return failed, true }
	body := make([]byte, 2)
	binary.LittleEndian.PutUint16(body, 99)
	if err := s.handleDungeonUseCoin(session, body); err != nil {
		t.Fatal(err)
	}
	key := currentRaidMemberRun{RunID: r.raidEntry.RunID, CharacterID: 99}
	if !r.deathReturnWaiting || !r.deathStateSent || !b.MemberLives[key].Dead {
		t.Fatal("failed asset transaction forged a revive")
	}
	if err := s.handleDungeonDieCharacter(session, make([]byte, 4)); err != nil || b.PhaseDeaths[0] != 1 {
		t.Fatal("failure cleared latch", err)
	}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := s.handleDungeonUseCoin(session, body); err != nil {
		t.Fatal(err)
	}
	if err := s.handleDungeonDieCharacter(session, make([]byte, 4)); err != nil || b.PhaseDeaths[0] != 2 {
		t.Fatal("retry commit did not permit next death", err, b.PhaseDeaths)
	}
}

func TestRaidPhaseDeathRejectsUnownedOrExpiredLifeReports(t *testing.T) {
	for _, name := range []string{"unaccepted", "unsent", "old_runtime", "account", "login", "instance", "phase", "attempt", "channel", "participant", "completed_run", "closed_map", "phase_complete", "expired", "before_start", "zero_time", "journal_conflict"} {
		t.Run(name, func(t *testing.T) {
			s, session, r, b := raidLifeRuntimeFixture(t)
			r.deathReturnWaiting, r.deathStateSent = true, true
			now := time.Now()
			switch name {
			case "unaccepted":
				r.deathReturnWaiting = false
			case "unsent":
				r.deathStateSent = false
			case "old_runtime":
				session.dungeon.runtime = nil
			case "account":
				r.AccountID = "foreign"
			case "login":
				replacement := &gameSession{accountID: "account-1", selectedCharacterID: 99}
				s.bindGameSessionCharacter(replacement, 99)
			case "instance":
				r.raidEntry.InstanceID = "foreign"
			case "phase":
				r.raidEntry.Phase = 1
			case "attempt":
				r.raidEntry.Attempt = 2
			case "channel":
				session.channel.ID = 24
			case "participant":
				r.raidEntry.Participants = []uint16{100}
			case "completed_run":
				b.ClearedRuns = map[string]currentRaidClearedRun{r.raidEntry.RunID: {DungeonID: 210}}
			case "closed_map":
				b.Dungeons[0].State = 1
			case "phase_complete":
				b.PhaseCompleted = true
			case "expired":
				now = b.Deadline
			case "before_start":
				now = b.EnterAfter.Add(-time.Second)
			case "zero_time":
				now = time.Time{}
			case "journal_conflict":
				b.MemberLives = map[currentRaidMemberRun]currentRaidMemberLife{{RunID: r.raidEntry.RunID, CharacterID: 99}: {Phase: 1, Attempt: 1}}
			}
			session.dungeon.mu.Lock()
			update, err := s.recordCurrentRaidCharacterLifeLocked(session, r, true, now)
			session.dungeon.mu.Unlock()
			if err == nil || update.Changed || b.PhaseDeaths != [2]uint32{} {
				t.Fatal("unowned life changed count", err, update.Changed, b.PhaseDeaths)
			}
			if name == "expired" && (len(update.Projections) != 1 || !b.TimedOut) {
				t.Fatal("elapsed clock closure lost on life rejection")
			}
		})
	}
}

func TestRaidPhaseDeathInstanceJournalSurvivesRuntimeReplacementAndSaturates(t *testing.T) {
	s, session, r, b := raidLifeRuntimeFixture(t)
	r.deathReturnWaiting, r.deathStateSent = true, true
	session.dungeon.mu.Lock()
	update, err := s.recordCurrentRaidCharacterLifeLocked(session, r, true, time.Now())
	if err != nil || !update.Changed {
		t.Fatal(err)
	}
	// Replace only the local runtime, retaining the server-frozen run identity.
	copy := *r
	session.dungeon.runtime = &copy
	update, err = s.recordCurrentRaidCharacterLifeLocked(session, &copy, true, time.Now())
	if err != nil || update.Changed || b.PhaseDeaths[0] != 1 {
		t.Fatal("replacement runtime duplicated death", err)
	}
	update, err = s.recordCurrentRaidCharacterLifeLocked(session, &copy, false, time.Now())
	if err != nil || !update.Changed {
		t.Fatal(err)
	}
	b.PhaseDeaths[0] = math.MaxUint32
	key := currentRaidMemberRun{RunID: r.raidEntry.RunID, CharacterID: 99}
	life := b.MemberLives[key]
	life.Deaths = math.MaxUint32
	b.MemberLives[key] = life
	update, err = s.recordCurrentRaidCharacterLifeLocked(session, &copy, true, time.Now())
	session.dungeon.mu.Unlock()
	if err != nil || !update.Changed || b.PhaseDeaths[0] != math.MaxUint32 || b.MemberLives[key].Deaths != math.MaxUint32 {
		t.Fatal("death total wrapped", err)
	}
}

func TestRealPVFAntonPhaseDeathAdvancesElapsedResetBeforeOldRunReport(t *testing.T) {
	f, session, now := realAntonProjectionFixture(t, 0)
	b := f.state.Battle
	for n := 0; n < 4; n++ {
		next, _, err := b.Anton.ClearDungeon("death-smoke-"+string(rune('a'+n)), 210, b.Anton.Epoch(), now)
		if err != nil {
			t.Fatal(err)
		}
		b.Anton = next
	}
	b.Dungeons = b.Anton.Dungeons()
	_, r := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true, singleMonster: true})
	r.Dungeon.ID = 212
	r.AccountID = session.accountID
	r.Character = dnfrepo.CharacterRecord{CharacterID: "19", AccountID: session.accountID}
	r.deathReturnWaiting, r.deathStateSent = true, true
	r.raidEntry = &currentRaidDungeonEntry{RaidKey: f.state.RaidKey, InstanceID: b.InstanceID, Phase: 0, Attempt: b.Anton.Epoch(), ChannelID: session.channel.ID, ChannelType: session.channel.Type, RunID: "112233445566778899aabbccddeeff00", Participants: []uint16{19}}
	session.dungeon.runtime = r
	session.dungeon.mu.Lock()
	update, err := f.service.recordCurrentRaidCharacterLifeLocked(session, r, true, now.Add(301*time.Second))
	session.dungeon.mu.Unlock()
	if err == nil || update.Changed || len(update.Projections) != 1 || b.Anton.Epoch() != 2 || b.PhaseDeaths != [2]uint32{} {
		t.Fatal("delayed reset counted old attempt", err, len(update.Projections), b.PhaseDeaths)
	}
}
