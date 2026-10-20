package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestReviewActualRaidFullInventoryRewardRequiresDurableDelivery(t *testing.T) {
	catalog, config := actualAntonRewardFixture(t)
	for phase, p := range config.Phases {
		for _, c := range p.Choices {
			t.Logf("actual phase=%d choice=%+v", phase, c)
		}
	}
	var choice raid.AntonRewardChoice
	for _, c := range config.Phases[0].Choices {
		if c.ContainerID == 10304483 {
			choice = c
		}
	}
	if choice.ContainerID == 0 {
		t.Fatal("actual direct box absent")
	}
	now := time.Now().UTC()
	plan, err := resolveCurrentAntonConfiguredReward(catalog, choice, zeroAntonRoll, now)
	if err != nil || len(plan.Rewards) != 1 {
		t.Fatal(err)
	}
	def := plan.Rewards[0].Definition
	if _, shared := currentDisjointWarehouseFixedSlots[def.ItemID]; shared {
		t.Fatal("fixture must test nonshared placement")
	}
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "account"}); err != nil {
		t.Fatal(err)
	}
	slots := map[string]repo.ItemStack{}
	for slot := def.SlotStart; slot <= def.SlotEnd; slot++ {
		slots[currentDungeonPickupMainSlotKey(slot)] = repo.ItemStack{ItemID: 10096296, Count: 1}
	}
	for slot := currentDungeonPickupQuickSlotStart; slot <= currentDungeonPickupQuickSlotEnd; slot++ {
		slots[currentDungeonPickupMainSlotKey(slot)] = repo.ItemStack{ItemID: 10096296, Count: 1}
	}
	if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "31", Slots: slots}); err != nil {
		t.Fatal(err)
	}
	cmd := raid.RewardCommand{AccountID: "account", CharacterID: "31", InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, Phase: 0, Kind: map[string]byte{"party_card": 1, "pcroom_card": 2, "squad_item": 3}[choice.Type], Card: 0, MinimumClears: config.Phases[0].MinimumClears, OwnedClears: 1, Fingerprint: plan.Fingerprint, CompletedAt: now}
	_, err = grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
	if err != nil {
		if !errors.Is(err, errDungeonPickupInventoryFull) {
			t.Fatalf("unexpected fixture failure %v", err)
		}
		char, _, loadErr := g.Character.Load(ctx, "31")
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		inv, _, loadErr := g.Inventory.Load(ctx, "31")
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if len(char.Stats) != 0 || len(inv.Slots) != len(slots) {
			t.Fatal("failed transaction leaked assets")
		}
		// Control: the identical frozen entitlement commits exactly once as soon
		// as its actual placement page has one free row. No source-plan change.
		delete(inv.Slots, currentDungeonPickupMainSlotKey(def.SlotStart))
		if e := g.Inventory.Save(ctx, inv); e != nil {
			t.Fatal(e)
		}
		first, e := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
		if e != nil || first.Replayed {
			t.Fatal("free-slot positive control failed", e)
		}
		repeat, e := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
		if e != nil || !repeat.Replayed {
			t.Fatal("receipt replay positive control failed", e)
		}
		t.Fatalf("source gap: actual reward=%d page=%d..%d full fails without durable delivery: %v; same plan free-row commit/replay passed", def.ItemID, def.SlotStart, def.SlotEnd, err)
	}

	inv, _, e := g.Inventory.Load(ctx, "31")
	if e != nil {
		t.Fatal(e)
	}
	if len(inv.Slots) != len(slots) {
		t.Fatal("mail grant mutated full inventory")
	}
	mail, found, e := g.Mailbox.Load(ctx, "31")
	if e != nil || !found || len(mail.Mails) != 1 {
		t.Fatal("no atomic overflow mail", e, mail)
	}
	for _, m := range mail.Mails {
		if m.RecipientCharacterID != "31" || len(m.Attachments) != 1 || m.Attachments[0].ItemID != int64(def.ItemID) || m.Attachments[0].Count != int64(plan.Rewards[0].Count) || len(m.Attachments[0].RawEntry) != currentItemListEntryWireSize || m.Attachments[0].Extra["source"] != "raid_reward" {
			t.Fatal("wrong durable attachment", m)
		}
	}
	repeat, e := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
	if e != nil || !repeat.Replayed {
		t.Fatal("mail receipt replay failed", e)
	}
	after, _, e := g.Mailbox.Load(ctx, "31")
	if e != nil || len(after.Mails) != 1 {
		t.Fatal("mail replay duplicated", e)
	}
	t.Log(fmt.Sprintf("full-page atomic mail+receipt accepted actual item %d", def.ItemID))
}

