package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
	"time"
)

func prepareSemesterActualReady(t *testing.T) (*Service, *gameSession, oe.EpicCampaign) {
	t.Helper()
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	actual, _, _, _ := prepareMonthlyBridge(t)
	ctx := context.Background()
	table, e := worldmap.LoadSource(ctx, actual.initialEquipmentArchive, worldmap.Options{})
	if e != nil {
		t.Fatal(e)
	}
	resolver, e := worldmap.NewResolver(table)
	if e != nil {
		t.Fatal(e)
	}
	monsters, e := newPVFDungeonMonsterCatalog(actual.initialEquipmentArchive)
	if e != nil {
		t.Fatal(e)
	}
	ai, e := newPVFDungeonAICharacterCatalog(actual.initialEquipmentArchive)
	if e != nil {
		t.Fatal(e)
	}
	s, a, old := newBackToVillageRuntime(t)
	if e = old.Session.Abandon(); e != nil {
		t.Fatal(e)
	}
	g, _ := s.repositoryGroup()
	c, _, e := g.Character.Load(ctx, "99")
	if e != nil {
		t.Fatal(e)
	}
	c.Level = 17
	c.Stats["exp"] = 0
	c.Stats["town_id"] = 50
	c.Stats["area_id"] = 0
	c.Stats["pos_x"] = 441
	c.Stats["pos_y"] = 207
	c.Stats["fatigue"] = 0
	if e = g.Character.Save(ctx, c); e != nil {
		t.Fatal(e)
	}
	if e = g.Skill.Save(ctx, repo.SkillRecord{CharacterID: "99", Points: repo.SkillPointState{SyncedLevel: 17}}); e != nil {
		t.Fatal(e)
	}
	if e = g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{}}); e != nil {
		t.Fatal(e)
	}
	s.options.currentClientVersion = "official-24.9"
	s.worldMapTable = table
	s.worldMapResolver = resolver
	s.dungeonMonsterTable = monsters
	s.dungeonAICharacterTable = ai
	s.initialEquipmentArchive = actual.initialEquipmentArchive
	q := newFakeCurrentDungeonDeathTimerQueue()
	q.now = time.Date(2026, 10, 10, 9, 0, 0, 0, oe.ChinaCalendar)
	s.gameplayTimers = q
	a.accountID = c.AccountID
	a.loginPacketSeen = true
	a.accountFromToken = true
	s.bindGameSessionCharacter(a, 99)
	if e = s.initializeActivityControls(ctx); e != nil {
		t.Fatal(e)
	}
	campaign, e := s.currentEpicCampaign(a)
	if e != nil {
		t.Fatal(e)
	}
	return s, a, campaign
}
func prepareSemesterActualCompleted(t *testing.T) (*Service, *gameSession, *runtimeDungeonState, oe.EpicCampaign) {
	t.Helper()
	s, a, campaign := prepareSemesterActualReady(t)
	return s, a, completeSemesterActualRun(t, s, a), campaign
}
func TestNewSemesterActual6062CompletedFixture(t *testing.T) {
	_, _, _, _ = prepareSemesterActualCompleted(t)
}
func TestNewSemesterREDCommitted6062MustJournalQualifiedDay(t *testing.T) {
	s, _, r, campaign := prepareSemesterActualCompleted(t)
	g, _ := s.repositoryGroup()
	ch, _, e := g.Character.Load(context.Background(), "99")
	if e != nil {
		t.Fatal(e)
	}
	key := "event_new_semester_v1:" + campaign.Starts.UTC().Format("20060102T150405Z") + ":qualified_days"
	if ch.Stats[key] != 1 {
		t.Fatalf("missingcommittedclearjournal: daycount=%d want1 actual6062/boss63851/key=%s", ch.Stats[key], r.clearMapCompletionKey)
	}
}

