package itemtrade

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOwnerExchangeAtomicallyMovesBothOffers(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	for _, record := range []dnfrepo.InventoryRecord{
		{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:97": {ItemID: 1001, Count: 1, RawEntry: []byte{1, 2, 3}}}},
		{CharacterID: "5", Slots: map[string]dnfrepo.ItemStack{"0:57": {ItemID: 2002, Count: 3}}},
	} {
		if err := repositories.Inventory.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	owner.now = func() time.Time { return time.Unix(100, 0) }

	result, err := owner.Exchange(ctx,
		Participant{CharacterID: "1", Offers: []Offer{{TradeSlot: 0, SourceList: 0, SourceSlot: 97, Count: 1, ExpectedItem: 1001}}},
		Participant{CharacterID: "5", Offers: []Offer{{TradeSlot: 0, SourceList: 0, SourceSlot: 57, Count: 2, ExpectedItem: 2002}}},
	)
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	first := result.Inventories["1"]
	second := result.Inventories["5"]
	if first.Slots["0:9"].ItemID != 2002 || first.Slots["0:9"].Count != 2 {
		t.Fatalf("first received = %+v", first.Slots)
	}
	if second.Slots["0:65"].ItemID != 1001 || second.Slots["0:65"].Count != 1 || second.Slots["0:57"].Count != 1 {
		t.Fatalf("second inventory = %+v", second.Slots)
	}
	if len(result.Received["1"]) != 1 || result.Received["1"][0].TradeSlot != 0 || result.Received["1"][0].DestinationSlot != 9 {
		t.Fatalf("first transfer = %+v", result.Received["1"])
	}
}

func TestOwnerExchangeRejectsBoundItemWithoutMutation(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "1",
		Slots:       map[string]dnfrepo.ItemStack{"0:97": {ItemID: 1001, Count: 1, Bind: true}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "5", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(repositories)
	_, err := owner.Exchange(ctx,
		Participant{CharacterID: "1", Offers: []Offer{{TradeSlot: 0, SourceList: 0, SourceSlot: 97, Count: 1, ExpectedItem: 1001}}},
		Participant{CharacterID: "5"},
	)
	if !errors.Is(err, ErrItemNotTradeable) {
		t.Fatalf("Exchange() error = %v", err)
	}
	loaded, _, _ := repositories.Inventory.Load(ctx, "1")
	if loaded.Slots["0:97"].ItemID != 1001 {
		t.Fatalf("bound source mutated: %+v", loaded.Slots)
	}
}

func TestOwnerExchangeAtomicallyMovesGoldAndItem(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	for _, record := range []dnfrepo.CharacterRecord{
		{CharacterID: "1", AccountID: "a", Stats: map[string]int64{"gold": 250_000}},
		{CharacterID: "5", AccountID: "b", Stats: map[string]int64{"gold": 40_000}},
	} {
		if err := repositories.Character.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "5",
		Slots:       map[string]dnfrepo.ItemStack{"0:57": {ItemID: 2002, Count: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}

	result, err := owner.Exchange(ctx,
		Participant{CharacterID: "1", Gold: 100_000},
		Participant{CharacterID: "5", Gold: 10_000, Offers: []Offer{{TradeSlot: 1, SourceList: 0, SourceSlot: 57, Count: 1, ExpectedItem: 2002}}},
	)
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if got := result.Characters["1"].Stats["gold"]; got != 159_700 {
		t.Fatalf("first gold = %d", got)
	}
	if got := result.Characters["5"].Stats["gold"]; got != 127_000 {
		t.Fatalf("second gold = %d", got)
	}
	if got := result.Inventories["1"].Slots["0:9"]; got.ItemID != 2002 || got.Count != 1 {
		t.Fatalf("first received item = %+v", got)
	}
}

func TestOwnerExchangePlacesExpandedSourceSlotAtVisiblePageStart(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "1",
		Slots:       map[string]dnfrepo.ItemStack{"0:110": {ItemID: 490702514, Count: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "7", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}

	result, err := owner.Exchange(ctx,
		Participant{CharacterID: "1", Offers: []Offer{{TradeSlot: 1, SourceList: 0, SourceSlot: 110, Count: 1, ExpectedItem: 490702514}}},
		Participant{CharacterID: "7"},
	)
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if got := result.Inventories["7"].Slots["0:65"]; got.ItemID != 490702514 || got.Count != 1 {
		t.Fatalf("recipient visible consumable slot = %+v, inventory = %+v", got, result.Inventories["7"].Slots)
	}
	if got := result.Received["7"][0].DestinationSlot; got != 65 {
		t.Fatalf("destination slot = %d, want 65", got)
	}
}

func TestOwnerExchangeRejectsInsufficientGoldWithoutMovingItem(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	for _, record := range []dnfrepo.CharacterRecord{
		{CharacterID: "1", AccountID: "a", Stats: map[string]int64{"gold": 99}},
		{CharacterID: "5", AccountID: "b", Stats: map[string]int64{"gold": 0}},
	} {
		if err := repositories.Character.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "5",
		Slots:       map[string]dnfrepo.ItemStack{"0:57": {ItemID: 2002, Count: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(repositories)
	_, err := owner.Exchange(ctx,
		Participant{CharacterID: "1", Gold: 100},
		Participant{CharacterID: "5", Offers: []Offer{{TradeSlot: 1, SourceList: 0, SourceSlot: 57, Count: 1, ExpectedItem: 2002}}},
	)
	if !errors.Is(err, ErrGoldUnavailable) {
		t.Fatalf("Exchange() error = %v", err)
	}
	character, _, _ := repositories.Character.Load(ctx, "1")
	if got := character.Stats["gold"]; got != 99 {
		t.Fatalf("first gold mutated to %d", got)
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "5")
	if got := inventory.Slots["0:57"]; got.ItemID != 2002 || got.Count != 1 {
		t.Fatalf("offered item mutated: %+v", inventory.Slots)
	}
}

// 交易收下的堆叠品必须先并入同身份的既有行（旧代码无条件另开一格，于是收同一件
// 材料越收越多行）。判据 = dnfrepo.StackContract + dnfrepo.CanMergeItemStack，
// 与发放路径/op19 同一份实现。
func TestOwnerExchangeMergesReceivedStackIntoExistingRow(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	existing := stackableTradeRow(3244, 4, 0)
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:123": existing},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "5", Slots: map[string]dnfrepo.ItemStack{"0:124": stackableTradeRow(3244, 3, 0)},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}

	result, err := owner.Exchange(ctx,
		Participant{CharacterID: "1"},
		Participant{CharacterID: "5", Offers: []Offer{{TradeSlot: 0, SourceList: 0, SourceSlot: 124, Count: 3, ExpectedItem: 3244}}},
	)
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	received := result.Inventories["1"].Slots
	if got := received["0:123"]; got.ItemID != 3244 || got.Count != 7 {
		t.Fatalf("recipient row = %+v, want 3244 x7 merged into the existing stack", got)
	}
	if _, opened := received["0:121"]; opened {
		t.Fatalf("a new slot was opened instead of merging: %+v", received)
	}
	if len(result.Received["1"]) != 1 || result.Received["1"][0].DestinationSlot != 123 ||
		result.Received["1"][0].Stack.Count != 7 {
		t.Fatalf("transfer receipt = %+v, want the merged row 123 x7", result.Received["1"])
	}
}

// 上限封顶 + 溢出：既有行 998/1000，收下 5 ⇒ 既有行到 1000，剩下 3 另开一格。
func TestOwnerExchangeCapsMergedStackAtPVFLimitAndSpillsTheRest(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:123": stackableTradeRow(3243, 998, 1000)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "5", Slots: map[string]dnfrepo.ItemStack{"0:124": stackableTradeRow(3243, 5, 1000)},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}

	result, err := owner.Exchange(ctx,
		Participant{CharacterID: "1"},
		Participant{CharacterID: "5", Offers: []Offer{{TradeSlot: 0, SourceList: 0, SourceSlot: 124, Count: 5, ExpectedItem: 3243}}},
	)
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	slots := result.Inventories["1"].Slots
	if got := slots["0:123"]; got.Count != 1000 {
		t.Fatalf("merged row = %+v, want the PVF limit 1000", got)
	}
	if got := slots["0:121"]; got.ItemID != 3243 || got.Count != 3 {
		t.Fatalf("spill row = %+v, want 3243 x3 in the first empty material slot", got)
	}
	if len(result.Received["1"]) != 2 {
		t.Fatalf("transfer receipts = %+v, want the merged row plus the spill", result.Received["1"])
	}
}

// 装备（没有堆叠契约）必须照旧另开一格，绝不折进任何一行。
func TestOwnerExchangeKeepsNonStackableReceiptInItsOwnSlot(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "1",
		Slots: map[string]dnfrepo.ItemStack{
			"0:9": {ItemID: 700, Count: 1, Extra: map[string]string{"item_kind": "equipment"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "5",
		Slots: map[string]dnfrepo.ItemStack{
			"0:10": {ItemID: 700, Count: 1, Extra: map[string]string{"item_kind": "equipment"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}

	result, err := owner.Exchange(ctx,
		Participant{CharacterID: "1"},
		Participant{CharacterID: "5", Offers: []Offer{{TradeSlot: 0, SourceList: 0, SourceSlot: 10, Count: 1, ExpectedItem: 700}}},
	)
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	slots := result.Inventories["1"].Slots
	if got := slots["0:9"]; got.Count != 1 {
		t.Fatalf("existing equipment row mutated: %+v", got)
	}
	// 收件人自己的 0:9 已被占，装备页 9..64 的第一个空格是 0:10（发送方的 0:10 是
	// 另一个角色的行，两者互不相干）；装备没有堆叠契约，必须自己占一格。
	if got := slots["0:10"]; got.ItemID != 700 || got.Count != 1 {
		t.Fatalf("received equipment = %+v, want its own slot; all recipient slots = %+v", got, slots)
	}
}

// stackableTradeRow 复刻线上 3243/3244 那一族的形状（真档 PVF 无 [stack limit]；
// limit > 0 时写显式上限）。
func stackableTradeRow(itemID int64, count int64, limit int64) dnfrepo.ItemStack {
	path := "stackable/material/crystal_pure.stk"
	if itemID == 3243 {
		path = "stackable/material/vestiges_intensive.stk"
	}
	extra := map[string]string{
		"item_kind":      "stackable",
		"pvf_path":       path,
		"stackable_type": "[material]",
	}
	if limit > 0 {
		extra["stack_limit"] = strconv.FormatInt(limit, 10)
	}
	return dnfrepo.ItemStack{ItemID: itemID, Count: count, Extra: extra}
}
