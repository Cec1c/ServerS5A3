package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func despairSyntheticSource() dungeonDropCatalogTestSource {
	return dungeonDropCatalogTestSource{
		"monster/monster.lst":       "1 `test.mon`",
		"monster/test.mon":          "[name] `test`\n[level] 1 1",
		"stackable/stackable.lst":   "1252 `fixed.stk`\n3314 `medal.stk`",
		"stackable/fixed.stk":       "[stackable type] `[etc]`\n[stack limit] 1000",
		"stackable/medal.stk":       "[stackable type] `[material]`",
		"equipment/equipment.lst":   "1001 `weapon.equ`\n1002 `avatar/coat.equ`\n1003 `wronggrade.equ`",
		"equipment/weapon.equ":      "[equipment type] `[weapon]` 1\n[minimum level] 80\n[grade] 70\n[rarity] 0\n[creation rate] 1\n[durability] 40",
		"equipment/avatar/coat.equ": "[equipment type] `[coat avatar]` 1\n[minimum level] 1\n[grade] 70\n[rarity] 0\n[creation rate] 1",
		"equipment/wronggrade.equ":  "[equipment type] `[weapon]` 1\n[minimum level] 70\n[grade] 99\n[rarity] 0\n[creation rate] 10000",
		despairClearRewardPath:      "[drop item type prob]\n0 9500 0 500\n[basis of rarity dicision]\n1000000\n[item drop ref table]\n70 0 1\n",
	}
}

func TestDespairAvatarRewardKeepsResolvedGrantPeriod(t *testing.T) {
	s, rt, _, _, _ := prepareCompletedSettlementRuntime(t)
	source := despairSyntheticSource()
	source[despairClearRewardPath] = strings.ReplaceAll(source[despairClearRewardPath], "0 9500 0 500", "0 0 0 10000")
	source["equipment/avatar/coat.equ"] += "\n[usable period]\n2\n"
	monsters, err := newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.dungeonMonsterTable = monsters
	rt.Dungeon.Path = "dungeon/towers/ztowerofdespair001.dgn"
	rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of despair": 1}
	rt.Dungeon.Metadata.BasisLevel.Set = true
	rt.Dungeon.Metadata.BasisLevel.Value = 70
	before := s.gameplayNow().UTC()
	if err := s.freezeCurrentDespairRewardsLocked(rt); err != nil {
		t.Fatal(err)
	}
	if len(rt.settlementDespairRewards.Items) != 5 {
		t.Fatal("missing avatar draws")
	}
	for _, reward := range rt.settlementDespairRewards.Items {
		if reward.ListType != 1 || reward.Stack.Extra["item_kind"] != "avatar" || reward.Stack.ExpireAt.Before(before.Truncate(time.Second).Add(48*time.Hour)) || reward.Stack.ExpireAt.After(s.gameplayNow().UTC().Add(48*time.Hour+time.Second)) {
			t.Fatalf("avatar period=%v list=%d", reward.Stack.ExpireAt, reward.ListType)
		}
	}
}

