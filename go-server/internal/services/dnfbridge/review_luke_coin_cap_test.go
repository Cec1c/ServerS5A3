package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"sync"
	"testing"
	"time"
)

func reviewActualLukeCoinFixtureForDungeon(t *testing.T, withPeer bool, dungeonID uint32) (*raidStartGuardFixture, *gameSession, *runtimeDungeonState, *gameSession, time.Time) {
	t.Helper()
	f, s, phaseAt := normalLukeFixture(t)
	if dungeonID == 5311 {
		for _, id := range []uint32{5054, 5055, 5056} {
			for n := 0; n < 3; n++ {
				clearOwnedLuke(t, f, s, id, phaseAt)
				phaseAt = phaseAt.Add(time.Second)
			}
		}
		flow := f.state.Battle.RewardFlow[0]
		if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
			t.Fatal(errs)
		}
		if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(65*time.Second), zeroAntonRoll); len(errs) != 0 {
			t.Fatal(errs)
		}
		if _, errs := f.service.advanceCurrentRaidClocks(flow.BreakUntil); len(errs) != 0 || f.state.Battle.Phase != 1 {
			t.Fatal("phase-two fixture failed", errs)
		}
		s.dungeon.runtime = nil
	}
	a := f.service.initialEquipmentArchive
	table, e := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if e != nil {
		t.Fatal(e)
	}
	resolver, e := worldmap.NewResolver(table)
	if e != nil {
		t.Fatal(e)
	}
	f.service.worldMapTable, f.service.worldMapResolver = table, resolver
	repos, _ := f.service.repositoryGroup()
	character, characterFound, err := repos.Character.Load(context.Background(), "19")
	if err != nil || !characterFound {
		t.Fatal(err)
	}
	character.Stats = map[string]int64{"fatigue": 156, "town_id": 19, "area_id": 0, "pos_x": 474, "pos_y": 234, "direction": 5, "area_state": 3}
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	inventory, _, err := repos.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	inventory.CharacterID = "19"
	if inventory.Slots == nil {
		inventory.Slots = map[string]repo.ItemStack{}
	}
	inventory.Slots[csharpReviveCoinSlotKey()] = repo.ItemStack{ItemID: csharpReviveCoinItemID, Count: 30}
	if err := repos.Inventory.Save(context.Background(), inventory); err != nil {
		t.Fatal(err)
	}
	bindDungeonSelectorOriginForTestAt(t, f.service, s, 19, 0, 474, 234)
	d, ok := table.FindDungeon(int64(dungeonID))
	if !ok {
		t.Fatal("actual birth dungeon missing")
	}
	var found bool
	for maze := range d.Mazes {
		top, e := worldmap.BuildDungeonLayout(resolver, int64(dungeonID), maze, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
		if e != nil {
			t.Fatal(e)
		}
		for _, rm := range top.Rooms() {
			if rm.Map == nil {
				continue
			}
			budget := 0
			for _, o := range rm.Map.Map.PassiveObjects {
				if o.ObjectID == 64249 || o.ObjectID == 64250 {
					budget++
				}
			}
			if dungeonID == 5054 && budget == 0 {
				continue
			}
			found = true
			coord := rm.Coordinate
			top.Start = &coord
			run, e := worldmap.NewDungeonRun(top)
			if e != nil {
				t.Fatal(e)
			}
			ds, e := worldmap.NewDungeonSession(run)
			if e != nil {
				t.Fatal(e)
			}
			scene, _ := ds.Scene()
			mt, e := newPVFDungeonMonsterCatalog(a)
			if e != nil {
				t.Fatal(e)
			}
			room, _, e := newRuntimeDungeonRoom(scene, mt, firstDungeonMonsterObjectKey)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = room.AnnounceAllActors(ds); e != nil {
				t.Fatal(e)
			}
			r := &runtimeDungeonState{Dungeon: d, MazeIndex: maze, Session: ds, Room: room, AccountID: s.accountID, Character: character}
			s.dungeon.runtime = r
			if err := freezeCurrentDungeonTownReturnOrigin(s, r); err != nil {
				t.Fatal(err)
			}
			s.postStartMapPlayerStateSent = true
			var peer *gameSession
			var followers []*runtimePartyPreparedDungeonEntry
			if withPeer {
				peer = f.session()
				peer.selectedCharacterID = 20
				peer.accountID = "account-20"
				peer.channel = s.channel
				peer.loginPacketSeen = true
				peer.accountFromToken = true
				f.service.bindGameSessionCharacter(peer, 20)
				f.state.Members[1].GroupIndex = 1
				f.state.Battle.MemberGroups[20] = 1
				prun, err := worldmap.NewDungeonRun(top)
				if err != nil {
					t.Fatal(err)
				}
				pds, err := worldmap.NewDungeonSession(prun)
				if err != nil {
					t.Fatal(err)
				}
				psc, _ := pds.Scene()
				proom, _, err := newRuntimeDungeonRoom(psc, mt, firstDungeonMonsterObjectKey)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = proom.AnnounceAllActors(pds); err != nil {
					t.Fatal(err)
				}
				pr := &runtimeDungeonState{Dungeon: d, MazeIndex: maze, Session: pds, Room: proom, AccountID: peer.accountID, Character: repo.CharacterRecord{CharacterID: "20", AccountID: peer.accountID}}
				peer.dungeon.runtime = pr
				peerCharacter, ok, err := repos.Character.Load(context.Background(), "20")
				if err != nil || !ok {
					t.Fatal(err)
				}
				peerCharacter.Stats = repo.CloneCharacter(character).Stats
				if err := repos.Character.Save(context.Background(), peerCharacter); err != nil {
					t.Fatal(err)
				}
				pr.Character = peerCharacter
				pi := repo.InventoryRecord{CharacterID: "20", Slots: map[string]repo.ItemStack{csharpReviveCoinSlotKey(): {ItemID: csharpReviveCoinItemID, Count: 30}}}
				if err := repos.Inventory.Save(context.Background(), pi); err != nil {
					t.Fatal(err)
				}
				bindDungeonSelectorOriginForTestAt(t, f.service, peer, 19, 0, 474, 234)
				if err := freezeCurrentDungeonTownReturnOrigin(peer, pr); err != nil {
					t.Fatal(err)
				}
				peer.postStartMapPlayerStateSent = true
				followers = []*runtimePartyPreparedDungeonEntry{{session: peer, runtime: pr}}
			}
			if e = f.service.freezeCurrentRaidDungeonGroupEntry(s, r, followers); e != nil {
				t.Fatal(e)
			}
			at := time.Now()
			if _, _, e = f.service.reduceCurrentLukeSceneLocked(s, r, "enter", at); e != nil {
				t.Fatal(e)
			}
			if peer != nil {
				if _, _, err := f.service.reduceCurrentLukeSceneLocked(peer, peer.dungeon.runtime, "enter", at); err != nil {
					t.Fatal(err)
				}
			}
			return f, s, r, peer, at
		}
	}
	if !found {
		t.Fatal("actual owned raid generator not present in chosen birth layouts")
	}

	return nil, nil, nil, nil, time.Time{}
}