func completeSemesterActualRun(t *testing.T, s *Service, a *gameSession, beforeBoss ...bool) *runtimeDungeonState {
	t.Helper()
	bindDungeonSelectorOriginForTestAt(t, s, a, 50, 0, 441, 207)
	runtime, _, e := s.prepareDungeonRuntime(context.Background(), a, dungeoncmd.SelectDungeonRequest{DungeonID: 6062})
	if e != nil {
		t.Fatal(e)
	}
	if e = freezeCurrentDungeonTownReturnOrigin(a, runtime); e != nil {
		t.Fatal(e)
	}
	if e = s.commitDungeonRuntime(a, runtime); e != nil {
		t.Fatal(e)
	}
	for x := 0; x < 3; x++ {
		if x > 0 {
			scene, e := runtime.Session.MoveByte(byte(x), 0)
			if e != nil {
				t.Fatal(e)
			}
			room, next, e := newRuntimeDungeonRoom(scene, s.dungeonMonsterTable, runtime.NextObjectKey)
			if e != nil {
				t.Fatal(e)
			}
			runtime.Room = room
			runtime.NextObjectKey = next
		}
		scene, ok := runtime.Session.Scene()
		if !ok || scene.Map.Map.ID != int64(80486+x) {
			t.Fatal("actualsceneowner", scene.Map.Map.ID)
		}
		if _, e = runtime.Room.AnnounceAllActors(runtime.Session); e != nil {
			t.Fatal(e)
		}
		for _, m := range runtime.Room.Snapshot().Monsters {
			if len(beforeBoss) > 0 && beforeBoss[0] && x == 2 && m.Definition.ID == 63851 {
				continue
			}
			if m.State == runtimeDungeonMonsterDefeated {
				continue
			}
			if _, already := runtime.Room.DefeatedDeathTarget(m.ObjectKey); already {
				continue
			}
			b := make([]byte, dungeoncmd.Official249DieMonsterBaseSize+dungeoncmd.Official249DieMonsterEntrySize)
			binary.LittleEndian.PutUint32(b, m.ObjectKey)
			binary.LittleEndian.PutUint16(b[4:], 99)
			b[30] = 1
			if e = s.handleDungeonMonsterDeath(a, b); e != nil {
				t.Fatal("actual39", x, m.Definition.ID, e)
			}
		}
	}
	if len(beforeBoss) > 0 && beforeBoss[0] {
		return runtime
	}
	snap := runtime.Session.Snapshot()
	if snap.Run.Status != worldmap.DungeonRunCompleted || snap.Scene.Map.Map.ID != 80488 || !runtime.bossDieCheckAccepted || !runtime.clearMapCompletionPhaseAPersisted || runtime.clearMapCompletionKey == "" {
		t.Fatalf("notactualcompletedreceipt %+v runtime %+v", snap, runtime)
	}
	found := false
	for _, m := range runtime.Room.Snapshot().Monsters {
		if m.Definition.ID == 63851 && m.State == runtimeDungeonMonsterDefeated {
			found = true
			if _, ok := runtime.Room.DeathResponseBody(m.ObjectKey); !ok {
				t.Fatal("missingboss39receipt")
			}
		}
	}
	if !found {
		t.Fatal("missingrealboss63851")
	}

	return runtime
}

func semesterQualifiedKey(c oe.EpicCampaign) string {
	return "event_new_semester_v1:" + c.Starts.UTC().Format("20060102T150405Z") + ":qualified_days"
}
func TestNewSemesterREDSameDayReentryMustCountOnce(t *testing.T) {
	s, a, first, c := prepareSemesterActualCompleted(t)
	q := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
	q.mu.Lock()
	q.now = q.now.Add(time.Minute)
	q.mu.Unlock()
	second := completeSemesterActualRun(t, s, a)
	if second == first || second.clearMapCompletionKey == first.clearMapCompletionKey {
		t.Fatal("notrealnewrun")
	}
	g, _ := s.repositoryGroup()
	ch, _, e := g.Character.Load(context.Background(), "99")
	if e != nil {
		t.Fatal(e)
	}
	if ch.Stats[semesterQualifiedKey(c)] != 1 {
		t.Fatalf("same-day-journal=%d want1 twoacceptedruns", ch.Stats[semesterQualifiedKey(c)])
	}
}

