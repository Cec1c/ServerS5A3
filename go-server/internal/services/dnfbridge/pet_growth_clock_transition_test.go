package dnfbridge

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	dnfpet "longheng.io/server/internal/modules/dnf/pet"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type petClockFaultUOW struct {
	inner   dnfrepo.CharacterPetUnitOfWork
	fail    bool
	history []int64
	repos   dnfrepo.Group
	before  func()
}

func TestPetGrowthClockRealPVFCreaturePersistsDungeonDecay(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the fixed a56 Script.pvf")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfpet.NewPVFCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for id, ref := range items.itemRefs {
		if ref.kind == dungeonDropItemEquipment && strings.HasPrefix(strings.ToLower(ref.path), "creature/") {
			ids = append(ids, int64(id))
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var definition dnfpet.PetCreatureDefinition
	for _, id := range ids {
		if got, err := catalog.ResolveCreature(id); err == nil {
			definition = got
			break
		}
	}
	if definition.ItemID == 0 {
		t.Fatal("actual equipment index has no resolvable creature")
	}
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Pet.Save(ctx, dnfrepo.PetRecord{CharacterID: "19", EquippedKey: "37", Entries: map[string]dnfrepo.PetEntry{
		"37": {PetKey: "37", CreatureKey: 37, ItemID: definition.ItemID, SourceListType: 3, SourceSlotIndex: 26, Level: 1, Satiety: 90, SatietyMicros: 90_000_000},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{
		"26": {SlotIndex: 26, ItemID: definition.ItemID, RawEntry: testCurrentEquippedCreatureRaw(26, uint32(definition.ItemID), 37)},
	}}); err != nil {
		t.Fatal(err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	service := &Service{petCatalog: catalog, gameplayTimers: queue, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{selectedCharacterID: 19, connID: "real-creature-clock"}
	start := queue.Now()
	if err := service.switchCurrentPetGrowthClock(session, currentPetGrowthClockTown, start, "town"); err != nil {
		t.Fatal(err)
	}
	queue.now = start.Add(360 * time.Second)
	if err := service.switchCurrentPetGrowthClock(session, currentPetGrowthClockDungeon, queue.Now(), "entry"); err != nil {
		t.Fatal(err)
	}
	queue.now = queue.now.Add(120 * time.Second)
	service.runCurrentPetGrowthTick(session, session.petGrowth.generation)
	// Use a fresh repository-backed service to check persistence, not a field
	// retained in the clock or a client-side optimistic display value.
	fresh := &Service{repositoryProvider: service.repositoryProvider}
	group, _ := fresh.repositoryGroup()
	record, found, err := group.Pet.Load(ctx, "19")
	if err != nil || !found || record.Entries["37"].SatietyMicros != 89_000_000 {
		t.Fatalf("durable decay found=%t entry=%+v err=%v", found, record.Entries["37"], err)
	}
	t.Logf("real creature item=%d path=%s name=%s: 90 -> town91 -> dungeon89 persisted", definition.ItemID, definition.PVFPath, definition.Name)
}

func (u *petClockFaultUOW) WithinCharacterPets(ctx context.Context, id string, apply func(dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository, dnfrepo.PetRepository) error) error {
	if u.before != nil {
		u.before()
	}
	if u.fail {
		return errors.New("controlled pet settlement failure")
	}
	err := u.inner.WithinCharacterPets(ctx, id, apply)
	if err == nil {
		record, _, loadErr := u.repos.Pet.Load(ctx, id)
		if loadErr != nil {
			return loadErr
		}
		u.history = append(u.history, record.Entries["37"].SatietyMicros)
	}
	return err
}

func TestPetGrowthClockEpicAdmissionFlushesAfterDungeonUnlock(t *testing.T) {
	service, session, runtime, repos, _ := prepareAuditActual6063Owner(t)
	// This branch checks admission and timer ownership without an equipped pet.
	// The existing real-resource epic fixture still owns all paid ticket checks.
	equipment, _, err := repos.Equipment.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	delete(equipment.Entries, "26")
	equipment.CharacterID = "99"
	if err := repos.Equipment.Save(context.Background(), equipment); err != nil {
		t.Fatal(err)
	}
	if err := repos.Pet.Save(context.Background(), dnfrepo.PetRecord{CharacterID: "99"}); err != nil {
		t.Fatal(err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Now()
	service.gameplayTimers = queue
	session.petGrowth = petGrowthClockState{mode: currentPetGrowthClockTown, characterID: 99, anchor: queue.Now().Add(-360 * time.Second), generation: 11}
	checked := false
	probe := &petClockFaultUOW{inner: repos.CharacterPets, repos: repos, before: func() {
		checked = true
		if !session.dungeon.mu.TryLock() {
			t.Fatal("pet repository I/O ran while the admission held dungeon.mu")
		}
		session.dungeon.mu.Unlock()
	}}
	repos.CharacterPets = probe
	service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := service.commitDungeonRuntime(session, runtime); err != nil {
		t.Fatal(err)
	}
	if !checked || session.petGrowth.mode != currentPetGrowthClockDungeon || len(session.petGrowth.pending) != 0 || !session.petGrowth.anchor.Equal(queue.Now()) {
		t.Fatal("paid epic admission did not switch the pet clock after unlocking")
	}
	bag, _, err := repos.Inventory.Load(context.Background(), "99")
	if err != nil || bag.Slots["0:1"].Count != 0 || session.dungeon.runtime != runtime {
		t.Fatal("paid epic fixture lost its ticket/runtime contract")
	}
}

func newPetClockTransitionFixture(t *testing.T) (*Service, *gameSession, *fakeCurrentDungeonDeathTimerQueue, *petClockFaultUOW) {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repos.Pet.Save(ctx, dnfrepo.PetRecord{CharacterID: "19", EquippedKey: "37", Entries: map[string]dnfrepo.PetEntry{
		"37": {PetKey: "37", CreatureKey: 37, ItemID: 63000, SourceListType: 3, SourceSlotIndex: 26, Satiety: 90, SatietyMicros: 90_000_000, Level: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{
		"26": {SlotIndex: 26, ItemID: 63000, RawEntry: testCurrentEquippedCreatureRaw(26, 63000, 37)},
	}}); err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfpet.NewPVFCatalog(bridgePetCatalogFixture())
	if err != nil {
		t.Fatal(err)
	}
	fault := &petClockFaultUOW{inner: repos.CharacterPets, repos: repos}
	repos.CharacterPets = fault
	queue := newFakeCurrentDungeonDeathTimerQueue()
	service := &Service{petCatalog: catalog, gameplayTimers: queue, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{selectedCharacterID: 19, connID: "pet-clock-transition"}
	if err := service.switchCurrentPetGrowthClock(session, currentPetGrowthClockTown, queue.Now(), "initial-town"); err != nil {
		t.Fatal(err)
	}
	return service, session, queue, fault
}

func TestPetGrowthClockRetriesPreserveOrderedTownDungeonTownIntervals(t *testing.T) {
	service, session, queue, fault := newPetClockTransitionFixture(t)
	start := queue.Now()
	generation := session.petGrowth.generation
	fault.fail = true
	entry := start.Add(360 * time.Second)
	if err := service.switchCurrentPetGrowthClock(session, currentPetGrowthClockDungeon, entry, "entry"); err == nil {
		t.Fatal("missing injected settlement failure")
	}
	if len(session.petGrowth.pending) != 1 || !session.petGrowth.anchor.Equal(start) {
		t.Fatal("failed entry lost original interval")
	}
	// Repeating the same desired mode must retain its first timestamp.
	if err := service.switchCurrentPetGrowthClock(session, currentPetGrowthClockDungeon, entry.Add(30*time.Second), "entry-replay"); err == nil {
		t.Fatal("failure not retained")
	}
	if len(session.petGrowth.pending) != 1 || !session.petGrowth.pending[0].at.Equal(entry) {
		t.Fatal("replay shifted the entry boundary")
	}
	returned := entry.Add(120 * time.Second)
	if err := service.switchCurrentPetGrowthClock(session, currentPetGrowthClockTown, returned, "rollback"); err == nil {
		t.Fatal("missing rollback settlement failure")
	}
	if len(session.petGrowth.pending) != 2 {
		t.Fatal("rollback erased the intervening dungeon segment")
	}
	if task := queue.task(t, len(queue.tasks)-1); task.delay != currentPetGrowthDungeonTick {
		t.Fatalf("pending transition retry delay=%s", task.delay)
	}
	fault.fail = false
	queue.now = returned.Add(360 * time.Second)
	service.runCurrentPetGrowthTick(session, generation)
	if got, want := fault.history, []int64{91_000_000, 89_000_000, 90_000_000}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered persisted states=%v want%v", got, want)
	}
	if session.petGrowth.mode != currentPetGrowthClockTown || len(session.petGrowth.pending) != 0 || !session.petGrowth.anchor.Equal(queue.Now()) {
		t.Fatal("retry did not catch up to the current town interval")
	}
	before := len(fault.history)
	service.runCurrentPetGrowthTick(session, generation)
	if len(fault.history) != before {
		t.Fatal("retired timer generation repeated a settlement")
	}
}

func TestPetGrowthClockScheduleFailurePreservesAnchorAndCanRearm(t *testing.T) {
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.scheduleErr = errors.New("controlled shared queue failure")
	service := &Service{gameplayTimers: queue}
	session := &gameSession{selectedCharacterID: 19, connID: "pet-clock-schedule"}
	at := queue.Now()
	if err := service.switchCurrentPetGrowthClock(session, currentPetGrowthClockDungeon, at, "entry"); err == nil {
		t.Fatal("queue failure was hidden")
	}
	if session.petGrowth.mode != currentPetGrowthClockDungeon || !session.petGrowth.anchor.Equal(at) || session.petGrowth.timerName != "" {
		t.Fatal("queue failure erased the committed mode or anchor")
	}
	generation := session.petGrowth.generation
	queue.scheduleErr = nil
	if err := service.switchCurrentPetGrowthClock(session, currentPetGrowthClockDungeon, at.Add(time.Second), "retry"); err != nil {
		t.Fatal(err)
	}
	if session.petGrowth.generation != generation || !session.petGrowth.anchor.Equal(at) || queue.scheduleCount != 1 {
		t.Fatal("same-mode retry shifted time or generation")
	}
}

func TestPetGrowthClockRejectsBackwardsAndForeignCharacterBoundaries(t *testing.T) {
	service, session, queue, _ := newPetClockTransitionFixture(t)
	start := queue.Now()
	if err := service.switchCurrentPetGrowthClock(session, currentPetGrowthClockDungeon, start.Add(-time.Second), "backwards"); err == nil {
		t.Fatal("backwards boundary accepted")
	}
	session.selectedCharacterID = 20
	if err := service.switchCurrentPetGrowthClock(session, currentPetGrowthClockDungeon, start.Add(time.Second), "foreign"); err == nil {
		t.Fatal("foreign character inherited old clock")
	}
	if session.petGrowth.characterID != 19 || len(session.petGrowth.pending) != 0 || !session.petGrowth.anchor.Equal(start) {
		t.Fatal("rejected boundary changed existing clock")
	}
}

func TestPetGrowthClockPreparedFollowerWaitsForGroupAndRejectsStaleRollback(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	session.dungeon.runtime = nil
	queue := newFakeCurrentDungeonDeathTimerQueue()
	service.gameplayTimers = queue
	catalog, err := dnfpet.NewPVFCatalog(bridgePetCatalogFixture())
	if err != nil {
		t.Fatal(err)
	}
	service.petCatalog = catalog
	repos, _ := service.repositoryGroup()
	equipment, _, err := repos.Equipment.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	equipment.CharacterID = "99"
	delete(equipment.Entries, "26")
	if err := repos.Equipment.Save(context.Background(), equipment); err != nil {
		t.Fatal(err)
	}
	if err := repos.Pet.Save(context.Background(), dnfrepo.PetRecord{CharacterID: "99"}); err != nil {
		t.Fatal(err)
	}
	start := queue.Now()
	session.petGrowth = petGrowthClockState{mode: currentPetGrowthClockTown, characterID: 99, anchor: start, generation: 7}
	member := &runtimePartyPreparedDungeonEntry{session: session, runtime: runtime}
	members := []*runtimePartyPreparedDungeonEntry{member}
	if err := service.commitRuntimePartyPreparedFollowers(members); err != nil {
		t.Fatal(err)
	}
	if !runtime.petGrowthEntryPending || session.petGrowth.mode != currentPetGrowthClockTown || len(session.petGrowth.pending) != 0 {
		t.Fatal("prepared follower started decay before the group committed")
	}
	queue.now = start.Add(120 * time.Second)
	service.rollbackRuntimePartyPreparedFollowers(members)
	if session.dungeon.runtime != nil || session.petGrowth.mode != currentPetGrowthClockTown || !session.petGrowth.anchor.Equal(start) {
		t.Fatal("failed group charged an unentered dungeon interval")
	}
	if err := service.commitRuntimePartyPreparedFollowers(members); err != nil {
		t.Fatal(err)
	}
	service.activateCurrentPetGrowthDungeonClock(session, runtime)
	if runtime.petGrowthEntryPending || session.petGrowth.mode != currentPetGrowthClockDungeon || !session.petGrowth.anchor.Equal(queue.Now()) {
		t.Fatal("committed group did not activate the follower clock")
	}
	newRuntime := *runtime
	session.dungeon.runtime = &newRuntime
	anchor, generation := session.petGrowth.anchor, session.petGrowth.generation
	service.activateCurrentPetGrowthDungeonClock(session, runtime)
	service.rollbackRuntimePartyPreparedFollowers(members)
	if session.dungeon.runtime != &newRuntime || session.petGrowth.mode != currentPetGrowthClockDungeon || !session.petGrowth.anchor.Equal(anchor) || session.petGrowth.generation != generation {
		t.Fatal("stale group callback changed the newer runtime's clock")
	}
}
