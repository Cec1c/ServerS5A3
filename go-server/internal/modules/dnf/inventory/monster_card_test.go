package inventory

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	placement "longheng.io/server/internal/modules/dnf/itemplacement"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type cardFailSaveRepository struct{ record repo.InventoryRecord }

func (r *cardFailSaveRepository) Load(context.Context, string) (repo.InventoryRecord, bool, error) {
	return repo.CloneInventory(r.record), true, nil
}
func (r *cardFailSaveRepository) Save(context.Context, repo.InventoryRecord) error {
	return errors.New("测试保存失败")
}
func TestMonsterCardSaveFailureLeavesOriginalAssets(t *testing.T) {
	for _, compound := range []bool{false, true} {
		original := repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:233": cardTestStack(100, 13, 0), "0:68": {ItemID: 900, Count: 1}}}
		stub := &cardFailSaveRepository{record: repo.CloneInventory(original)}
		o, e := NewOwner(repo.Group{Inventory: stub, CharacterItems: recordingMoveUnitOfWork{inventory: stub}})
		if e != nil {
			t.Fatal(e)
		}
		cmd := MonsterCardCommand{CharacterID: 77, TargetSlot: 233, MaterialSlot: 233, MaterialCount: 1, BinderSlot: 68, InputSlots: []int16{233, 233}, Random: func(int) int { return 0 }}
		if compound {
			_, e = o.CompoundMonsterCards(context.Background(), cmd, cardTestResolve, cardTestRules())
		} else {
			_, e = o.UpgradeMonsterCard(context.Background(), cmd, cardTestResolve, cardTestRules())
		}
		if e == nil || !reflect.DeepEqual(original, stub.record) {
			t.Fatal("保存失败未保留资产", e, stub.record)
		}
	}
}