func TestNewSemesterREDCrossDayOldReceiptMustNotCountAgain(t *testing.T) {
	s, a, r, c := prepareSemesterActualCompleted(t)
	key, completed := r.clearMapCompletionKey, r.clearMapCompletionAt
	q := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
	q.mu.Lock()
	q.now = time.Date(2026, 10, 11, 6, 0, 0, 0, oe.ChinaCalendar)
	q.mu.Unlock()
	r.clearMapCompletionPhaseAPersisted = false
	if e := s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), a, r, r.Session.Snapshot()); e != nil {
		t.Fatal(e)
	}
	if r.clearMapCompletionKey != key || !r.clearMapCompletionAt.Equal(completed) {
		t.Fatal("old receipt changed at next-day retry")
	}
	g, _ := s.repositoryGroup()
	ch, _, e := g.Character.Load(context.Background(), "99")
	if e != nil {
		t.Fatal(e)
	}
	if ch.Stats[semesterQualifiedKey(c)] != 1 {
		t.Fatalf("old receipt next-day journal=%d want1", ch.Stats[semesterQualifiedKey(c)])
	}
}

func TestNewSemesterREDChangedPhysicalAccountMustRejectClearJournal(t *testing.T) {
	s, a, r, _ := prepareSemesterActualCompleted(t)
	g, _ := s.repositoryGroup()
	ch, _, e := g.Character.Load(context.Background(), "99")
	if e != nil {
		t.Fatal(e)
	}
	ch.AccountID = "foreign-account"
	if e = g.Character.Save(context.Background(), ch); e != nil {
		t.Fatal(e)
	}
	r.clearMapCompletionPhaseAPersisted = false
	if e = s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), a, r, r.Session.Snapshot()); e == nil {
		t.Fatal("foreign physical account accepted by completed-clear boundary")
	}
	if r.clearMapCompletionPhaseAPersisted {
		t.Fatal("foreign owner closed PhaseA")
	}
}
func TestNewSemesterREDStableClearReplayMustJournalOnce(t *testing.T) {
	s, a, r, c := prepareSemesterActualCompleted(t)
	key := r.clearMapCompletionKey
	r.clearMapCompletionPhaseAPersisted = false
	if e := s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), a, r, r.Session.Snapshot()); e != nil {
		t.Fatal(e)
	}
	if r.clearMapCompletionKey != key {
		t.Fatal("replaychangedreceipt")
	}
	g, _ := s.repositoryGroup()
	ch, _, _ := g.Character.Load(context.Background(), "99")
	if ch.Stats[semesterQualifiedKey(c)] != 1 {
		t.Fatalf("replayed-journal=%d want1", ch.Stats[semesterQualifiedKey(c)])
	}
}
func TestNewSemesterREDDayCutNewRunMustCountTwo(t *testing.T) {
	s, a, _, c := prepareSemesterActualCompleted(t)
	q := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
	q.mu.Lock()
	q.now = time.Date(2026, 10, 11, 6, 0, 0, 0, oe.ChinaCalendar)
	q.mu.Unlock()
	completeSemesterActualRun(t, s, a)
	g, _ := s.repositoryGroup()
	ch, _, _ := g.Character.Load(context.Background(), "99")
	if ch.Stats[semesterQualifiedKey(c)] != 2 {
		t.Fatalf("day-cut-journal=%d want2", ch.Stats[semesterQualifiedKey(c)])
	}
}

