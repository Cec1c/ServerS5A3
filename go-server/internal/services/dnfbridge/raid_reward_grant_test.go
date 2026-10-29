package dnfbridge

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func actualAntonRewardFixture(t *testing.T) (*pvfDungeonDropCatalog, raid.AntonRewardConfiguration) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current production PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	config, err := raid.LoadAntonRewardConfiguration(archive)
	if err != nil {
		t.Fatal(err)
	}
	return catalog, config
}

// All persistence in these tests is an in-memory owner fixture. No real
// character, inventory or weekly counter is created or modified.
func TestRealPVFAntonRewardPlansAndAtomicGrants(t *testing.T) {
	catalog, config := actualAntonRewardFixture(t)
	now := time.Now().UTC()
	if err := validateCurrentAntonRewardDependencies(catalog, config, now); err != nil {
		t.Fatal(err)
	}
	want := map[uint32]map[uint32]uint32{
		10304474: privateRaidExpectedZero(10304474, 1),
		10304481: privateRaidExpectedZero(10304481, 1),
		10304479: privateRaidExpectedZero(10304479, 1),
		10304483: privateRaidExpectedZero(10304483, 1),
		10304484: privateRaidExpectedZero(10304484, 1),
		10304475: privateRaidExpectedZero(10304475, 1),
		10304480: privateRaidExpectedZero(10304480, 1),
		10157783: privateRaidExpectedZero(10157783, 1),
		10304485: privateRaidExpectedZero(10304485, 1),
		10304486: privateRaidExpectedZero(10304486, 1),
		10304487: privateRaidExpectedZero(10304487, 1),
	}

	seen := map[uint32]bool{}
	for phase, p := range config.Phases {
		for _, choice := range p.Choices {
			if seen[choice.ContainerID] {
				continue
			}
			seen[choice.ContainerID] = true
			plan, err := resolveCurrentAntonConfiguredReward(catalog, choice, func(int64) (int64, error) { return 0, nil }, now)
			if err != nil {
				t.Fatal(choice.ContainerID, err)
			}
			w, ok := want[choice.ContainerID]
			if !ok {
				t.Fatalf("unexpected source %d", choice.ContainerID)
			}
			privateRaidAssertContents(t, plan.Rewards, w)

			g := memory.NewMemoryGroup()
			ctx := context.Background()
			if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "account"}); err != nil {
				t.Fatal(err)
			}
			if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{"0:50": {ItemID: 10096296, Count: 2}}}); err != nil {
				t.Fatal(err)
			}
			kind := map[string]byte{"party_card": 1, "pcroom_card": 2, "squad_item": 3}[choice.Type]
			cmd := raid.RewardCommand{AccountID: "account", CharacterID: "31", InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, Phase: byte(phase), Kind: kind, Card: 0, MinimumClears: p.MinimumClears, OwnedClears: 1, Fingerprint: plan.Fingerprint, CompletedAt: now}
			result, err := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
			if err != nil || result.Replayed {
				t.Fatalf("container=%d first grant %v %+v", choice.ContainerID, err, result)
			}
			inv, _, _ := g.Inventory.Load(ctx, "31")
			account, _, _ := g.AccountInventory.Load(ctx, "account")
			if inv.Slots["0:50"].Count != 2 {
				t.Fatal("raid grant consumed ticket/source box")
			}
			for itemID, count := range w {
				var found int64
				for _, slots := range []map[string]repo.ItemStack{inv.Slots, account.Slots} {
					for _, stack := range slots {
						if stack.ItemID == int64(itemID) {
							found += stack.Count
							if stack.Extra["source"] != "raid_reward" || len(stack.RawEntry) != currentItemListEntryWireSize {
								t.Fatalf("invalid persisted reward metadata %+v", stack)
							}
						}
					}
				}
				if found != int64(count) {
					t.Fatalf("source=%d item=%d persisted=%d want=%d", choice.ContainerID, itemID, found, count)
				}
			}

			replay, err := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
			if err != nil || !replay.Replayed || len(replay.GrantSlots) != 0 || replay.AccountChanged {
				t.Fatal("repeat grant not receipt-only", err, replay)
			}
			changed := cloneCurrentAntonRewardPlan(plan)
			changed.Rewards[0].Count++
			if _, err := grantCurrentAntonRewardPlan(ctx, g, catalog, changed, cmd); !errors.Is(err, raid.ErrRewardReceipt) {
				t.Fatal("changed frozen amount accepted", err)
			}
			t.Logf("container=%d actual=%v shared=%t receipt_replay=true", choice.ContainerID, w, result.AccountChanged)
		}
	}
	if len(seen) != len(want) {
		t.Fatal("configured reward coverage changed", len(seen), len(want))
	}
}

func TestRealPVFAntonRewardRNGFailuresAndFrozenRetry(t *testing.T) {
	catalog, config := actualAntonRewardFixture(t)
	now := time.Now().UTC()
	for _, roll := range []func(int64) (int64, error){func(int64) (int64, error) { return -1, nil }, func(n int64) (int64, error) { return n, nil }, func(int64) (int64, error) { return 0, errors.New("entropy unavailable") }} {
		if _, err := resolveCurrentAntonRewardPlan(catalog, config.Phases[0], "squad_item", 0, roll, now); err == nil {
			t.Fatal("invalid entropy accepted")
		}
	}
	for phase, p := range config.Phases {
		for rank := byte(0); rank <= 3; rank++ {
			for _, typ := range []string{"party_card", "pcroom_card", "squad_item"} {
				plan, err := resolveCurrentAntonRewardPlan(catalog, p, typ, rank, func(int64) (int64, error) { return 0, nil }, now)
				if err != nil || plan.Choice.Type != typ || len(plan.Rewards) == 0 {
					t.Fatal(phase, rank, typ, err)
				}
			}
		}
	}
	if _, err := resolveCurrentAntonRewardPlan(catalog, config.Phases[0], "gold", 0, func(int64) (int64, error) { return 0, nil }, now); err == nil {
		t.Fatal("missing gold pool invented")
	}
}
