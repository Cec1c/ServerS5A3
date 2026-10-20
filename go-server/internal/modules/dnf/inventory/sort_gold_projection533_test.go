package inventory

import (
	"context"
	"encoding/binary"
	"math"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// Gold belongs to Character.Stats in the Go repository; native login projects
// it into list0/slot0 without requiring a persisted inventory wallet row.
func TestSort533RefreshProjectsAuthoritativeWallet(t *testing.T) {
	for _, category := range []byte{3, 11} {
		for _, wallet := range []string{"missing", "stale", "current"} {
			t.Run(wallet+string(rune('A'+category)), func(t *testing.T) {
				ctx := context.Background()
				repos := dnfrepomemory.NewMemoryGroup()
				saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 4321}})
				slots := map[string]dnfrepo.ItemStack{slotKey(0, 125): {ItemID: 3277, Count: 2, Extra: map[string]string{"stackable_type": "[material]"}}}
				if wallet != "missing" {
					count := int64(4321)
					if wallet == "stale" {
						count = 9
					}
					slots[slotKey(0, 0)] = dnfrepo.ItemStack{ItemID: 0, Count: count}
				}
				saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots})
				result, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: 20, Body: []byte{0, category}, AccountID: "acc", SelectedCharacterID: 77, Repositories: repos})
				if err != nil || !result.ResponseAllowed || len(result.UpperResponses) != 2 {
					t.Fatalf("sort failed: err=%v result=%+v", err, result)
				}
				body := result.UpperResponses[1].Body
				if len(body) < 5 {
					t.Fatal("short main-list refresh")
				}
				found := false
				for offset := 5; offset+currentItemListEntrySize <= len(body); offset += currentItemListEntrySize {
					row := body[offset : offset+currentItemListEntrySize]
					if binary.LittleEndian.Uint16(row[:2]) == 0 {
						found = true
						if binary.LittleEndian.Uint32(row[2:6]) != 0 || binary.LittleEndian.Uint32(row[6:10]) != 4321 {
							t.Errorf("wallet row is stale: item=%d count=%d", binary.LittleEndian.Uint32(row[2:6]), binary.LittleEndian.Uint32(row[6:10]))
						}
					}
				}
				if !found {
					t.Error("full main-list refresh omitted authoritative gold wallet")
				}
				character, _, err := repos.Character.Load(ctx, "77")
				if err != nil || character.Stats["gold"] != 4321 {
					t.Fatalf("persisted gold changed: %v", err)
				}
				stored := loadTestInventory(t, ctx, repos, "77")
				if wallet == "missing" {
					if _, ok := stored.Slots[slotKey(0, 0)]; ok {
						t.Error("projection persisted a fabricated wallet row")
					}
				}
			})
		}
	}
}

func TestSort533ProjectionBoundariesAndNoMutation(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		list  byte
		gold  int64
		known bool
		item  int64
		want  int64
	}{
		{"negative", 0, -1, true, 0, 0},
		{"maximum", 0, math.MaxInt64, true, 0, math.MaxInt32},
		{"conflicting_item", 0, 4321, true, 3277, 9},
		{"unknown_gold", 0, 0, false, 0, 9},
		{"personal_cargo", 2, 4321, true, 0, 9},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			stats := map[string]int64{}
			if scenario.known {
				stats["gold"] = scenario.gold
			}
			saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: stats})
			key := slotKey(scenario.list, 0)
			input := map[string]dnfrepo.ItemStack{key: {ItemID: scenario.item, Count: 9, Extra: map[string]string{"amount_or_count": "9"}}}
			before := cloneItemMap(input)
			got, err := projectSortRefreshGold(ctx, repos.Character, SortResult{CharacterID: "77", ListType: scenario.list, Refresh: input})
			if err != nil {
				t.Fatal(err)
			}
			if got.Refresh[key].Count != scenario.want {
				t.Fatalf("count=%d want=%d", got.Refresh[key].Count, scenario.want)
			}
			if !reflect.DeepEqual(input, before) {
				t.Fatal("projection mutated the input snapshot")
			}
			if scenario.list == 0 && scenario.known && scenario.item == 0 {
				body, ok := buildSortRefreshBody(ctx, repos.Settings, got)
				if !ok || binary.LittleEndian.Uint32(body[11:15]) != uint32(scenario.want) {
					t.Fatal("stale extra count overrode authoritative wallet")
				}
			}
		})
	}
}