func TestReviewActualLukeBirthEntryRejectsNinthCoinBeforeRevive(t *testing.T) {
	f, s, r, _, _ := reviewActualLukeCoinFixture(t, false)
	f.service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	r.lifecycleToken = 1
	body := make([]byte, 2)
	binary.LittleEndian.PutUint16(body, currentSceneActorObjectKey(s.selectedCharacterID))
	for n := 0; n < 9; n++ {
		if e := f.service.handleDungeonDieCharacter(s, make([]byte, 4)); e != nil {
			t.Fatal(e)
		}
		if !r.deathReturnWaiting {
			t.Fatal("legal death did not arm waiting", n)
		}
		f.service.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue).now = time.Now()
		if e := f.service.handleDungeonUseCoin(s, body); e != nil {
			t.Fatal(e)
		}
		if n < 8 && r.deathReturnWaiting {
			t.Fatal("legal capped revive rejected", n)
		}
		if n == 8 && !r.deathReturnWaiting {
			t.Fatal("actual Luke birth coin limit8 allowed ninth revive")
		}
	}
}

func reviewLukeCoinDie(t *testing.T, f *raidStartGuardFixture, s *gameSession) {
	t.Helper()
	if e := f.service.handleDungeonDieCharacter(s, make([]byte, 4)); e != nil {
		t.Fatal(e)
	}
	if !s.dungeon.runtime.deathReturnWaiting {
		t.Fatal("death fixture not committed")
	}
	f.service.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue).now = time.Now()
}
func reviewLukeCoinRequest(s *gameSession) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, currentSceneActorObjectKey(s.selectedCharacterID))
	return b
}
func reviewLukeCoinBalance(t *testing.T, f *raidStartGuardFixture, s *gameSession) int64 {
	t.Helper()
	repos, _ := f.service.repositoryGroup()
	v, ok, e := repos.Inventory.Load(context.Background(), fmt.Sprint(s.selectedCharacterID))
	if e != nil || !ok {
		t.Fatal(e)
	}
	return v.Slots[csharpReviveCoinSlotKey()].Count
}

