package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

var errR195CacheEntryCommit = errors.New("r195 cache entry commit injection")

// Delegate to the actual memory unit of work and actual domain callback. Only
// fail after its transaction-local inventory has been updated, before commit.
type r195CacheEntryCommitGate struct {
	base       dnfrepo.CharacterItemUnitOfWork
	table      *worldmap.Table
	fail       bool
	calls      int
	staged     int
	injections int
	lastErr    error
}

func (g *r195CacheEntryCommitGate) WithinCharacterItems(ctx context.Context, characterID string, apply func(dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	g.calls++
	g.lastErr = g.base.WithinCharacterItems(ctx, characterID, func(inventories dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository) error {
		// The only cached map must already have finished loading before consuming.
		snap := g.table.MapCacheSnapshot()
		if !snap.Enabled || snap.Loads == 0 || snap.Entries != 1 || snap.Errors != 0 {
			return fmt.Errorf("entry transaction ran before successful cached map preparation: %+v", snap)
		}
		before, found, err := inventories.Load(ctx, characterID)
		if err != nil || !found || before.Slots["0:0"].Count != 2 {
			return fmt.Errorf("unexpected pre-consume inventory: found=%t count=%d err=%v", found, before.Slots["0:0"].Count, err)
		}
		if err := apply(inventories, equipment); err != nil {
			return err
		}
		after, found, err := inventories.Load(ctx, characterID)
		if err != nil || !found || after.Slots["0:0"].ItemID != 4726 || after.Slots["0:0"].Count != 1 {
			return fmt.Errorf("real consume callback did not stage exactly one ticket: found=%t stack=%+v err=%v", found, after.Slots["0:0"], err)
		}
		g.staged++
		if g.fail {
			g.injections++
			return errR195CacheEntryCommit
		}
		return nil
	})
	return g.lastErr
}

func TestR195CachePreparedMapTicketTransactionFailurePreservesAssetsAndRetry(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	ctx := context.Background()
	s, session := newDungeonSelectRefusalFixture(t, dnfrepo.CharacterRecord{
		CharacterID: "99", AccountID: "account-1", Level: 20,
		Stats: map[string]int64{"fatigue": 100, "town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3},
	}, nil)
	source := bridgeDungeonPVF(false)
	source["dungeon/test.dgn"] += "[required item]\n4726 1 1\n[/required item]\n"
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	wantMap, found := table.FindMap(100)
	if !found || len(wantMap.Monsters) == 0 {
		t.Fatal("eager fixture has no complete start map")
	}
	s.worldMapTable, s.worldMapResolver, s.dungeonMonsterTable = table, resolver, monsters
	s.options.mapDefinitionCache = true
	options := worldmap.DefaultMapCacheOptions()
	options.Capacity, options.PinnedBytes = 1, 0
	if err := table.EnableMapCache(source, options); err != nil {
		t.Fatal(err)
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.CharacterItems == nil {
		t.Fatal("fixture requires the real memory item transaction")
	}
	s.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	if _, err := s.updateCurrentCharacterFatigue(ctx, session, false); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{"0:0": {ItemID: 4726, Count: 2}}}); err != nil {
		t.Fatal(err)
	}
	beforeCharacter, found, err := repos.Character.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("load initial character: found=%t err=%v", found, err)
	}
	beforeInventory, found, err := repos.Inventory.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("load initial inventory: found=%t err=%v", found, err)
	}
	beforeEquipment, equipmentFound, err := repos.Equipment.Load(ctx, "99")
	if err != nil {
		t.Fatal(err)
	}
	gate := &r195CacheEntryCommitGate{base: repos.CharacterItems, table: table, fail: true}
	repos.CharacterItems = gate
	// repositoryGroup returned a copy; replace the provider as well as that copy.
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	bindDungeonSelectorOriginForTestAt(t, s, session, 38, 1, 450, 234)
	body := make([]byte, 21)
	binary.LittleEndian.PutUint32(body, 700)
	conn := session.conn.(*bufferConn)
	conn.write.Reset()
	if snap := table.MapCacheSnapshot(); snap.Loads != 0 || snap.Entries != 0 {
		t.Fatalf("entry must start from a cold cache: %+v", snap)
	}
	if err := s.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatal(err)
	}
	if gate.calls != 1 || gate.staged != 1 || gate.injections != 1 || !errors.Is(gate.lastErr, errR195CacheEntryCommit) {
		t.Fatalf("late transaction fault not reached: calls=%d staged=%d injections=%d err=%v", gate.calls, gate.staged, gate.injections, gate.lastErr)
	}
	// Reuse the exact refusal/scene-progress contract, including no trailing start
	// packets. A generic transaction error must not masquerade as success.
	assertDungeonSelectRefusal(t, s, session, currentSelectDungeonUnavailableReason, "dungeon_owner_validation_failed", "entry_unavailable")
	afterCharacter, found, err := repos.Character.Load(ctx, "99")
	if err != nil || !found || !reflect.DeepEqual(beforeCharacter, afterCharacter) {
		t.Fatalf("failed commit changed character/fatigue: found=%t err=%v before=%+v after=%+v", found, err, beforeCharacter, afterCharacter)
	}
	afterInventory, found, err := repos.Inventory.Load(ctx, "99")
	if err != nil || !found || !reflect.DeepEqual(beforeInventory, afterInventory) {
		t.Fatalf("failed commit changed persistent inventory: found=%t err=%v before=%+v after=%+v", found, err, beforeInventory, afterInventory)
	}
	afterEquipment, found, err := repos.Equipment.Load(ctx, "99")
	if err != nil || found != equipmentFound || !reflect.DeepEqual(beforeEquipment, afterEquipment) {
		t.Fatalf("failed commit changed equipment: found=%t err=%v", found, err)
	}
	checkMap := func() {
		t.Helper()
		got, found, err := table.FindMapWithError(100)
		if err != nil || !found || !reflect.DeepEqual(got, wantMap) {
			t.Fatalf("prepared map cache lost eager semantics: found=%t err=%v got=%+v want=%+v", found, err, got, wantMap)
		}
		snap := table.MapCacheSnapshot()
		if snap.Loads != 1 || snap.Errors != 0 || snap.Entries != 1 || snap.EstimatedBytes <= 0 || snap.EstimatedBytes > options.MaxBytes {
			t.Fatalf("failed entry polluted or bypassed retained map: %+v", snap)
		}
	}
	checkMap()
	gate.fail = false
	conn.write.Reset()
	if err := s.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatal(err)
	}
	if gate.calls != 2 || gate.staged != 2 || gate.injections != 1 || gate.lastErr != nil {
		t.Fatalf("retry did not commit once: calls=%d staged=%d injections=%d err=%v", gate.calls, gate.staged, gate.injections, gate.lastErr)
	}
	afterInventory, found, err = repos.Inventory.Load(ctx, "99")
	if err != nil || !found || afterInventory.Slots["0:0"].Count != 1 {
		t.Fatalf("retry ticket count: found=%t err=%v inventory=%+v", found, err, afterInventory)
	}
	afterCharacter, found, err = repos.Character.Load(ctx, "99")
	if err != nil || !found || afterCharacter.Stats["fatigue"] != 99 || !sessionHasActiveDungeonRuntime(session) {
		t.Fatalf("retry runtime/fatigue: found=%t err=%v fatigue=%d active=%t", found, err, afterCharacter.Stats["fatigue"], sessionHasActiveDungeonRuntime(session))
	}
	starts := 0
	for raw := conn.write.Bytes(); len(raw) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, raw)
		if packet.Header.MsgID == currentDungeonStartNotification {
			starts++
		}
		raw = rest
	}
	if starts != 1 {
		t.Fatalf("retry start-map notifications=%d want=1", starts)
	}
	checkMap()
}
