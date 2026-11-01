package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/inventory"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	archive "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestTownPotion002RealPVF120(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	input := os.Getenv("TOWN_POTION002_ITEMS")
	if p == "" || input == "" {
		t.Skip("requires paired PVF and 120-ID census")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(b)) != "95147859374186dbeb99f998829a1c642f949e56942ac98be8b5af4492aefe1c" {
		t.Fatal("wrong archive")
	}
	b = nil
	a, err := archive.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID      uint32   `json:"itemId"`
		Path    string   `json:"pvfPath"`
		RawHash string   `json:"rawSHA256"`
		Tags    []string `json:"tags"`
	}
	if err = json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 120 {
		t.Fatalf("count %d", len(rows))
	}
	results := []map[string]any{}
	seen := map[uint32]bool{}
	for _, row := range rows {
		t.Run(fmt.Sprint(row.ID), func(t *testing.T) {
			if seen[row.ID] {
				t.Fatal("duplicate")
			}
			seen[row.ID] = true
			raw, e := a.ReadRaw(row.Path)
			if e != nil {
				t.Fatal(e)
			}
			hash := fmt.Sprintf("%x", sha256.Sum256(raw))
			if hash != row.RawHash {
				t.Fatal("raw changed")
			}
			d, e := c.ResolveItem(row.ID)
			if e != nil {
				t.Fatal(e)
			}
			if currentUpgradeRateBuffStackableToken(d.StackableType) != "[expert town potion]" {
				t.Fatal("wrong type")
			}
			resolution, e := resolveCurrentStackableUseProvenance(c, int64(row.ID))
			if e != nil {
				t.Fatal(e)
			}
			g := memory.NewMemoryGroup()
			ctx := context.Background()
			now := time.Now().UTC()
			ch := repo.CharacterRecord{CharacterID: "77", AccountID: "town-audit", Level: 90, Stats: map[string]int64{"gold": 200}}
			iv := repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:87": {ItemID: int64(row.ID), Count: 2, Extra: map[string]string{"item_kind": "stackable", "pvf_path": row.Path}}}}
			if e = g.Character.Save(ctx, ch); e != nil {
				t.Fatal(e)
			}
			if e = g.Inventory.Save(ctx, iv); e != nil {
				t.Fatal(e)
			}
			before, _, _ := g.Inventory.Load(ctx, "77")
			bc, _, _ := g.Character.Load(ctx, "77")
			body := make([]byte, 15)
			binary.LittleEndian.PutUint16(body, 87)
			binary.LittleEndian.PutUint32(body[3:], row.ID)
			binary.LittleEndian.PutUint32(body[7:], row.ID)
			got, e := inventory.NewHandler().Handle(ctx, alignedcmd.Request{Opcode: 44, Body: body, AccountID: "town-audit", SelectedCharacterID: 77, Repositories: g, ItemUsageNow: now,
				StackableUseProvenanceResolver: func(id int64) (alignedcmd.StackableUseProvenanceResolution, error) {
					return resolveCurrentStackableUseProvenance(c, id)
				}})
			if e != nil {
				t.Fatal(e)
			}
			after, _, _ := g.Inventory.Load(ctx, "77")
			ac, _, _ := g.Character.Load(ctx, "77")
			supported := row.ID == 10007343
			if supported && (len(got.UpperResponses) == 0 || len(got.UpperResponses[0].Body) == 0 || got.UpperResponses[0].Body[0] != 1) {
				t.Errorf("ACK does not match effect result: %+v", got.UpperResponses)
			}
			if !supported && (!got.Handled || got.ResponseAllowed || len(got.UpperResponses) != 0) {
				t.Errorf("blocked owner must suppress success responses: %+v", got)
			}
			status := "rejected_without_debit"
			reason := "effect parameters/owner not proven; generic debit forbidden"
			for _, tag := range row.Tags {
				switch tag {
				case "monstercard bind":
					reason = "card-binding mechanism belongs to existing monster-card owner, not op44"
				case "shop doll":
					reason = "profession shop lifecycle owner required, not potion debit"
				case "need material":
					reason = "material-dependent effect requires its own validated owner"
				}
			}
			if d.ActionType != "" {
				reason = "independent action requires effect owner: " + d.ActionType
			}
			if row.ID == 2683466 {
				reason = "native-protocol-audit: op515/action107 observed; current fatigue owner accepts action58 only; separate repair required"
			}
			if supported {
				status = "supported_atomic_reinforce"
				reason = "native effect 99; 10% reinforce/30s/60s; existing op44 ACK"
				if resolution.TownPotion002 == nil || after.Slots["0:87"].Count != 1 || ac.Stats["upgrade_rate_buff_reinforce_bonus"] != 10000 || ac.Stats["upgrade_rate_buff_reinforce_until_unix"] != now.Unix()+30 {
					t.Errorf("crystal must debit and apply effect: count=%d stats=%v handler=%s", after.Slots["0:87"].Count, ac.Stats, got.Reason)
				}
				if len(got.UpperResponses) == 0 {
					t.Error("missing actual op44 ACK")
				}
			} else {
				if resolution.TownPotion002 != nil {
					t.Error("unsupported effect marked supported")
				}
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(bc, ac) {
					t.Error("unsupported item mutated assets")
				}
			}
			results = append(results, map[string]any{"itemId": row.ID, "pvfPath": row.Path, "rawSHA256": hash, "status": status, "reason": reason, "handlerReason": got.Reason, "passed": !t.Failed(), "gameAcceptance": "not_run"})
		})
	}
	if output := os.Getenv("TOWN_POTION002_OUT"); output != "" {
		data, e := json.MarshalIndent(results, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(output, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
}

func TestTownPotion002PVFRejectsNewUnownedFields(t *testing.T) {
	path := "stackable/cash/chn_20141111_sao_package/crystallight_10007343.stk"
	base := "[name] `Crystal`\n[stackable type] `[expert town potion]` 0\n[minimum level] 1\n[item group name] `potion`\n[usable job] `[all]`\n[/usable job]\n[cool time] 60000\n[stat change duration] 30000 `myself`\n"
	for _, extra := range []string{"", "[monstercard bind] 2", "[total usable count] 1", "[action usable place] `[village]`", "[usable event] 1", "[need material] 3033 2", "[cool time] 1", "[action type] `[add fatigue]` 10"} {
		s := dungeonDropCatalogTestSource{"monster/monster.lst": "", "equipment/equipment.lst": "", "stackable/stackable.lst": "10007343 `cash/chn_20141111_sao_package/crystallight_10007343.stk`", path: base + extra}
		c, e := newPVFDungeonDropCatalog(s)
		if e != nil {
			t.Fatal(e)
		}
		d, e := resolveTownPotion002(c, 10007343)
		if extra == "" {
			if e != nil || d == nil {
				t.Fatalf("base: %v", e)
			}
		} else if e == nil {
			t.Fatalf("unhandled field accepted: %s", extra)
		}
	}
}
