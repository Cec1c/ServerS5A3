package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/experiencebuff"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestExperiencePotionResolverKeepsOrdinaryPotionFallback(t *testing.T) {
	service, _, session, _, _ := prepareCompletedSettlementRuntime(t)
	repos, _ := service.repositoryGroup()
	ctx := context.Background()
	if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{
		"0:65": {ItemID: 1066, Count: 2, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/aroma_elf.stk", "stackable_type": "[waste]"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketUseStackable), buildRejectedUseStackableBody(65, 0, 0x16f7, 1066)); err != nil {
		t.Fatal(err)
	}
	inv, _, err := repos.Inventory.Load(ctx, "99")
	if err != nil || inv.Slots["0:65"].Count != 1 {
		t.Fatalf("ordinary potion consumption=%+v err=%v", inv, err)
	}
}

func TestClearExperiencePotionCommitsAndProjectsBonus(t *testing.T) {
	for _, rate := range []int64{10000, 20000, 40000} {
		t.Run(string(rune('0'+rate/10000+1))+"x", func(t *testing.T) {
			service, runtime, session, _, _ := prepareCompletedSettlementRuntime(t)
			repos, _ := service.repositoryGroup()
			ctx := context.Background()
			c, _, _ := repos.Character.Load(ctx, "99")
			c.Level = 1
			c.Stats["exp"] = 0
			c.Stats["experience_potion_clear_basis_points"] = rate
			c.Stats["experience_potion_clear_until_ms"] = time.Now().Add(time.Hour).UnixMilli()
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := repos.Skill.Save(ctx, repo.SkillRecord{CharacterID: "99", Points: repo.SkillPointState{TotalSP: 10, RemainingSP: 10, SyncedLevel: 1}}); err != nil {
				t.Fatal(err)
			}
			runtime.Character = c
			runtime.Dungeon.Metadata.BasisLevel.Set = true
			runtime.Dungeon.Metadata.BasisLevel.Value = 1
			runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Set = true
			runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Value = 1
			runtime.Request.Difficulty = 0
			runtime.settlementPlayResultReceived = true
			runtime.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
			runtime.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset] = 73
			if err := service.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, runtime, currentDungeonSettlementTestResources(t)); err != nil {
				t.Fatal(err)
			}
			after, _, _ := repos.Character.Load(ctx, "99")
			bonus := uint32(130 * rate / 10000)
			if after.Stats["exp"] != int64(156+bonus) {
				t.Fatalf("committed exp=%d want %d", after.Stats["exp"], 156+bonus)
			}
			packet := runtime.settlementResultPlan.ClearRewardBody
			if got := binary.LittleEndian.Uint32(packet[17+currentDungeonClearRewardBonusClearAncientElixirIndex*4:]); got != bonus {
				t.Fatalf("elixir presentation=%d want=%d", got, bonus)
			}
			original := append([]byte(nil), packet...)
			after.Stats["experience_potion_clear_until_ms"] = time.Now().Add(-time.Hour).UnixMilli()
			if err := repos.Character.Save(ctx, after); err != nil {
				t.Fatal(err)
			}
			runtime.settlementResultPlan = nil
			if err := service.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, runtime, currentDungeonSettlementTestResources(t)); err != nil {
				t.Fatal(err)
			}
			after, _, _ = repos.Character.Load(ctx, "99")
			if after.Stats["exp"] != int64(156+bonus) || string(original) != string(runtime.settlementResultPlan.ClearRewardBody) {
				t.Fatal("replay changed experience or presentation")
			}
		})
	}
}

func TestRealPVFExperiencePotionFamilies(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	source, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   uint32
		kind string
		rate int64
	}{{7440, experiencebuff.Clear, 10000}, {10304122, experiencebuff.Quest, 1000}, {10304435, experiencebuff.Quest, 1000}, {490009951, experiencebuff.Clear, 40000}} {
		item, err := catalog.ResolveItem(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		text, err := source.ReadText(item.PVFPath)
		if err != nil {
			t.Fatal(err)
		}
		d, err := pvf.Parse(item.PVFPath, text)
		if err != nil {
			t.Fatal(err)
		}
		effect, err := experiencebuff.Parse(d)
		if err != nil {
			t.Fatal(err)
		}
		if effect.Kind != tc.kind || effect.BasisPoints != tc.rate || effect.DurationMillis <= 0 {
			t.Fatalf("item=%d effect=%+v", tc.id, effect)
		}
	}
}

// Scan the current archive rather than relying on translated item names.
func TestRealPVFAllExperiencePotionDefinitions(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	source, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := source.ReadText("stackable/stackable.lst")
	if err != nil {
		t.Fatal(err)
	}
	list, err := pvf.Parse("stackable/stackable.lst", raw)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, entry := range pvf.ParseList(list) {
		path := "stackable/" + strings.TrimPrefix(entry.Path, "stackable/")
		raw, err := source.ReadText(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(raw, "exp bonus rate") {
			continue
		}
		doc, err := pvf.Parse(path, raw)
		if err != nil {
			t.Fatal(err)
		}
		effect, err := experiencebuff.Parse(doc)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if effect.Kind == "" {
			continue
		}
		counts[effect.Kind]++
		kind, _ := doc.Text("stackable type")
		if kind != "[waste]" && kind != "[unlimited waste]" {
			t.Fatalf("%s type=%s bypasses potion owner", path, kind)
		}
		maintenance := doc.Ints("effect maintenance")
		if len(maintenance) != 4 || maintenance[0] != 1 || maintenance[1] != 1 || maintenance[2] != 1 || maintenance[3] != 0 {
			t.Fatalf("%s incompatible maintenance=%v", path, maintenance)
		}
	}
	if counts[experiencebuff.Clear] < 500 || counts[experiencebuff.Quest] < 2 {
		t.Fatalf("missing potion families: %v", counts)
	}
	t.Logf("real PVF experience definitions: %v", counts)
}
