package dungeon

import (
	"context"
	"errors"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

type cycleLateProbe struct{ entry, tickets, fatigue int64 }
type cycleLateCharacter struct {
	dnfrepo.CharacterRepository
	bag   dnfrepo.InventoryRepository
	probe *cycleLateProbe
}

func (r cycleLateCharacter) Save(ctx context.Context, c dnfrepo.CharacterRecord) error {
	r.probe.entry = c.Stats["dungeon_cycle_entry_5097_used"]
	r.probe.fatigue = c.Stats["fatigue"]
	bag, _, _ := r.bag.Load(ctx, c.CharacterID)
	r.probe.tickets = bag.Slots["0:20"].Count
	return errors.New("cycle character save failure")
}
func (r cycleLateCharacter) SaveFields(ctx context.Context, c dnfrepo.CharacterRecord, _ ...dnfrepo.CharacterField) error {
	return r.Save(ctx, c)
}

type cycleLateAssets struct {
	base  dnfrepo.CharacterAssetUnitOfWork
	probe *cycleLateProbe
}

func (u cycleLateAssets) WithinCharacterAssets(ctx context.Context, id string, apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return u.base.WithinCharacterAssets(ctx, id, func(c dnfrepo.CharacterRepository, b dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
		return apply(cycleLateCharacter{c, b, u.probe}, b, e)
	})
}
func TestCycleEntryLateSaveFailureRollsBackTicketsFatigueAndCounter(t *testing.T) {
	ctx := context.Background()
	repos, _, cmd := dailyEntryFixture(t, 20)
	probe := &cycleLateProbe{}
	repos.CharacterAssets = cycleLateAssets{repos.CharacterAssets, probe}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	cmd.DungeonID = 5097
	cmd.CycleEntry = true
	cmd.RequiredItemWeight = 0
	cmd.InTransaction = func(c *dnfrepo.CharacterRecord) error { c.Stats["fatigue"]--; return nil }
	if _, err := owner.ConsumeDailyEntry(ctx, cmd); err == nil {
		t.Fatal("late save succeeded")
	}
	if probe.entry != 1 || probe.fatigue != 99 || probe.tickets != 18 {
		t.Fatalf("not a late failure: %+v", probe)
	}
	c, _, _ := repos.Character.Load(ctx, "19")
	bag, _, _ := repos.Inventory.Load(ctx, "19")
	used, _ := CycleEntryUsed(c, 5097, cmd.UpdatedAt)
	if used != 0 || c.Stats["fatigue"] != 100 || bag.Slots["0:20"].Count != 20 {
		t.Fatalf("late failure changed assets: %+v %+v", c, bag)
	}
}
