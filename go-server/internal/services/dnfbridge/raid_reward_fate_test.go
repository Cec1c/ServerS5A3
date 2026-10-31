package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/raidcurrency"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func fateRewardBridgeTime() time.Time {
	return time.Date(2026, 10, 27, 2, 0, 0, 0, time.FixedZone("CST", 8*60*60))
}

func fateRewardBridgePlan(t *testing.T, amount uint32) currentAntonRewardPlan {
	t.Helper()
	plan := currentAntonRewardPlan{
		Choice: raid.AntonRewardChoice{Type: "party_card", Rank: -1, Weight: 1, ContainerID: 10304474},
		Rewards: []currentBoosterReward{{
			Definition: dungeonDropItemDefinition{ItemID: raidcurrency.FateItemID, Kind: dungeonDropItemStackable, StackableType: "[material]"},
			Count:      amount,
		}},
	}
	encoded, err := json.Marshal(struct {
		Choice  raid.AntonRewardChoice
		Rewards []currentBoosterReward
	}{plan.Choice, plan.Rewards})
	if err != nil {
		t.Fatal(err)
	}
	plan.Fingerprint = sha256.Sum256(encoded)
	return plan
}

func TestRaidFateRowCreditsLedgerWithoutBagOrOverflowMail(t *testing.T) {
	account := repo.AccountRecord{AccountID: "account-fate"}
	assets := &raid.RewardAssets{Ledger: &account}
	cmd := raid.RewardCommand{CharacterID: "31", CompletedAt: fateRewardBridgeTime()}
	reward := currentBoosterReward{
		Definition: dungeonDropItemDefinition{ItemID: raidcurrency.FateItemID, Kind: dungeonDropItemStackable, StackableType: "[material]"},
		Count:      120,
	}
	keys, accountChanged, mailed, err := grantCurrentAntonRewardRow(context.Background(), assets, nil, reward, cmd)
	if err != nil || len(keys) != 0 || accountChanged || mailed || !assets.LedgerChanged {
		t.Fatalf("fate row keys=%v account=%t mailed=%t ledger=%t err=%v", keys, accountChanged, mailed, assets.LedgerChanged, err)
	}
	state, err := raidcurrency.Read(account, cmd.CompletedAt)
	if err != nil || state.Balance != 120 || state.Earned != 120 {
		t.Fatalf("fate state=%+v err=%v", state, err)
	}
}

func TestRaidFatePlanCommitsLedgerReceiptAndSkipsBag(t *testing.T) {
	ctx := context.Background()
	group := memory.NewMemoryGroup()
	if err := group.Account.Save(ctx, repo.AccountRecord{AccountID: "account-fate", Metadata: map[string]string{"keep": "yes"}}); err != nil {
		t.Fatal(err)
	}
	if err := group.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "account-fate", Stats: map[string]int64{}}); err != nil {
		t.Fatal(err)
	}
	if err := group.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	accountBefore, _, _ := group.Account.Load(ctx, "account-fate")
	if _, _, err := raidcurrency.Credit(&accountBefore, 2490, fateRewardBridgeTime()); err != nil {
		t.Fatal(err)
	}
	if err := group.Account.Save(ctx, accountBefore); err != nil {
		t.Fatal(err)
	}
	plan := fateRewardBridgePlan(t, 100)
	cmd := raid.RewardCommand{
		AccountID:     "account-fate",
		CharacterID:   "31",
		InstanceID:    "00112233445566778899aabbccddeeff",
		Mode:          1,
		Phase:         0,
		Kind:          1,
		MinimumClears: 1,
		OwnedClears:   1,
		Fingerprint:   plan.Fingerprint,
		CompletedAt:   fateRewardBridgeTime(),
	}
	first, err := grantCurrentAntonRewardPlan(ctx, group, nil, plan, cmd)
	if err != nil || first.Replayed || !first.LedgerChanged || len(first.GrantSlots) != 0 {
		t.Fatalf("first fate grant result=%+v err=%v", first, err)
	}
	second, err := grantCurrentAntonRewardPlan(ctx, group, nil, plan, cmd)
	if err != nil || !second.Replayed || second.LedgerChanged || len(second.GrantSlots) != 0 {
		t.Fatalf("replayed fate grant result=%+v err=%v", second, err)
	}
	for _, grant := range []currentAntonRewardGrantResult{first, second} {
		if grant.Plan.FateGranted == nil || *grant.Plan.FateGranted != 10 {
			t.Fatalf("cap/replay receipt=%+v", grant)
		}
		display := currentAntonPartyDisplayItems(grant.Plan)
		if len(display) != 1 || display[0].Quantity != 10 {
			t.Fatalf("display=%+v want actual granted 10", display)
		}
		if grant.Plan.Fingerprint != plan.Fingerprint || grant.Plan.Rewards[0].Count != 100 {
			t.Fatal("display mutated immutable entitlement")
		}
	}
	account, _, _ := group.Account.Load(ctx, "account-fate")
	state, err := raidcurrency.Read(account, cmd.CompletedAt)
	if err != nil || state.Balance != 2500 || state.Earned != 2500 || account.Metadata["keep"] != "yes" {
		t.Fatalf("fate account state=%+v err=%v metadata=%#v", state, err, account.Metadata)
	}
	inventory, _, _ := group.Inventory.Load(ctx, "31")
	if len(inventory.Slots) != 0 {
		t.Fatalf("fate reward created list-0 rows: %#v", inventory.Slots)
	}
	mailbox, found, err := group.Mailbox.Load(ctx, "31")
	if err != nil || found && len(mailbox.Mails) != 0 {
		t.Fatalf("fate reward created overflow mail: found=%t mailbox=%+v err=%v", found, mailbox, err)
	}
	if _, err := raidcurrency.Debit(&account, 40, cmd.CompletedAt); err != nil {
		t.Fatal(err)
	}
	if err := group.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	afterSpend, err := grantCurrentAntonRewardPlan(ctx, group, nil, plan, cmd)
	if err != nil || !afterSpend.Replayed || afterSpend.Plan.FateGranted == nil || *afterSpend.Plan.FateGranted != 10 {
		t.Fatalf("replay after spending inferred award from balance: %+v err=%v", afterSpend, err)
	}
	// A distinct earned receipt at cap must still settle, but disclose no item.
	cmd.InstanceID = "00112233445566778899aabbccddeefe"
	capped, err := grantCurrentAntonRewardPlan(ctx, group, nil, plan, cmd)
	if err != nil || capped.Replayed || capped.Plan.FateGranted == nil || *capped.Plan.FateGranted != 0 || len(currentAntonPartyDisplayItems(capped.Plan)) != 0 {
		t.Fatalf("capped display=%+v err=%v", capped, err)
	}
	finalAccount, _, _ := group.Account.Load(ctx, "account-fate")
	state, err = raidcurrency.Read(finalAccount, cmd.CompletedAt)
	if err != nil || state.Balance != 2460 || state.Earned != 2500 {
		t.Fatalf("spending/replay/cap=%+v err=%v", state, err)
	}
}
