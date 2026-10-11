package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"testing"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFLukeStartRejectsMissingOriginalPrerequisiteWithoutCharging(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires actual PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, a, 1)
	f.service.options.currentClientVersion = "official-24.9"
	f.state.CurrentLayout = true
	f.state.CurrentUIFlags = [3]byte{}
	f.state.TailFlag = 0
	f.state.RouteOrRaidType = 1
	f.state.Members[0].GroupIndex = 1
	session := f.session()
	session.channel.ID, session.channel.Type = 32, 32
	session.loginPacketSeen, session.accountFromToken = true, true
	f.service.bindGameSessionCharacter(session, 19)
	ctx := context.Background()
	ticket, err := f.service.currentRaidAdmissionRule(session)
	if err != nil {
		t.Fatal(err)
	}
	q := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{2903: {Status: "completed"}, 12830: {Status: "active"}}}
	inv := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:50": {ItemID: int64(ticket.ItemID), Count: 2}, "0:51": {ItemID: 10159142, Count: 40}}}
	if err := f.repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Quest.Save(ctx, q); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.service.startRuntimeRaidWithEntryCost(session); ok || err == nil || f.state.Started || f.state.Battle != nil {
		t.Fatal("missing Anton prerequisite admitted", ok, err)
	}
	q.States[12830] = dnfrepo.QuestState{Status: "completed"}
	if err := f.repos.Quest.Save(ctx, q); err != nil {
		t.Fatal(err)
	}
	inv.Slots["0:51"] = dnfrepo.ItemStack{ItemID: 10159142, Count: 39}
	if err := f.repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.service.startRuntimeRaidWithEntryCost(session); ok || err == nil || f.state.Started || f.state.Battle != nil {
		t.Fatal("missing root holding admitted", ok, err)
	}
	after, _, _ := f.repos.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(inv, after) {
		t.Fatal("rejected start modified inventory")
	}
	inv.Slots["0:51"] = dnfrepo.ItemStack{ItemID: 10159142, Count: 40}
	if err := f.repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if state, ok, err := f.service.startRuntimeRaidWithEntryCost(session); !ok || err != nil || !state.Started || state.Battle == nil || state.Battle.Mode != 2 {
		t.Fatal("qualified normal Luke start failed", ok, err)
	}
	after, _, _ = f.repos.Inventory.Load(ctx, "19")
	if after.Slots["0:50"].Count != 1 || after.Slots["0:51"].Count != 40 {
		t.Fatal("entrance holding consumed instead of ticket", after)
	}
}

func TestRealPVFLukeEntranceAllMembersAndAtomicHoldingCheck(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires actual PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := dnfquest.LoadLukeEntryRequirement(a)
	if err != nil || rule != (dnfquest.LukeEntryRequirement{QuestID: 2903, AntonQuestID: 12830, ItemID: 10159142, Count: 40}) {
		t.Fatal(rule, err)
	}
	f := raidSafetyFixture(t, a, 2)
	f.service.options.currentClientVersion = "official-24.9"
	session := f.session()
	session.channel.ID, session.channel.Type = 32, 32
	ctx := context.Background()
	for _, id := range []string{"19", "20"} {
		if err := f.repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: id, States: map[int64]dnfrepo.QuestState{2903: {Status: "completed"}, 12830: {Status: "completed"}}}); err != nil {
			t.Fatal(err)
		}
		inv, _, _ := f.repos.Inventory.Load(ctx, id)
		inv.Slots["0:51"] = dnfrepo.ItemStack{ItemID: rule.ItemID, Count: rule.Count}
		if err := f.repos.Inventory.Save(ctx, inv); err != nil {
			t.Fatal(err)
		}
	}
	holdings, err := f.service.currentRaidHoldingRequirements(ctx, session, *f.state)
	if err != nil || len(holdings) != 1 {
		t.Fatal(holdings, err)
	}
	// A follower's active Anton quest cannot be treated as completed.
	q, _, _ := f.repos.Quest.Load(ctx, "20")
	q.States[12830] = dnfrepo.QuestState{Status: "active", ProgressValue: 0}
	if err := f.repos.Quest.Save(ctx, q); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.currentRaidHoldingRequirements(ctx, session, *f.state); err == nil {
		t.Fatal("unfinished follower prerequisite admitted")
	}
	// The ownership validation is performed under inventory row locks, including
	// a late follower shortage; no leader ticket may leak from the failed batch.
	inv, _, _ := f.repos.Inventory.Load(ctx, "20")
	inv.Slots["0:51"] = dnfrepo.ItemStack{ItemID: rule.ItemID, Count: 39}
	if err := f.repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	before, _, _ := f.repos.Inventory.Load(ctx, "19")
	ticket := raid.AdmissionRule{ItemID: raidStartGuardTestMaterial, Count: 1}
	if _, err := f.service.consumeRaidEntryCostForCharacters(ctx, []string{"19", "20"}, "19", "account-1", ticket, holdings...); err == nil {
		t.Fatal("late holding shortage admitted")
	}
	after, _, _ := f.repos.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed follower holding check charged leader")
	}
	inv.Slots["0:51"] = dnfrepo.ItemStack{ItemID: rule.ItemID, Count: 40}
	if err := f.repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if total, err := f.service.consumeRaidEntryCostForCharacters(ctx, []string{"19", "20"}, "19", "account-1", ticket, holdings...); err != nil || total != 2 {
		t.Fatal(total, err)
	}
	for _, id := range []string{"19", "20"} {
		inv, _, _ := f.repos.Inventory.Load(ctx, id)
		if raidEntryCostOwnedCount(inv, rule.ItemID) != 40 || inv.Slots["0:50"].Count != 2 {
			t.Fatal("holding was consumed or ticket not charged", id, inv)
		}
	}
}