func TestDespairRewardRefreshFailureRetriesProjectionWithoutGrant(t *testing.T) {
	s, rt, session, _, _ := prepareCompletedSettlementRuntime(t)
	rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of despair": 1}
	plan := currentDungeonSettlementPacketPlanForTest(t, rt, 0)
	plan.DespairResultBody, _ = buildCurrentDespairResultBody(12345, 1, []currentDespairResultItem{{ItemID: 1001, Quantity: 1}, {ItemID: 1002, Quantity: 1}})
	plan.DespairItemSlots = []alignedcmd.ItemSlotRefresh{{ListType: 0, SlotIndex: 9}, {ListType: 1, SlotIndex: 0}}
	rt.settlementResultPlan = &plan
	g, _ := s.repositoryGroup()
	before := map[string]dnfrepo.ItemStack{"0:9": {ItemID: 1001, Count: 1}, "1:0": {ItemID: 1002, Count: 1, Extra: map[string]string{"item_kind": "avatar", "amount_or_count": "0"}}}
	if err := g.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "99", Slots: before}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("despair inventory projection failed")
	conn := &failNthDungeonWriteConn{failAt: 4, err: want}
	session.conn = conn
	request := make([]byte, currentDungeonPlayResultBaseSize)
	if err := s.handleDungeonSetPlayResult(session, request); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	if rt.settlementDespairItemsSent || rt.settlementDespairResultSent || rt.settlementPhase >= currentDungeonSettlementPhaseResultShown {
		t.Fatal("failed refresh opened result gate")
	}
	if err := s.handleDungeonSetPlayResult(session, request); err != nil {
		t.Fatal(err)
	}
	rest := conn.write.Bytes()
	wantIDs := []uint16{34, 37, 35, 14, 14, 348}
	for _, id := range wantIDs {
		p, next := splitGameServerUpperPacket(t, rest)
		if p.Header.MsgID != id {
			t.Fatalf("msg=%d want=%d", p.Header.MsgID, id)
		}
		rest = next
	}
	if len(rest) != 0 || !rt.settlementDespairItemsSent || !rt.settlementDespairResultSent {
		t.Fatal("retry failed projection")
	}
	after, _, _ := g.Inventory.Load(context.Background(), "99")
	if !reflect.DeepEqual(before, after.Slots) {
		t.Fatal("projection mutated inventory")
	}
}

func TestDespairConfiguredRewardPoolUsesGradeAndAvatarType(t *testing.T) {
	for _, row := range []struct {
		weights string
		want    uint32
	}{{"0 10000 0 0", 1001}, {"0 0 0 10000", 1002}} {
		source := despairSyntheticSource()
		source[despairClearRewardPath] = strings.ReplaceAll(source[despairClearRewardPath], "0 9500 0 500", row.weights)
		catalog, err := newPVFDungeonDropCatalog(source)
		if err != nil {
			t.Fatal(err)
		}
		rules, err := loadCurrentDespairRewardRules(source)
		if err != nil {
			t.Fatal(err)
		}
		for _, floor := range []int{1, 9, 10, 20, 90, 99, 100} {
			ids, err := currentDespairRewardCandidateIDs(floor, 70, newCurrentDungeonDropLCG(123), catalog, rules)
			if err != nil {
				t.Fatal(err)
			}
			n := 5
			if floor >= 10 && floor <= 90 && floor%10 == 0 {
				n = 9
			}
			for i := 0; i < n; i++ {
				if ids[i] != row.want {
					t.Fatalf("floor=%d ids=%v", floor, ids)
				}
			}
			if n == 9 {
				if len(ids) != 10 || ids[9] != 1252 {
					t.Fatal(ids)
				}
			} else if floor == 100 {
				if len(ids) != 6 || ids[5] != 3314 {
					t.Fatal(ids)
				}
			} else if len(ids) != 5 {
				t.Fatal(ids)
			}
		}
	}
}

func TestDespairConfiguredRewardRejectsMalformedRules(t *testing.T) {
	for _, body := range []string{"", "[drop item type prob]\n1 9500 0 500", strings.ReplaceAll(despairSyntheticSource()[despairClearRewardPath], "1000000", "99"), strings.ReplaceAll(despairSyntheticSource()[despairClearRewardPath], "70 0 1", "70 -1 1")} {
		source := despairSyntheticSource()
		source[despairClearRewardPath] = body
		if _, err := loadCurrentDespairRewardRules(source); err == nil {
			t.Fatalf("malformed rules accepted: %s", body)
		}
	}
}