// The participants below enter through the real runtime prepare/commit and
// shared-instance adoption paths. No completed snapshot or boss-death receipt
// is invented for a peer; each peer's scene is bound to the same real room.
func prepareSemesterActualPartyCompleted(t *testing.T, beforeFinal ...func(*Service, []*gameSession, []*runtimeDungeonState)) (*Service, []*gameSession, []*runtimeDungeonState, oe.EpicCampaign) {
	t.Helper()
	s, leader, campaign := prepareSemesterActualReady(t)
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	base, _, e := g.Character.Load(ctx, "99")
	if e != nil {
		t.Fatal(e)
	}
	sessions := []*gameSession{leader}
	state := alignedcmd.PartyState{PartyID: 93, UserID: 99, MaxMembers: 4}
	for id := uint16(99); id <= 102; id++ {
		state.Members = append(state.Members, alignedcmd.PartyMemberState{UserID: id, UserState: 1, HPPercent: 100, MPPercent: 100})
		if id == 99 {
			continue
		}
		ch := base
		ch.Stats = make(map[string]int64, len(base.Stats))
		for key, value := range base.Stats {
			ch.Stats[key] = value
		}
		ch.CharacterID = fmt.Sprint(id)
		ch.AccountID = fmt.Sprintf("semester-account-%d", id)
		if e = g.Account.Save(ctx, repo.AccountRecord{AccountID: ch.AccountID}); e != nil {
			t.Fatal(e)
		}
		if e = g.Character.Save(ctx, ch); e != nil {
			t.Fatal(e)
		}
		if e = g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: ch.CharacterID, Slots: map[string]repo.ItemStack{}}); e != nil {
			t.Fatal(e)
		}
		if e = g.Skill.Save(ctx, repo.SkillRecord{CharacterID: ch.CharacterID, Points: repo.SkillPointState{SyncedLevel: 17}}); e != nil {
			t.Fatal(e)
		}
		peer := &gameSession{conn: &bufferConn{}, channel: leader.channel, residentChannel: leader.residentChannel, accountID: ch.AccountID, loginPacketSeen: true, accountFromToken: true}
		s.bindGameSessionCharacter(peer, id)
		sessions = append(sessions, peer)
	}
	runtimes := []*runtimeDungeonState{}
	for _, peer := range sessions {
		storeRuntimePartyState(peer, state)
		bindDungeonSelectorOriginForTestAt(t, s, peer, 50, 0, 441, 207)
		r, _, e := s.prepareDungeonRuntime(ctx, peer, dungeoncmd.SelectDungeonRequest{DungeonID: 6062})
		if e != nil {
			t.Fatal("party prepare", peer.selectedCharacterID, e)
		}
		if e = freezeCurrentDungeonTownReturnOrigin(peer, r); e != nil {
			t.Fatal(e)
		}
		if e = s.commitDungeonRuntime(peer, r); e != nil {
			t.Fatal("party commit", peer.selectedCharacterID, e)
		}
		if r.partyInstance == nil {
			t.Fatal("missing actual party adoption")
		}
		runtimes = append(runtimes, r)
	}
	instance := runtimes[0].partyInstance
	for _, r := range runtimes {
		if r.semesterRun != runtimes[0].semesterRun {
			t.Fatal("actual party did not share frozen semester nonce")
		}
	}
	if instance.memberCount() != 4 {
		t.Fatal("not four actual participants")
	}
	catalog, e := s.dungeonMonsterCatalog()
	if e != nil {
		t.Fatal(e)
	}
	for x := 0; x < 3; x++ {
		for index, r := range runtimes {
			if r.partyInstance != instance {
				t.Fatal("foreign instance")
			}
			if x > 0 {
				scene, e := r.Session.MoveByte(byte(x), 0)
				if e != nil {
					t.Fatal("party move", index, x, e)
				}
				room, next, e := s.prepareRuntimeDungeonTargetRoom(r, scene, catalog, "semester_actual_fixture")
				if e != nil {
					t.Fatal(e)
				}
				r.Room, r.NextObjectKey = room, next
			}
			if _, e = r.Room.AnnounceAllActors(r.Session); e != nil {
				t.Fatal(e)
			}
			if r.Room != runtimes[0].Room {
				t.Fatal("party room not shared")
			}
		}
		if x == 2 && len(beforeFinal) > 0 {
			beforeFinal[0](s, sessions, runtimes)
		}
		for _, m := range runtimes[0].Room.Snapshot().Monsters {
			if _, already := runtimes[0].Room.DefeatedDeathTarget(m.ObjectKey); already {
				continue
			}
			body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize+dungeoncmd.Official249DieMonsterEntrySize)
			binary.LittleEndian.PutUint32(body, m.ObjectKey)
			binary.LittleEndian.PutUint16(body[4:], 99)
			body[30] = 1
			if e = s.handleDungeonMonsterDeath(leader, body); e != nil {
				t.Fatal("actual party death", x, m.Definition.ID, e)
			}
		}
	}
	for i, r := range runtimes {
		if r.Session.Snapshot().Run.Status == worldmap.DungeonRunAbandoned {
			continue
		}
		if i > 0 && r.partyInstance == nil {
			continue
		}
		if r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !r.bossDieCheckAccepted || (len(beforeFinal) == 0 && !r.clearMapCompletionPhaseAPersisted) || r.clearMapCompletionKey == "" {
			t.Fatalf("participant %d not actually completed: status=%s accepted=%t phase=%t", sessions[i].selectedCharacterID, r.Session.Snapshot().Run.Status, r.bossDieCheckAccepted, r.clearMapCompletionPhaseAPersisted)
		}
	}
	return s, sessions, runtimes, campaign
}
func TestNewSemesterActualFourParticipantsCompletedFixture(t *testing.T) {
	prepareSemesterActualPartyCompleted(t)
}
func TestNewSemesterREDFourActualParticipantsMustJournal(t *testing.T) {
	s, sessions, _, campaign := prepareSemesterActualPartyCompleted(t)
	g, _ := s.repositoryGroup()
	for _, peer := range sessions {
		ch, found, e := g.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
		if e != nil || !found || ch.AccountID != peer.accountID {
			t.Fatal("physical participant owner", e)
		}
		if ch.Stats[semesterQualifiedKey(campaign)] != 1 {
			t.Errorf("participant %s/%s committed journal=%d want1", ch.AccountID, ch.CharacterID, ch.Stats[semesterQualifiedKey(campaign)])
		}
	}
}

