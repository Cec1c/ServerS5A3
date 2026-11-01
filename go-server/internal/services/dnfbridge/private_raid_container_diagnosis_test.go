package dnfbridge

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

// Diagnose the deployed PVF with the real reward owner, using only memory stores.
// It never accesses a production account, database or network service.
func TestPrivateRaidContainerDiagnosis(t *testing.T) {
	out := os.Getenv("DNFBRIDGE_RAID_CONTAINER_OUT")
	if out == "" {
		t.Skip("explicit diagnosis output required")
	}
	catalog, anton := actualAntonRewardFixture(t)
	luke, err := raid.LoadLukeRewardConfiguration(catalog.source)
	if err != nil {
		t.Fatal(err)
	}
	fiend, err := raid.LoadFiendWarRewardConfiguration(catalog.source)
	if err != nil {
		t.Fatal(err)
	}
	type received struct {
		ID    uint32 `json:"item_id"`
		Name  string `json:"name"`
		Count uint32 `json:"count"`
	}
	type result struct {
		Raid      string     `json:"raid"`
		Phase     int        `json:"phase"`
		Pool      string     `json:"pool"`
		Source    uint32     `json:"source_id"`
		Type      string     `json:"source_type"`
		Weight    uint32     `json:"weight"`
		DirectBox bool       `json:"unopened_box_persisted"`
		Items     []received `json:"actual_items"`
	}
	results := []result{}
	for mode, c := range []struct {
		name   string
		config raid.AntonRewardConfiguration
	}{{"anton", anton}, {"luke", luke}, {"fiendwar", fiend}} {
		for phase, p := range c.config.Phases {
			for _, choice := range p.Choices {
				if choice.Type != "party_card" && choice.Type != "squad_item" {
					continue
				}
				now := time.Now().UTC()
				plan, e := resolveCurrentAntonConfiguredReward(catalog, choice, func(int64) (int64, error) { return 0, nil }, now)
				if e != nil {
					t.Fatal(e)
				}
				def, e := catalog.ResolveItem(choice.ContainerID)
				if e != nil {
					t.Fatal(e)
				}
				g := memory.NewMemoryGroup()
				ctx := context.Background()
				if e = g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "diagnostic-fixture"}); e != nil {
					t.Fatal(e)
				}
				// Match the existing raid owner fixture: an initialized inventory
				// with a non-reward entry survives the store's empty-map cloning.
				if e = g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{"0:50": {ItemID: 10096296, Count: 2}}}); e != nil {
					t.Fatal(e)
				}
				kind := byte(1)
				if choice.Type == "squad_item" {
					kind = 3
				}
				cmd := raid.RewardCommand{AccountID: "diagnostic-fixture", CharacterID: "31", InstanceID: "00112233445566778899aabbccddeeff", Mode: byte(mode + 1), Phase: byte(phase), Kind: kind, Card: 0, MinimumClears: p.MinimumClears, OwnedClears: 6, Fingerprint: plan.Fingerprint, CompletedAt: now}
				if _, e = grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd); e != nil {
					t.Fatal(e)
				}
				inv, _, e := g.Inventory.Load(ctx, "31")
				if e != nil {
					t.Fatal(e)
				}
				if inv.Slots["0:50"].Count != 2 {
					t.Fatal("raid grant changed the preexisting fixture item")
				}
				account, _, e := g.AccountInventory.Load(ctx, "diagnostic-fixture")
				if e != nil {
					t.Fatal(e)
				}
				r := result{Raid: c.name, Phase: phase, Pool: choice.Type, Source: choice.ContainerID, Type: def.StackableType, Weight: choice.Weight}
				for _, item := range plan.Rewards {
					var found int64
					for _, slots := range []map[string]repo.ItemStack{inv.Slots, account.Slots} {
						for _, stack := range slots {
							if stack.ItemID == int64(item.Definition.ItemID) {
								found += stack.Count
							}
						}
					}
					if found != int64(item.Count) {
						t.Fatalf("source %d persist mismatch: %d", choice.ContainerID, found)
					}
					r.Items = append(r.Items, received{item.Definition.ItemID, item.Definition.Name, item.Count})
					r.DirectBox = r.DirectBox || item.Definition.ItemID == choice.ContainerID
				}
				if r.DirectBox {
					t.Fatalf("unexpected source treatment %+v", r)
				}
				privateRaidAssertContents(t, plan.Rewards, privateRaidExpectedZero(choice.ContainerID, 1))
				results = append(results, r)
				t.Logf("%s phase=%d pool=%s source=%d type=%s actual=%+v unopened=%t", r.Raid, r.Phase, r.Pool, r.Source, r.Type, r.Items, r.DirectBox)
			}
		}
	}
	raw, err := json.MarshalIndent(map[string]any{"passed": true, "roll_policy": "zero draw to inspect each configured branch; not a probability simulation", "database": "memory only", "results": results}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(out, raw, 0644); err != nil {
		t.Fatal(err)
	}
}