type reviewLukeLateFailItems struct{ base repo.CharacterItemUnitOfWork }

func (u reviewLukeLateFailItems) WithinCharacterItems(ctx context.Context, id string, apply func(repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.base.WithinCharacterItems(ctx, id, func(i repo.InventoryRepository, e repo.EquipmentRepository) error {
		if err := apply(i, e); err != nil {
			return err
		}
		return fmt.Errorf("injected failure after physical scoped inventory save")
	})
}

type reviewLukeTimeoutItems struct{}

func (reviewLukeTimeoutItems) WithinCharacterItems(ctx context.Context, _ string, _ func(repo.InventoryRepository, repo.EquipmentRepository) error) error {
	<-ctx.Done()
	return ctx.Err()
}
func TestReviewActualLukeCoinLateFailureAndTimeoutDoNotSpendQuotaOrWallet(t *testing.T) {
	for _, name := range []string{"late-after-save", "timeout"} {
		t.Run(name, func(t *testing.T) {
			f, s, r, _, _ := reviewActualLukeCoinFixture(t, false)
			f.service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
			r.lifecycleToken = 1
			reviewLukeCoinDie(t, f, s)
			repos, _ := f.service.repositoryGroup()
			failed := repos
			if name == "timeout" {
				failed.CharacterItems = reviewLukeTimeoutItems{}
			} else {
				failed.CharacterItems = reviewLukeLateFailItems{repos.CharacterItems}
			}
			f.service.repositoryProvider = func() (repo.Group, bool) { return failed, true }
			start := time.Now()
			if err := f.service.handleDungeonUseCoin(s, reviewLukeCoinRequest(s)); err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(start)
			b := f.state.Battle
			if !r.deathReturnWaiting || b.RunCoins[r.raidEntry.RunID] != 0 || b.PhaseCoins != [2]uint32{} || reviewLukeCoinBalance(t, f, s) != 30 {
				t.Fatal("failed UOW spent quota, wallet or revived")
			}
			if name == "timeout" && (elapsed < createWriteTimeout || elapsed > createWriteTimeout+time.Second) {
				t.Fatal("cooperative context deadline wrong", elapsed)
			}
			f.service.repositoryProvider = func() (repo.Group, bool) { return repos, true }
			f.service.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue).now = time.Now()
			if e := f.service.handleDungeonUseCoin(s, reviewLukeCoinRequest(s)); e != nil {
				t.Fatal(e)
			}
			if r.deathReturnWaiting || b.RunCoins[r.raidEntry.RunID] != 1 || reviewLukeCoinBalance(t, f, s) != 29 {
				t.Fatal("legal retry did not commit exactly once")
			}
		})
	}
}
func TestReviewActualLukeCoinSharedPeersRaceForLastMapBudget(t *testing.T) {
	f, s, r, peer, _ := reviewActualLukeCoinFixture(t, true)
	f.service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	r.lifecycleToken = 1
	peer.dungeon.runtime.lifecycleToken = 2
	for n := 0; n < 7; n++ {
		reviewLukeCoinDie(t, f, s)
		if e := f.service.handleDungeonUseCoin(s, reviewLukeCoinRequest(s)); e != nil || r.deathReturnWaiting {
			t.Fatal(e)
		}
	}
	reviewLukeCoinDie(t, f, s)
	reviewLukeCoinDie(t, f, peer)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, member := range []*gameSession{s, peer} {
		wg.Add(1)
		go func(m *gameSession) {
			defer wg.Done()
			errs <- f.service.handleDungeonUseCoin(m, reviewLukeCoinRequest(m))
		}(member)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if f.state.Battle.RunCoins[r.raidEntry.RunID] != 8 || reviewLukeCoinBalance(t, f, s)+reviewLukeCoinBalance(t, f, peer) != 52 || r.deathReturnWaiting == peer.dungeon.runtime.deathReturnWaiting {
		t.Fatal("shared last quota not serialized with physical wallet success")
	}
}

func reviewActualLukeCoinFixture(t *testing.T, withPeer bool) (*raidStartGuardFixture, *gameSession, *runtimeDungeonState, *gameSession, time.Time) {
	return reviewActualLukeCoinFixtureForDungeon(t, withPeer, 5054)
}
func TestReviewActualLukePhaseTwoSharedCoinLimitSurvivesReentry(t *testing.T) {
	f, s, r, _, _ := reviewActualLukeCoinFixtureForDungeon(t, false, 5311)
	f.service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	r.lifecycleToken = 1
	for n := 0; n < 21; n++ {
		reviewLukeCoinDie(t, f, s)
		if e := f.service.handleDungeonUseCoin(s, reviewLukeCoinRequest(s)); e != nil {
			t.Fatal(e)
		}
		if n < 20 && r.deathReturnWaiting {
			t.Fatal("phase-two coin rejected early", n)
		}
		if n == 20 && !r.deathReturnWaiting {
			t.Fatal("phase-two total20 allowed21")
		}
	}
	b := f.state.Battle
	if b.PhaseCoins != [2]uint32{0, 20} || reviewLukeCoinBalance(t, f, s) != 10 {
		t.Fatal("actual phase2 total/account wallet wrong", b.PhaseCoins)
	}
	// Change only the run ownership through the real abandon/re-entry reducers;
	// the phase quota must stay20 despite a fresh frozen entry identity.
	oldRun := r.raidEntry.RunID
	oldScene, _ := r.Session.Scene()
	if e := r.Session.Abandon(); e != nil {
		t.Fatal(e)
	}
	if _, _, e := f.service.reduceCurrentLukeSceneLocked(s, r, "giveup", time.Now()); e != nil {
		t.Fatal(e)
	}
	top, e := worldmap.BuildDungeonLayout(f.service.worldMapResolver, r.Dungeon.ID, r.MazeIndex, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
	if e != nil {
		t.Fatal(e)
	}
	coord := oldScene.Coordinate
	top.Start = &coord
	run, e := worldmap.NewDungeonRun(top)
	if e != nil {
		t.Fatal(e)
	}
	ds, e := worldmap.NewDungeonSession(run)
	if e != nil {
		t.Fatal(e)
	}
	scene, _ := ds.Scene()
	mt, e := newPVFDungeonMonsterCatalog(f.service.initialEquipmentArchive)
	if e != nil {
		t.Fatal(e)
	}
	room, _, e := newRuntimeDungeonRoom(scene, mt, firstDungeonMonsterObjectKey)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := room.AnnounceAllActors(ds); e != nil {
		t.Fatal(e)
	}
	nextRuntime := *r
	nextRuntime.Session, nextRuntime.Room = ds, room
	nextRuntime.raidEntry = nil
	nextRuntime.deathReturnWaiting, nextRuntime.deathStateSent = false, false
	nextRuntime.lifecycleToken++
	r = &nextRuntime
	s.dungeon.runtime = r
	if e := f.service.freezeCurrentRaidDungeonGroupEntry(s, r, nil); e != nil {
		t.Fatal(e)
	}
	if _, _, e := f.service.reduceCurrentLukeSceneLocked(s, r, "enter", time.Now()); e != nil {
		t.Fatal(e)
	}
	if r.raidEntry.RunID == oldRun {
		t.Fatal("fixture did not get fresh run")
	}
	reviewLukeCoinDie(t, f, s)
	if e := f.service.handleDungeonUseCoin(s, reviewLukeCoinRequest(s)); e != nil {
		t.Fatal(e)
	}
	if !r.deathReturnWaiting || b.PhaseCoins[1] != 20 || reviewLukeCoinBalance(t, f, s) != 10 {
		t.Fatal("fresh run reset phase quota")
	}
}

func TestReviewActualLukeCoinInvalidBindingCannotSpendWalletOrQuota(t *testing.T) {
	f, s, r, _, _ := reviewActualLukeCoinFixture(t, false)
	f.service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	r.lifecycleToken = 1
	reviewLukeCoinDie(t, f, s)
	b := f.state.Battle
	e := r.raidEntry
	for _, name := range []string{"instance", "attempt", "phase", "account", "participants", "channel", "post-placement", "death-journal", "dungeon", "deadline"} {
		t.Run(name, func(t *testing.T) {
			restore := func() {}
			switch name {
			case "instance":
				old := e.InstanceID
				e.InstanceID = "foreign"
				restore = func() { e.InstanceID = old }
			case "attempt":
				old := e.Attempt
				e.Attempt++
				restore = func() { e.Attempt = old }
			case "phase":
				old := b.Phase
				b.Phase = 1
				restore = func() { b.Phase = old }
			case "account":
				old := r.AccountID
				r.AccountID = "foreign"
				restore = func() { r.AccountID = old }
			case "participants":
				old := e.Participants
				e.Participants = []uint16{20}
				restore = func() { e.Participants = old }
			case "channel":
				old := s.channel.Type
				s.channel.Type = 23
				restore = func() { s.channel.Type = old }
			case "post-placement":
				s.postStartMapPlayerStateSent = false
				restore = func() { s.postStartMapPlayerStateSent = true }
			case "death-journal":
				old := b.MemberLives
				b.MemberLives = nil
				restore = func() { b.MemberLives = old }
			case "dungeon":
				old := r.Dungeon.ID
				r.Dungeon.ID = 5055
				restore = func() { r.Dungeon.ID = old }
			case "deadline":
				old := b.Deadline
				b.Deadline = time.Now().Add(-time.Second)
				restore = func() { b.Deadline = old }
			}
			defer restore()
			f.service.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue).now = time.Now()
			if err := f.service.handleDungeonUseCoin(s, reviewLukeCoinRequest(s)); err != nil {
				t.Fatal(err)
			}
			if !r.deathReturnWaiting || reviewLukeCoinBalance(t, f, s) != 30 || b.RunCoins[e.RunID] != 0 || b.PhaseCoins != [2]uint32{} {
				t.Fatal("foreign binding spent assets or quota")
			}
		})
	}
	replacement := f.session()
	replacement.channel = s.channel
	replacement.accountID = s.accountID
	replacement.loginPacketSeen, replacement.accountFromToken = true, true
	f.service.bindGameSessionCharacter(replacement, 19)
	if err := f.service.handleDungeonUseCoin(s, reviewLukeCoinRequest(s)); err != nil {
		t.Fatal(err)
	}
	if !r.deathReturnWaiting || reviewLukeCoinBalance(t, f, s) != 30 || b.RunCoins[e.RunID] != 0 {
		t.Fatal("old socket spent new binding wallet")
	}
}

func TestReviewActualLukeCoinPhaseDeadlineCancelsInFlightUOWBeforeQuota(t *testing.T) {
	f, s, r, _, _ := reviewActualLukeCoinFixture(t, false)
	f.service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	r.lifecycleToken = 1
	reviewLukeCoinDie(t, f, s)
	b := f.state.Battle
	b.Deadline = time.Now().Add(80 * time.Millisecond)
	repos, _ := f.service.repositoryGroup()
	failed := repos
	failed.CharacterItems = reviewLukeTimeoutItems{}
	f.service.repositoryProvider = func() (repo.Group, bool) { return failed, true }
	started := time.Now()
	if err := f.service.handleDungeonUseCoin(s, reviewLukeCoinRequest(s)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second || elapsed < 40*time.Millisecond {
		t.Fatal("phase deadline not applied", elapsed)
	}
	if !r.deathReturnWaiting || reviewLukeCoinBalance(t, f, s) != 30 || b.RunCoins[r.raidEntry.RunID] != 0 || b.PhaseCoins != [2]uint32{} {
		t.Fatal("phase-expired UOW consumed quota or assets")
	}
}

type reviewLukeHeldItems struct {
	base             repo.CharacterItemUnitOfWork
	entered, release chan struct{}
}

func (u reviewLukeHeldItems) WithinCharacterItems(ctx context.Context, id string, apply func(repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.base.WithinCharacterItems(ctx, id, func(i repo.InventoryRepository, e repo.EquipmentRepository) error {
		if err := apply(i, e); err != nil {
			return err
		}
		close(u.entered)
		select {
		case <-u.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}
func TestReviewActualLukeCoinCanonicalRebindingWaitsForPhysicalCommit(t *testing.T) {
	f, s, r, _, _ := reviewActualLukeCoinFixture(t, false)
	f.service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	r.lifecycleToken = 1
	reviewLukeCoinDie(t, f, s)
	repos, _ := f.service.repositoryGroup()
	entered, release := make(chan struct{}), make(chan struct{})
	held := repos
	held.CharacterItems = reviewLukeHeldItems{repos.CharacterItems, entered, release}
	f.service.repositoryProvider = func() (repo.Group, bool) { return held, true }
	consumed := make(chan error, 1)
	go func() { consumed <- f.service.handleDungeonUseCoin(s, reviewLukeCoinRequest(s)) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("physical UOW did not reach staged save")
	}
	replacement := f.session()
	replacement.channel = s.channel
	replacement.accountID = s.accountID
	replacement.loginPacketSeen, replacement.accountFromToken = true, true
	bound := make(chan struct{})
	go func() { f.service.bindGameSessionCharacter(replacement, 19); close(bound) }()
	select {
	case <-bound:
		close(release)
		t.Fatal("canonical generation crossed in-flight asset commit")
	case <-time.After(60 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-consumed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("consume remained blocked")
	}
	select {
	case <-bound:
	case <-time.After(time.Second):
		t.Fatal("canonical bind did not resume")
	}
	if f.state.Battle.RunCoins[r.raidEntry.RunID] != 1 || reviewLukeCoinBalance(t, f, s) != 29 {
		t.Fatal("accepted old owner commit not exactly once before replacement")
	}
	if identity, ok := f.service.boundGameSessionCharacterSnapshot(replacement); !ok || identity.character != 19 {
		t.Fatal("actual replacement binding not installed")
	}
}