func cardTestStack(id, count int64, stage byte) repo.ItemStack {
	s := repo.ItemStack{ItemID: id, Count: count, RawEntry: make([]byte, 119), Extra: map[string]string{"item_kind": "stackable", "pvf_path": fmt.Sprintf("stackable/monstercard/%d.stk", id), "stackable_type": "[material expert job]", "stack_limit": "0"}}
	repo.SetMonsterCardUpgradeCount(&s, stage)
	updateStackRawAmount(&s)
	return s
}
func cardTestRules() MonsterCardRules {
	return MonsterCardRules{UpgradePercent: map[int]int{0: 100, 1: 80, 2: 60, 3: 40, 6: 40}, MaximumInputs: map[int]int{0: 5, 1: 4, 2: 3, 3: 2}, Pools: map[int][]MonsterCardWeightedReward{0: {{100, 1}}, 1: {{110, 1}}, 2: {{120, 1}}, 3: {{130, 1}}}}
}
func cardTestResolve(id int64) (MonsterCardDefinition, error) {
	if id == 900 {
		return MonsterCardDefinition{ItemID: id, PVFPath: "binder.stk", IsBinder: true, Policy: placement.SlotPolicy{ListType: 0, Start: 65, End: 120}}, nil
	}
	if (id < 100 || id > 139) && (id < 160 || id > 169) {
		return MonsterCardDefinition{}, fmt.Errorf("无此卡片")
	}
	return MonsterCardDefinition{ItemID: id, PVFPath: fmt.Sprintf("stackable/monstercard/%d.stk", id), IsCard: true, Rarity: int((id - 100) / 10), Stages: []byte{0, 1, 2}, Policy: placement.SlotPolicy{ListType: 0, Start: 233, End: 288}}, nil
}
func cardTestOwner(t *testing.T, slots map[string]repo.ItemStack) (*Owner, repo.Group) {
	t.Helper()
	r := memory.NewMemoryGroup()
	ctx := context.Background()
	saveTestCharacter(t, ctx, r, repo.CharacterRecord{CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 0}})
	saveTestInventory(t, ctx, r, repo.InventoryRecord{CharacterID: "77", Slots: slots})
	o, e := NewOwner(r)
	if e != nil {
		t.Fatal(e)
	}
	return o, r
}
func TestMonsterCardRequestCurrentNativeVectors(t *testing.T) {
	c, e := DecodeMonsterCardUpgrade([]byte{0, 241, 0, 1, 0, 241, 0})
	if e != nil || c.TargetSlot != 241 || c.MaterialSlot != 241 || c.MaterialCount != 1 {
		t.Fatal(c, e)
	}
	for n := 2; n <= 5; n++ {
		b := make([]byte, 3+2*n)
		b[0] = 68
		b[2] = byte(n)
		for i := 0; i < n; i++ {
			binary.LittleEndian.PutUint16(b[3+i*2:], 242)
		}
		c, e := DecodeMonsterCardCompound(b)
		if e != nil || c.BinderSlot != 68 || len(c.InputSlots) != n {
			t.Fatal(c, e)
		}
		for _, slot := range c.InputSlots {
			if slot != 242 {
				t.Fatal(c)
			}
		}
	}
	for _, b := range [][]byte{nil, {0, 68, 0}, {0, 68, 0, 1, 242, 0}, {0, 68, 0, 6}, {0, 68, 0, 2, 242, 0}, {0, 68, 0, 2, 242, 0, 242, 0, 0}} {
		if _, e := DecodeMonsterCardCompound(b); e == nil {
			t.Fatal("应拒绝畸形包", b)
		}
	}
}
func TestMonsterCardUpgradeSameStackSplitsAndCostsNoGold(t *testing.T) {
	o, r := cardTestOwner(t, map[string]repo.ItemStack{"0:241": cardTestStack(100, 13, 0)})
	out, e := o.UpgradeMonsterCard(context.Background(), MonsterCardCommand{CharacterID: 77, TargetSlot: 241, MaterialSlot: 241, MaterialCount: 1, Random: func(int) int { t.Fatal("白卡PVF概率为100%，不应抽随机"); return 0 }}, cardTestResolve, cardTestRules())
	if e != nil || !out.Applied || !out.Upgraded || out.ResultSlot == 241 || out.Level != 1 {
		t.Fatal(out, e)
	}
	inv := loadTestInventory(t, context.Background(), r, "77")
	original := inv.Slots["0:241"]
	newCard := inv.Slots[slotKey(0, out.ResultSlot)]
	if original.Count != 11 || newCard.Count != 1 || original.RawEntry[18] != 0 || newCard.RawEntry[18] != 1 || original.Extra["enchant_upgrade_count"] != "0" {
		t.Fatal(inv)
	}
	gold, _, e := r.Character.Load(context.Background(), "77")
	if e != nil || gold.Stats["gold"] != 0 {
		t.Fatal(gold, e)
	}
}

func TestMonsterCardSameIDUpgradeUsesRarityProbabilityIncludingLegend(t *testing.T) {
	for _, rarity := range []int{1, 2, 3, 6} {
		percent := cardTestRules().UpgradePercent[rarity]
		for _, sameSlot := range []bool{false, true} {
			for _, roll := range []int{percent - 1, percent} {
				t.Run(fmt.Sprintf("品质%d同槽%t掷%d", rarity, sameSlot, roll), func(t *testing.T) {
					id := int64(100 + rarity*10)
					materialSlot := int16(234)
					slots := map[string]repo.ItemStack{"0:233": cardTestStack(id, 13, 0)}
					if sameSlot {
						materialSlot = 233
					} else {
						slots["0:234"] = cardTestStack(id, 13, 0)
					}
					o, r := cardTestOwner(t, slots)
					calls := 0
					out, e := o.UpgradeMonsterCard(context.Background(), MonsterCardCommand{CharacterID: 77, TargetSlot: 233, MaterialSlot: materialSlot, MaterialCount: 1, Random: func(n int) int {
						calls++
						if n != 100 {
							t.Fatal(n)
						}
						return roll
					}}, cardTestResolve, cardTestRules())
					success := roll < percent
					if e != nil || !out.Applied || out.Upgraded != success || out.ChancePercent != percent || calls != 1 {
						t.Fatal(out, e, calls)
					}
					inv := loadTestInventory(t, context.Background(), r, "77")
					total := int64(0)
					upgraded := int64(0)
					for _, stack := range inv.Slots {
						total += stack.Count
						level, ok := repo.MonsterCardUpgradeCount(stack)
						if !ok {
							t.Fatal(stack)
						}
						if level == 1 {
							upgraded += stack.Count
						}
					}
					want := int64(25)
					if sameSlot {
						want = 12
					}
					if total != want || upgraded != map[bool]int64{true: 1, false: 0}[success] {
						t.Fatal(inv, total, upgraded)
					}
					if success && out.ResultSlot == 233 {
						t.Fatal("原堆仍有卡时升级卡必须分格")
					}
				})
			}
		}
	}
}

