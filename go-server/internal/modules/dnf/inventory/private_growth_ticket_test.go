package inventory

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"testing"
	"time"
)

func growthFixture(t *testing.T, id int64, level byte) (repo.Group, *Owner, GrowthTicketApply) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	ticket := repo.ItemStack{ItemID: id, Count: 1, RawEntry: make([]byte, 119), Extra: map[string]string{"item_kind": "equipment"}}
	if err := InitializeGrowthTicket(&ticket, 7); err != nil {
		t.Fatal(err)
	}
	// Retired IDs model already-owned instances. New grants are always white.
	if _, attr, _ := GrowthTicketType(id); attr != 0 {
		setAmplifyState(&ticket, attr, 7)
	}
	setUpgradeLevel(&ticket, level)
	target := repo.ItemStack{ItemID: 700, Count: 1, RawEntry: make([]byte, 119), Extra: map[string]string{"item_kind": "equipment", "custom_keep": "unchanged"}}
	target.RawEntry[0x57] = 6
	setUpgradeLevel(&target, 2)
	saveTestInventory(t, ctx, g, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{slotKey(0, 9): ticket, slotKey(0, 10): target}})
	owner, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	return g, owner, GrowthTicketApply{CharacterID: 77, TicketSlot: 9, TargetSlot: 10, Ticket: cloneStack(ticket), Target: cloneStack(target), TargetAllowed: true, TargetAmplifyValue: 9, Now: time.Now()}
}
func TestPrivateGrowthTicketAllTypesAndLevels(t *testing.T) {
	for id := int64(990033101); id <= 990033105; id++ {
		for level := byte(3); level <= 31; level++ {
			g, o, q := growthFixture(t, id, level)
			out, err := o.ApplyGrowthTicket(context.Background(), q)
			if err != nil {
				t.Fatalf("id=%d level=%d: %v", id, level, err)
			}
			_, attr, _ := GrowthTicketType(id)
			actual, val := upgradeAmplifyState(out)
			if upgradeLevelOf(out) != level || actual != attr || (attr != 0 && val != 9) || out.RawEntry[0x57] != 6 || out.Extra["custom_keep"] != "unchanged" {
				t.Fatalf("lost fields: %+v", out)
			}
			got, _, _ := g.Inventory.Load(context.Background(), "77")
			if _, exists := got.Slots[slotKey(0, 9)]; exists {
				t.Fatal("ticket survived")
			}
			if _, err := o.ApplyGrowthTicket(context.Background(), q); err == nil {
				t.Fatal("replay succeeded")
			}
		}
	}
}
func TestPrivateGrowthTicketRejectsWithoutMutation(t *testing.T) {
	for _, name := range []string{"zero", "samelevel", "changed_ticket", "changed_target", "wrong_slot", "same_slot", "forbidden", "expired_ticket", "expired_target", "locked_ticket", "locked_target", "wrong_attribute", "target_ticket", "red_reinforce"} {
		t.Run(name, func(t *testing.T) {
			g, o, q := growthFixture(t, 990033102, 12)
			switch name {
			case "zero":
				setUpgradeLevel(&q.Ticket, 0)
			case "samelevel":
				setUpgradeLevel(&q.Ticket, 2)
			case "changed_ticket":
				q.Ticket.Extra["changed"] = "yes"
			case "changed_target":
				q.Target.Extra["changed"] = "yes"
			case "wrong_slot":
				q.TicketSlot = 121
			case "same_slot":
				q.TargetSlot = q.TicketSlot
			case "forbidden":
				q.TargetAllowed = false
			case "expired_ticket":
				q.Ticket.ExpireAt = q.Now.Add(-time.Second)
			case "expired_target":
				q.Target.ExpireAt = q.Now.Add(-time.Second)
			case "locked_ticket":
				q.Ticket.Extra["equipment_lock_state"] = "locked"
			case "locked_target":
				q.Target.Extra["equipment_lock_state"] = "locked"
			case "wrong_attribute":
				setAmplifyState(&q.Ticket, 128, 7)
			case "target_ticket":
				q.Target.ItemID = 990033101
			case "red_reinforce":
				q.Ticket.ItemID = 990033101
				setAmplifyState(&q.Ticket, 0, 0)
				setAmplifyState(&q.Target, 3, 7)
			}
			// Semantic rejection cases must be tested against their actual persisted
			// rows, rather than accidentally passing only the snapshot mismatch check.
			before, _, _ := g.Inventory.Load(context.Background(), "77")
			if name != "changed_ticket" && name != "changed_target" && name != "wrong_slot" && name != "same_slot" {
				before.Slots[slotKey(0, 9)] = cloneStack(q.Ticket)
				before.Slots[slotKey(0, 10)] = cloneStack(q.Target)
				saveTestInventory(t, context.Background(), g, before)
			}
			before, _, _ = g.Inventory.Load(context.Background(), "77")
			if _, err := o.ApplyGrowthTicket(context.Background(), q); err == nil {
				t.Fatal("invalid application succeeded")
			}
			after, _, _ := g.Inventory.Load(context.Background(), "77")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejection mutated inventory")
			}
		})
	}
}
func TestPrivateGrowthTicketPropertyMapping(t *testing.T) {
	for id, want := range map[int64]byte{990033101: 0, 990033102: 3, 990033103: 4, 990033104: 1, 990033105: 2} {
		_, a, ok := GrowthTicketType(id)
		if !ok || a != want {
			t.Fatal(id, a)
		}
	}
	if _, _, ok := GrowthTicketType(700); ok {
		t.Fatal("ordinary equipment matched")
	}
}

type growthFailAfterSave struct{ repo.InventoryRepository }

func (r growthFailAfterSave) Save(ctx context.Context, v repo.InventoryRecord) error {
	if err := r.InventoryRepository.Save(ctx, v); err != nil {
		return err
	}
	return errors.New("injected failure after inventory write")
}

type growthFailItems struct{ inner repo.CharacterItemUnitOfWork }

func (x growthFailItems) WithinCharacterItems(ctx context.Context, id string, apply func(repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return x.inner.WithinCharacterItems(ctx, id, func(i repo.InventoryRepository, e repo.EquipmentRepository) error {
		return apply(growthFailAfterSave{i}, e)
	})
}
func TestPrivateGrowthTicketPostWriteFailureRollsBackBothRows(t *testing.T) {
	g, _, q := growthFixture(t, 990033105, 31)
	ctx := context.Background()
	before, _, _ := g.Inventory.Load(ctx, "77")
	g.CharacterItems = growthFailItems{g.CharacterItems}
	o, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApplyGrowthTicket(ctx, q); err == nil {
		t.Fatal("injected failure missing")
	}
	after, _, _ := g.Inventory.Load(ctx, "77")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("ticket/target transaction failed to roll back")
	}
}