func TestReviewActualRaidFullPageDoesNotBlockAutomaticSquadAndPhaseBreak(t *testing.T) {
	f, _, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	catalog, _ := actualAntonRewardFixture(t)
	plan, e := resolveCurrentAntonRewardPlan(catalog, flow.Rewards, "squad_item", flow.Rank, func(n int64) (int64, error) { return n - 1, nil }, flow.CompletedAt)
	if e != nil || len(plan.Rewards) != 1 {
		t.Fatal(e)
	}
	def := plan.Rewards[0].Definition
	if _, shared := currentDisjointWarehouseFixedSlots[def.ItemID]; shared {
		t.Fatal("last actual squad outcome unexpectedly shared")
	}
	ctx := context.Background()
	inv, found, e := f.repos.Inventory.Load(ctx, "19")
	if e != nil || !found {
		t.Fatal(e)
	}
	for slot := def.SlotStart; slot <= def.SlotEnd; slot++ {
		inv.Slots[currentDungeonPickupMainSlotKey(slot)] = repo.ItemStack{ItemID: 10096296, Count: 1}
	}
	if e = f.repos.Inventory.Save(ctx, inv); e != nil {
		t.Fatal(e)
	}
	_, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, func(n int64) (int64, error) { return n - 1, nil })
	if len(errs) != 0 {
		if len(errs) != 1 || !errors.Is(errs[0], errDungeonPickupInventoryFull) {
			t.Fatalf("unexpected source failure %+v", errs)
		}
		if !flow.Party[19].Committed || !flow.Party[20].Committed || flow.Squad[19] == nil || flow.Squad[19].Committed || !flow.SquadShownAt.IsZero() || !flow.FinishedAt.IsZero() {
			t.Fatal("source failure boundary not as investigated")
		}
		frozen := flow.Squad[19].Plan.Fingerprint
		inv, _, e = f.repos.Inventory.Load(ctx, "19")
		if e != nil {
			t.Fatal(e)
		}
		delete(inv.Slots, currentDungeonPickupMainSlotKey(def.SlotStart))
		if e = f.repos.Inventory.Save(ctx, inv); e != nil {
			t.Fatal(e)
		}
		_, errs = f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(2*time.Second), func(n int64) (int64, error) { return n - 1, nil })
		if len(errs) != 0 || !flow.Squad[19].Committed || flow.Squad[19].Plan.Fingerprint != frozen {
			t.Fatal("same frozen squad free-slot retry control failed", errs)
		}
		_, errs = f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(22*time.Second), func(n int64) (int64, error) { return n - 1, nil })
		if len(errs) != 0 || flow.FinishedAt.IsZero() || flow.BreakUntil.IsZero() {
			t.Fatal("free-slot retry did not unblock phase break", errs)
		}
		t.Fatalf("actual automatic squad item=%d fullpage blocks phase finish until bag edited; frozen retry and break positive controls passed", def.ItemID)
	}
	if flow.Squad[19] == nil || !flow.Squad[19].Committed || flow.SquadShownAt.IsZero() {
		t.Fatal("mail squad grant did not advance flow")
	}
	mail, found, e := f.repos.Mailbox.Load(ctx, "19")
	if e != nil || !found || len(mail.Mails) != 1 {
		t.Fatal("automatic squad overflow mail missing", e)
	}
	_, errs = f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), func(n int64) (int64, error) { return n - 1, nil })
	if len(errs) != 0 || flow.FinishedAt.IsZero() || flow.BreakUntil.IsZero() {
		t.Fatal("atomic mail delivery did not unblock break", errs)
	}

}