func TestMonsterCardSameIDCannotBypassMissingLegendProbability(t *testing.T) {
	o, r := cardTestOwner(t, map[string]repo.ItemStack{"0:233": cardTestStack(160, 13, 0)})
	rules := cardTestRules()
	delete(rules.UpgradePercent, 6)
	before := loadTestInventory(t, context.Background(), r, "77")
	out, e := o.UpgradeMonsterCard(context.Background(), MonsterCardCommand{CharacterID: 77, TargetSlot: 233, MaterialSlot: 233, MaterialCount: 1}, cardTestResolve, rules)
	if e != nil || out.Applied || !reflect.DeepEqual(before, loadTestInventory(t, context.Background(), r, "77")) {
		t.Fatal(out, e)
	}
}

func TestMonsterCardLegendCompoundRemainsUnsupportedWithoutConsumption(t *testing.T) {
	o, r := cardTestOwner(t, map[string]repo.ItemStack{"0:68": {ItemID: 900, Count: 1}, "0:233": cardTestStack(160, 13, 0)})
	before := loadTestInventory(t, context.Background(), r, "77")
	out, e := o.CompoundMonsterCards(context.Background(), MonsterCardCommand{CharacterID: 77, BinderSlot: 68, InputSlots: []int16{233, 233}, Random: func(int) int { t.Fatal("传说合成不应抽奖"); return 0 }}, cardTestResolve, cardTestRules())
	if e != nil || out.Applied || !reflect.DeepEqual(before, loadTestInventory(t, context.Background(), r, "77")) {
		t.Fatal(out, e)
	}
}
func TestMonsterCardUpgradeProbabilityBoundaryAndRejections(t *testing.T) {
	for _, roll := range []int{59, 60} {
		o, r := cardTestOwner(t, map[string]repo.ItemStack{"0:233": cardTestStack(120, 2, 0), "0:234": cardTestStack(121, 3, 0)})
		out, e := o.UpgradeMonsterCard(context.Background(), MonsterCardCommand{CharacterID: 77, TargetSlot: 233, MaterialSlot: 234, MaterialCount: 1, Random: func(int) int { return roll }}, cardTestResolve, cardTestRules())
		if e != nil || !out.Applied || out.Upgraded != (roll < 60) || out.Level != byte(map[bool]int{true: 1, false: 0}[roll < 60]) {
			t.Fatal(out, e)
		}
		inv := loadTestInventory(t, context.Background(), r, "77")
		if inv.Slots["0:234"].Count != 2 {
			t.Fatal(inv)
		}
		expected := int64(2)
		if roll < 60 {
			expected = 1
		}
		if inv.Slots["0:233"].Count != expected {
			t.Fatal(inv)
		}
	}
	tests := []struct {
		name             string
		target, material repo.ItemStack
		slot             int16
		count            uint16
	}{
		{"满级", cardTestStack(100, 1, 2), cardTestStack(100, 1, 0), 233, 1},
		{"品质不同", cardTestStack(100, 1, 0), cardTestStack(110, 1, 0), 233, 1},
		{"材料数量参数", cardTestStack(100, 1, 0), cardTestStack(100, 1, 0), 233, 2},
		{"非法级数", cardTestStack(100, 1, 7), cardTestStack(100, 1, 0), 233, 1},
		{"关闭格", cardTestStack(100, 1, 0), cardTestStack(100, 1, 0), 270, 1},
	}
	expired := cardTestStack(100, 1, 0)
	expired.ExpireAt = time.Now().Add(-time.Hour)
	tests = append(tests, struct {
		name             string
		target, material repo.ItemStack
		slot             int16
		count            uint16
	}{"过期", expired, cardTestStack(100, 1, 0), 233, 1})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slots := map[string]repo.ItemStack{slotKey(0, tt.slot): tt.target, "0:234": tt.material}
			o, r := cardTestOwner(t, slots)
			before := loadTestInventory(t, context.Background(), r, "77")
			out, e := o.UpgradeMonsterCard(context.Background(), MonsterCardCommand{CharacterID: 77, TargetSlot: tt.slot, MaterialSlot: 234, MaterialCount: tt.count}, cardTestResolve, cardTestRules())
			if e != nil || out.Applied {
				t.Fatal(out, e)
			}
			after := loadTestInventory(t, context.Background(), r, "77")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("拒绝后资产改变")
			}
		})
	}
}
func TestMonsterCardUpgradeFullBagDoesNotConsumeOrRoll(t *testing.T) {
	slots := map[string]repo.ItemStack{}
	for s := int16(233); s <= 264; s++ {
		slots[slotKey(0, s)] = cardTestStack(100, 2, 0)
	}
	o, r := cardTestOwner(t, slots)
	before := loadTestInventory(t, context.Background(), r, "77")
	out, e := o.UpgradeMonsterCard(context.Background(), MonsterCardCommand{CharacterID: 77, TargetSlot: 233, MaterialSlot: 234, MaterialCount: 1, Random: func(int) int { t.Fatal("放不下不应抽奖"); return 0 }}, cardTestResolve, cardTestRules())
	if e != nil || out.Applied || out.ErrorCode != 4 {
		t.Fatal(out, e)
	}
	if !reflect.DeepEqual(before, loadTestInventory(t, context.Background(), r, "77")) {
		t.Fatal("满包扣材")
	}
	// 材料耗尽的格可承接结果；不能误判为仍无空间。
	slots["0:234"] = cardTestStack(100, 1, 0)
	o, _ = cardTestOwner(t, slots)
	out, e = o.UpgradeMonsterCard(context.Background(), MonsterCardCommand{CharacterID: 77, TargetSlot: 233, MaterialSlot: 234, MaterialCount: 1}, cardTestResolve, cardTestRules())
	if e != nil || !out.Applied || out.ResultSlot != 234 {
		t.Fatal(out, e)
	}
}
func TestMonsterCardCompoundAllCustomRatesAndActualConsumption(t *testing.T) {
	for rarity, max := range []int{5, 4, 3, 2} {
		for n := 2; n <= max; n++ {
			p, ok := MonsterCardCompoundPercent(rarity, n)
			if !ok {
				t.Fatal(rarity, n)
			}
			for _, roll := range []int{0, 99} {
				t.Run(fmt.Sprintf("品质%d张数%d掷%d", rarity, n, roll), func(t *testing.T) {
					id := int64(100 + rarity*10)
					slots := make([]int16, n)
					for i := range slots {
						slots[i] = 233
					}
					o, r := cardTestOwner(t, map[string]repo.ItemStack{"0:68": {ItemID: 900, Count: 1}, "0:233": cardTestStack(id, 13, 0)})
					out, e := o.CompoundMonsterCards(context.Background(), MonsterCardCommand{CharacterID: 77, BinderSlot: 68, InputSlots: slots, Random: func(bound int) int {
						if bound == 100 {
							return roll
						}
						return 0
					}}, cardTestResolve, cardTestRules())
					promoted := p > 0 && roll < p
					want := id
					if promoted {
						want += 10
					}
					if e != nil || !out.Applied || out.Upgraded != promoted || out.ItemID != want {
						t.Fatal(out, e)
					}
					inv := loadTestInventory(t, context.Background(), r, "77")
					if _, ok := inv.Slots["0:68"]; ok {
						t.Fatal("合成器未扣除")
					}
					total := int64(0)
					for _, s := range inv.Slots {
						total += s.Count
					}
					if total != int64(14-n) {
						t.Fatal("不是N张换一张", total)
					}
					gold, _, e := r.Character.Load(context.Background(), "77")
					if e != nil || gold.Stats["gold"] != 0 {
						t.Fatal(gold, e)
					}
				})
			}
		}
	}
}
func TestMonsterCardCompoundRejectsWithoutLoss(t *testing.T) {
	for _, inputs := range [][]int16{{233, 233, 233}, {233, 234}, {233, 68}, {233, 270}, {233, 233, 233, 233, 233}} {
		o, r := cardTestOwner(t, map[string]repo.ItemStack{"0:68": {ItemID: 900, Count: 1}, "0:233": cardTestStack(120, 2, 0), "0:234": cardTestStack(110, 1, 0), "0:270": cardTestStack(120, 1, 0)})
		before := loadTestInventory(t, context.Background(), r, "77")
		out, e := o.CompoundMonsterCards(context.Background(), MonsterCardCommand{CharacterID: 77, BinderSlot: 68, InputSlots: inputs}, cardTestResolve, cardTestRules())
		if e != nil || out.Applied {
			t.Fatal(out, e)
		}
		if !reflect.DeepEqual(before, loadTestInventory(t, context.Background(), r, "77")) {
			t.Fatal("拒绝扣材")
		}
	}
	slots := map[string]repo.ItemStack{"0:68": {ItemID: 900, Count: 1}}
	for s := int16(233); s <= 264; s++ {
		slots[slotKey(0, s)] = cardTestStack(100, 13, 0)
	}
	o, r := cardTestOwner(t, slots)
	before := loadTestInventory(t, context.Background(), r, "77")
	out, e := o.CompoundMonsterCards(context.Background(), MonsterCardCommand{CharacterID: 77, BinderSlot: 68, InputSlots: []int16{233, 233}, Random: func(int) int { return 0 }}, cardTestResolve, cardTestRules())
	if e != nil || out.Applied || out.ErrorCode != 4 {
		t.Fatal(out, e)
	}
	if !reflect.DeepEqual(before, loadTestInventory(t, context.Background(), r, "77")) {
		t.Fatal("满包合成扣材")
	}
}
func TestMonsterCardMoveAndSortPreserveDifferentGrades(t *testing.T) {
	o, r := cardTestOwner(t, map[string]repo.ItemStack{"0:233": cardTestStack(100, 3, 0), "0:234": cardTestStack(100, 2, 1), "0:235": cardTestStack(100, 4, 0)})
	if canStack(cardTestStack(100, 3, 0), cardTestStack(100, 2, 1), 1) {
		t.Fatal("手工搬运仍可合并不同级")
	}
	// 上游允许两堆异级卡整堆交换；卡片身份修复只阻止混级合并。
	moved, e := o.Move(context.Background(), Command{Operation: "move_itemspace", AccountID: "acc", SelectedCharacterID: 77, SourceListType: 0, SourceSlotIndex: 233, DestinationListType: 0, DestinationSlotIndex: 234, MoveCount: 3})
	if e != nil || !moved.Changed || moved.Mode != "swap" {
		t.Fatal(moved, e)
	}
	swapped := loadTestInventory(t, context.Background(), r, "77")
	if !reflect.DeepEqual(swapped.Slots["0:233"], cardTestStack(100, 2, 1)) || !reflect.DeepEqual(swapped.Slots["0:234"], cardTestStack(100, 3, 0)) {
		t.Fatal("整堆交换混合了级数或数量", swapped)
	}
	moved, e = o.Move(context.Background(), Command{Operation: "move_itemspace", AccountID: "acc", SelectedCharacterID: 77, SourceListType: 0, SourceSlotIndex: 233, DestinationListType: 0, DestinationSlotIndex: 236, MoveCount: 2})
	if e != nil || !moved.Changed {
		t.Fatal(moved, e)
	}
	_, e = o.Sort(context.Background(), Command{Operation: "sort_item", AccountID: "acc", SelectedCharacterID: 77, SourceListType: 0, Category: 10})
	if e != nil {
		t.Fatal(e)
	}
	inv := loadTestInventory(t, context.Background(), r, "77")
	totals := map[byte]int64{}
	for _, s := range inv.Slots {
		level, ok := repo.MonsterCardUpgradeCount(s)
		if !ok {
			t.Fatal(s)
		}
		totals[level] += s.Count
	}
	if totals[0] != 7 || totals[1] != 2 {
		t.Fatal("整理混级", totals)
	}
}
