package repository

import (
	"math"
	"strconv"
	"testing"
)

// stackContractRow 造一行"PVF 说得清堆叠资格"的形状；limit <= 0 = 不写 [stack limit]。
func stackContractRow(itemID int64, count int64, limit int64) ItemStack {
	extra := map[string]string{
		"item_kind":      "stackable",
		"pvf_path":       "stackable/material/crystal_pure.stk",
		"stackable_type": "[material]",
	}
	if limit > 0 {
		extra["stack_limit"] = strconv.FormatInt(limit, 10)
	}
	return ItemStack{ItemID: itemID, Count: count, Extra: extra}
}

// 边界一：PVF 没写 [stack limit]（现场 3243/3244/宠物饲料的真实形状）= 无上限，
// 只要数量还在有符号 32 位里就成立（当前客户端与 86JP 参考实现这么装数量）。
func TestStackContractTreatsMissingLimitAsUncapped(t *testing.T) {
	path, limit, ok := StackContract(stackContractRow(3244, 4, 0))
	if !ok || limit != 0 || path != "stackable/material/crystal_pure.stk" {
		t.Fatalf("uncapped row = (%q,%d,%t), want (stackable/material/crystal_pure.stk,0,true)", path, limit, ok)
	}
	if _, _, ok := StackContract(stackContractRow(3244, math.MaxInt32, 0)); !ok {
		t.Fatal("MaxInt32 count must still satisfy the uncapped contract")
	}
	if _, _, ok := StackContract(stackContractRow(3244, math.MaxInt32+1, 0)); ok {
		t.Fatal("count above MaxInt32 must be rejected (signed 32-bit current client)")
	}
	// 显式写 0 与缺段同义（PVF 里 [stack limit] 0 = 无上限）。
	explicitZero := stackContractRow(3244, 5, 0)
	explicitZero.Extra["stack_limit"] = "0"
	if _, limit, ok := StackContract(explicitZero); !ok || limit != 0 {
		t.Fatalf("explicit zero limit = (%d,%t), want (0,true)", limit, ok)
	}
}

// 边界二：写了上限时"刚好满"成立、"超一格"不成立——合并循环就是靠这个把剩余量
// 挡回去另开新格的（③ 的封顶用例断言的就是这个边界）。
func TestStackContractHonorsExplicitLimitExactly(t *testing.T) {
	if _, limit, ok := StackContract(stackContractRow(3243, 1000, 1000)); !ok || limit != 1000 {
		t.Fatalf("exactly full stack = (%d,%t), want (1000,true)", limit, ok)
	}
	if _, _, ok := StackContract(stackContractRow(3243, 1001, 1000)); ok {
		t.Fatal("a row above its PVF limit must not satisfy the contract")
	}
	if _, _, ok := StackContract(stackContractRow(3243, 0, 1000)); ok {
		t.Fatal("an empty row must not satisfy the contract")
	}
	if _, _, ok := StackContract(stackContractRow(3243, 9, 1000)); !ok {
		t.Fatal("a row below its PVF limit must satisfy the contract")
	}
}

// 边界三：缺契约键的行**必须**判成不可堆叠——现场 3243/3244 的 16 行就是这样，
// 于是 op19 只能 swap，玩家看到"这些道具不能正常堆叠"。
func TestStackContractRejectsRowsWithoutThePVFContract(t *testing.T) {
	cases := map[string]ItemStack{
		"no extra at all":     {ItemID: 3243, Count: 9},
		"only source":         {ItemID: 3243, Count: 9, Extra: map[string]string{"source": "upgrade_destroy_bonus"}},
		"kind not stackable":  {ItemID: 3243, Count: 9, Extra: map[string]string{"item_kind": "equipment", "pvf_path": "stackable/material/x.stk"}},
		"path without prefix": {ItemID: 10159603, Count: 1, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "10159001/10159603.stk"}},
		"path traversal":      {ItemID: 3243, Count: 9, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/../etc/x.stk"}},
		"bad limit":           {ItemID: 3243, Count: 9, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/material/x.stk", "stack_limit": "abc"}},
		"negative limit":      {ItemID: 3243, Count: 9, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/material/x.stk", "stack_limit": "-1"}},
		"zero item":           {ItemID: 0, Count: 9, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/material/x.stk"}},
	}
	for name, row := range cases {
		if _, _, ok := StackContract(row); ok {
			t.Fatalf("%s: row must not satisfy the contract: %+v", name, row)
		}
	}
	// 反斜杠分隔的 pvf_path 归一化后仍然成立（真实 PVF 目录用过反斜杠）。
	row := stackContractRow(3243, 9, 0)
	row.Extra["pvf_path"] = `stackable\material\vestiges_intensive.stk`
	if path, _, ok := StackContract(row); !ok || path != "stackable/material/vestiges_intensive.stk" {
		t.Fatalf("normalized path = (%q,%t)", path, ok)
	}
	// 大小写不敏感（发布期 PVF 大小写并不统一）。
	upper := stackContractRow(3243, 9, 0)
	upper.Extra["item_kind"] = "Stackable"
	upper.Extra["pvf_path"] = "Stackable/Material/Vestiges_Intensive.stk"
	if _, _, ok := StackContract(upper); !ok {
		t.Fatal("case-insensitive contract must hold")
	}
}