type semesterFailClearUOW struct {
	repo.SemesterClearUnitOfWork
	err error
}

func (u *semesterFailClearUOW) WithinSemesterClear(ctx context.Context, source repo.SemesterClearOwner, owners []repo.SemesterClearOwner, scope string, apply func(repo.Group) error) error {
	if u.err != nil {
		return u.err
	}
	return u.SemesterClearUnitOfWork.WithinSemesterClear(ctx, source, owners, scope, apply)
}

// The frozen v1 test used Character.Load before the narrow UOW existed.
// v2 injects at the new physical owner boundary, preserving the retry contract.
func TestNewSemesterREDClearStoreFailureThenStableRetryMustJournal(t *testing.T) {
	s, a, runtime, campaign := prepareSemesterActualCompleted(t)
	g, _ := s.repositoryGroup()
	fault := errors.New("semester-clear-store-fault")
	wrapped := &semesterFailClearUOW{SemesterClearUnitOfWork: g.SemesterClear, err: fault}
	modified := g
	modified.SemesterClear = wrapped
	s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
	key, at := runtime.clearMapCompletionKey, runtime.clearMapCompletionAt
	runtime.clearMapCompletionPhaseAPersisted = false
	e := s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), a, runtime, runtime.Session.Snapshot())
	if !errors.Is(e, fault) || runtime.clearMapCompletionPhaseAPersisted {
		t.Fatalf("failure consumed barrier: err=%v phase=%t", e, runtime.clearMapCompletionPhaseAPersisted)
	}
	wrapped.err = nil
	if e = s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), a, runtime, runtime.Session.Snapshot()); e != nil {
		t.Fatal(e)
	}
	if runtime.clearMapCompletionKey != key || !runtime.clearMapCompletionAt.Equal(at) {
		t.Fatal("failed retry changed receipt identity")
	}
	ch, _, e := g.Character.Load(context.Background(), "99")
	if e != nil {
		t.Fatal(e)
	}
	if ch.Stats[semesterQualifiedKey(campaign)] != 1 {
		t.Fatalf("recovered clear journal=%d want1", ch.Stats[semesterQualifiedKey(campaign)])
	}
}

func TestNewSemesterWrongSelectedCharacterKeepsBarrierOpen(t *testing.T) {
	s, a, r, _ := prepareSemesterActualCompleted(t)
	r.clearMapCompletionPhaseAPersisted = false
	a.selectedCharacterID = 100
	if e := s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), a, r, r.Session.Snapshot()); e == nil {
		t.Fatal("foreign selected character accepted")
	}
	if r.clearMapCompletionPhaseAPersisted {
		t.Fatal("foreign character closed barrier")
	}
}