func TestRealPVFDespairRewardsCommitFloorItemsAndReplayReceipt(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("requires fixed published PVF")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	world, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := loadCurrentDespairRewardRules(a)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rules.types, []int64{0, 9500, 0, 500}) || !reflect.DeepEqual(rules.rarity, []int64{500000, 799900, 999900, 1000000, 1000001, 1000002, 1000003}) || rules.ranges[70].ValueA != 7 || rules.ranges[70].ValueB != 3 {
		t.Fatal("published clear-reward rules changed")
	}
	for _, floor := range []int{1, 10, 100} {
		t.Run(fmt.Sprintf("floor%d", floor), func(t *testing.T) {
			s, rt, _, _, _ := prepareCompletedSettlementRuntime(t)
			s.dungeonMonsterTable = monsters
			actual, found := world.FindDungeon(int64(11007 + floor))
			if !found {
				t.Fatal("real tower dungeon not found")
			}
			rt.Dungeon = actual
			if err := s.freezeCurrentDespairRewardsLocked(rt); err != nil {
				t.Fatal(err)
			}
			frozen := rt.settlementDespairRewards
			if len(frozen.Items) == 0 || len(frozen.Items) > 10 {
				t.Fatalf("reward count=%d", len(frozen.Items))
			}
			if err := s.freezeCurrentDespairRewardsLocked(rt); err != nil || frozen != rt.settlementDespairRewards {
				t.Fatal("reward rerolled")
			}
			if floor == 10 && frozen.Items[len(frozen.Items)-1].Stack.ItemID != 1252 {
				t.Fatal("mirror fixed item missing")
			}
			if floor == 100 && frozen.Items[len(frozen.Items)-1].Stack.ItemID != 3314 {
				t.Fatal("clear medal missing")
			}
			g := memory.NewMemoryGroup()
			ctx := context.Background()
			c := dnfrepo.CharacterRecord{CharacterID: "99", AccountID: "tower-test", Level: 1, Stats: map[string]int64{"exp": 90, dnfdungeon.DespairHighestClearedStatKey: int64(floor - 1)}}
			for _, err := range []error{g.Character.Save(ctx, c), g.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "99", Points: dnfrepo.SkillPointState{SyncedLevel: 1, TotalSP: 10, RemainingSP: 10}}), g.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99"})} {
				if err != nil {
					t.Fatal(err)
				}
			}
			owner, _ := dnfdungeon.NewOwner(g)
			cmd := dnfdungeon.SettlementCommand{AccountID: c.AccountID, CharacterID: c.CharacterID, CompletionKey: fmt.Sprintf("isolated-real-rewards-floor-%d", floor), Tables: currentDungeonSettlementTestResources(t).Progression, Experience: 20, DespairClearedFloor: floor, DespairRewards: frozen}
			result, err := owner.CommitSettlement(ctx, cmd)
			if err != nil {
				t.Fatal(err)
			}
			if result.Character.Stats[dnfdungeon.DespairHighestClearedStatKey] != int64(floor) || len(result.DespairItems) != len(frozen.Items) {
				t.Fatal("partial settlement")
			}
			items := make([]currentDespairResultItem, 0, len(result.DespairItems))
			for i, item := range result.DespairItems {
				stack := result.Inventory.Slots[fmt.Sprintf("%d:%d", item.ListType, item.Slot)]
				if stack.ItemID != int64(item.ItemID) || frozen.Items[i].Stack.ItemID != stack.ItemID {
					t.Fatal("displayed item differs from saved reward")
				}
				if item.ListType == 1 && stack.Extra["item_kind"] != "avatar" {
					t.Fatal("avatar identity lost")
				}
				items = append(items, currentDespairResultItem{ItemID: item.ItemID, Quantity: item.Quantity})
			}
			body, err := buildCurrentDespairResultBody(12345, uint16(floor), items)
			if err != nil || len(body) != 7+8*len(items) || binary.LittleEndian.Uint16(body[4:6]) != uint16(floor) {
				t.Fatal("wrong committed result projection")
			}
			replay, err := owner.CommitSettlement(ctx, cmd)
			if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Inventory.Slots, result.Inventory.Slots) || !reflect.DeepEqual(replay.DespairItems, result.DespairItems) {
				t.Fatalf("receipt replay err=%v", err)
			}
			t.Logf("floor=%d saved reward rows=%d items=%v", floor, len(items), items)
		})
	}
}
