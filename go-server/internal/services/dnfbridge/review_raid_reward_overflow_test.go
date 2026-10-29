package dnfbridge

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
	"time"
)

func TestReviewActualRaidFullInventoryRewardRequiresDurableDelivery(t *testing.T) {
	catalog, config := actualAntonRewardFixture(t)
	g, plan, cmd := reviewRaidOverflowFixture(t, catalog, config, 10304483)
	privateRaidAssertContents(t, plan.Rewards, map[uint32]uint32{10093974: 20, 100050518: 1})
	ctx := context.Background()
	before, _, _ := g.Inventory.Load(ctx, "31")
	result, err := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
	if err != nil || result.Replayed {
		t.Fatal(err)
	}
	after, _, _ := g.Inventory.Load(ctx, "31")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("overflow changed occupied inventory")
	}
	mail, found, err := g.Mailbox.Load(ctx, "31")
	if err != nil || !found || len(mail.Mails) != 2 {
		t.Fatal("two final reward rows not delivered", err, mail)
	}
	got := map[uint32]uint32{}
	for _, m := range mail.Mails {
		if m.RecipientCharacterID != "31" || len(m.Attachments) != 1 {
			t.Fatal("wrong recipient or attachment", m)
		}
		a := m.Attachments[0]
		if len(a.RawEntry) != currentItemListEntryWireSize || a.Extra["source"] != "raid_reward" {
			t.Fatal("invalid durable attachment", a)
		}
		got[uint32(a.ItemID)] += uint32(a.Count)
	}
	if !reflect.DeepEqual(got, map[uint32]uint32{10093974: 20, 100050518: 1}) {
		t.Fatal("missing final contents", got)
	}
	replay, err := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
	if err != nil || !replay.Replayed {
		t.Fatal("receipt replay failed", err)
	}
	mails, count := reviewRaidOverflowMailCount(t, g, "31")
	if mails != 2 || count != 21 {
		t.Fatal("replay duplicated contents", mails, count)
	}
}

func TestReviewActualRaidFullPageDoesNotBlockAutomaticSquadAndPhaseBreak(t *testing.T) {
	f, _, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	catalog, _ := actualAntonRewardFixture(t)
	roll := func(n int64) (int64, error) { return n - 1, nil }
	plan, err := resolveCurrentAntonRewardPlan(catalog, flow.Rewards, "squad_item", flow.Rank, roll, flow.CompletedAt)
	if err != nil || len(plan.Rewards) != 2 {
		t.Fatal(err)
	}
	ctx := context.Background()
	inv, _, _ := f.repos.Inventory.Load(ctx, "19")
	for _, r := range plan.Rewards {
		for slot := r.Definition.SlotStart; slot <= r.Definition.SlotEnd; slot++ {
			inv.Slots[currentDungeonPickupMainSlotKey(slot)] = repo.ItemStack{ItemID: 10096296, Count: 1}
		}
	}
	for slot := currentDungeonPickupQuickSlotStart; slot <= currentDungeonPickupQuickSlotEnd; slot++ {
		inv.Slots[currentDungeonPickupMainSlotKey(slot)] = repo.ItemStack{ItemID: 10096296, Count: 1}
	}
	if err := f.repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, roll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), roll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if flow.Squad[19] == nil || !flow.Squad[19].Committed || flow.SquadShownAt.IsZero() {
		t.Fatal("overflow failed to advance squad")
	}
	mail, found, err := f.repos.Mailbox.Load(ctx, "19")
	if err != nil || !found {
		t.Fatal("squad overflow mail missing", err)
	}
	// A full material page can also mail the party reward. Check each squad
	// leaf instead of assuming a fixed total mailbox size.
	for _, r := range plan.Rewards {
		var count int64
		for _, m := range mail.Mails {
			for _, a := range m.Attachments {
				if a.ItemID == int64(r.Definition.ItemID) {
					count += a.Count
				}
			}
		}
		if count != int64(r.Count) {
			t.Fatal("squad contents lost", r.Definition.ItemID, count, r.Count)
		}
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(40*time.Second), func(int64) (int64, error) { t.Fatal("frozen reward rerolled"); return 0, nil }); len(errs) != 0 || flow.FinishedAt.IsZero() || flow.BreakUntil.IsZero() {
		t.Fatal("overflow did not finish phase", errs)
	}
}
