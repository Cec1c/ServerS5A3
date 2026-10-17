package quest

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestFinishRewardEquipmentBatchConservesCountsAndKeepsStackablesCoalesced(t *testing.T) {
	var requests []FinishItemGrantRequest
	allocate := func(_ *dnfrepo.InventoryRecord, request FinishItemGrantRequest) ([]FinishCommittedItem, error) {
		requests = append(requests, request)
		if request.ItemID == 101040191 {
			return []FinishCommittedItem{finishBatchReceipt("0:9", request.ItemID, 1), finishBatchReceipt("0:10", request.ItemID, 1)}, nil
		}
		return []FinishCommittedItem{finishBatchReceipt("0:65", request.ItemID, request.Count)}, nil
	}
	base := FinishItemGrantRequest{QuestID: 4728, CompletionKey: "完成回执", Source: "quest_pvf:奖励表"}
	items, err := allocateFinishRewardItems(&dnfrepo.InventoryRecord{}, []RewardItemRule{
		{ItemID: 101040191, Count: 1}, {ItemID: 101040191, Count: 1},
		{ItemID: 10403, Count: 2}, {ItemID: 10403, Count: 3},
	}, allocate, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || len(requests) != 2 || requests[0].Count != 2 || requests[1].Count != 5 || items[2].Delta != 5 {
		t.Fatalf("装备拆格/堆叠数量错误: 请求=%+v 回执=%+v", requests, items)
	}
	for _, request := range requests {
		request.ItemID, request.Count = 0, 0
		if !reflect.DeepEqual(request, base) {
			t.Fatalf("任务来源元数据丢失: %+v", request)
		}
	}
}

func TestFinishRewardEquipmentBatchRejectsInvalidReceipts(t *testing.T) {
	good := finishBatchReceipt("0:9", 101040191, 2)
	for _, tc := range []struct {
		name  string
		items []FinishCommittedItem
	}{
		{"空回执", nil},
		{"少发", []FinishCommittedItem{finishBatchReceipt("0:9", 101040191, 1)}},
		{"多发", []FinishCommittedItem{finishBatchReceipt("0:9", 101040191, 3)}},
		{"同格重复", []FinishCommittedItem{finishBatchReceipt("0:9", 101040191, 1), finishBatchReceipt("0:9", 101040191, 1)}},
		{"错误物品", []FinishCommittedItem{finishBatchReceipt("0:9", 1, 2)}},
		{"负数量", []FinishCommittedItem{finishBatchReceipt("0:9", 101040191, -1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := allocateFinishRewardItems(&dnfrepo.InventoryRecord{}, []RewardItemRule{{ItemID: good.ItemID, Count: 2}}, func(*dnfrepo.InventoryRecord, FinishItemGrantRequest) ([]FinishCommittedItem, error) {
				return tc.items, nil
			}, FinishItemGrantRequest{})
			if !errors.Is(err, ErrQuestRewardMalformed) {
				t.Fatalf("非法回执未拒绝: %v", err)
			}
		})
	}
}

func TestFinishRewardEquipmentBatchChecksAggregateNativeRowLimit(t *testing.T) {
	for _, count := range []int64{255, 256} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			allocator := func(_ *dnfrepo.InventoryRecord, request FinishItemGrantRequest) ([]FinishCommittedItem, error) {
				items := make([]FinishCommittedItem, request.Count)
				for i := range items {
					items[i] = finishBatchReceipt(fmt.Sprintf("0:%d", i+int(request.ItemID)*1000), request.ItemID, 1)
				}
				return items, nil
			}
			items, err := allocateFinishRewardItems(&dnfrepo.InventoryRecord{}, []RewardItemRule{{ItemID: 1, Count: 254}, {ItemID: 2, Count: count - 254}}, allocator, FinishItemGrantRequest{})
			if count == 255 {
				if err != nil || len(items) != 255 {
					t.Fatalf("合法255行被拒绝: %d %v", len(items), err)
				}
			} else if !errors.Is(err, ErrQuestRewardMalformed) {
				t.Fatalf("256行提交前未拒绝: %v", err)
			}
		})
	}
}

func finishBatchReceipt(slot string, itemID, delta int64) FinishCommittedItem {
	return FinishCommittedItem{SlotKey: slot, ItemID: itemID, Delta: delta, PostCount: delta, CountOrSeed: 1, RawEntry: []byte{1}}
}
