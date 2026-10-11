package gmtool

import (
	"context"
	"sort"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件补上账号详情里**真实**的「账号金库」投影。
//
// 背景（本批）：账号金库的三条写路由（`POST /api/accounts/{id}/cargo/{max,clear,delete}`）
// 已经实现，但 `api_accounts.go` 的账号详情一直把 `cargo` 硬编码成空数组、`cargoState` 硬编码成
// nil，于是面板的「账号金库」表格恒显示"账号金库为空"、每行的「删除」按钮永远不出现，
// 而「清空」按钮又被前端 `detail.cargo.length === 0` 挡住（sidebar.js:198-217/219-236）
// —— 功能是通的，面板上却用不了。这里把读侧补齐。
//
// 归属与字段依据：
//   - 金库物品 = `dnfrepo.AccountInventoryRecord.Slots` 里 key 形如 `12:<slot>` 的行
//     （`modules/dnf/inventory/account_cargo.go:13-16/113-127`：AccountInventory 是 list 12 的
//     唯一持久归属；`parseItemSlotKey` 见 `api_items_delete.go:280`）。
//   - 金库容量 = 游戏账号记录 `dnf_account_metadata["account_cargo_level"]`
//     （键名刻意复用 `api_account_cargo.go:107` 的 `cargoMetadataLevelKey`，不另写一份字符串；
//     读取复用同一个 `cargoMetadataInt`，口径不会分叉）。
//   - 前端只读 `item.slot / item.templateId / item.name / item.count / item.durability`
//     （sidebar.js:202-204）；`name` 走既有 `s.itemsName`，`durability` 与背包视图同口径
//     （只读 `stack.Extra["durability"]`，见 api_items.go:624）。
//
// 只读：本函数绝不写库；读不到（仓储缺失 / Load 失败 / 账号没有金库记录）时返回空表 +
// `cargoState.available=false` 与具名 `reason`，绝不把"读不到"伪装成"已经空了"。
const accountCargoListType uint16 = 12

// accountCargoRowPayload 是面板「账号金库」表格的一行。
type accountCargoRowPayload struct {
	Slot       int16  `json:"slot"`
	Key        string `json:"key"`
	TemplateID int64  `json:"templateId"`
	Name       string `json:"name"`
	Count      int64  `json:"count"`
	Durability int64  `json:"durability"`
}

// accountCargoProjection 返回（表格行，状态块）。两个返回值都保证非 nil（前端直接取长度）。
func (s *Server) accountCargoProjection(
	ctx context.Context,
	group dnfrepo.Group,
	account webAccount,
) ([]accountCargoRowPayload, map[string]any) {
	rows := make([]accountCargoRowPayload, 0, 64)
	state := map[string]any{
		"listType":    accountCargoListType,
		"slotCount":   0,
		"available":   false,
		"reason":      "",
		"listParam16": nil,
	}
	if group.AccountInventory == nil {
		state["reason"] = "account_inventory_repository_missing"
		return rows, state
	}
	record, found, err := group.AccountInventory.Load(ctx, account.AccountID)
	if err != nil {
		state["reason"] = "account_inventory_load_failed"
		return rows, state
	}
	if !found {
		state["reason"] = "account_inventory_missing"
		return rows, state
	}

	keys := make([]string, 0, len(record.Slots))
	for key := range record.Slots {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		listType, slot, parseErr := parseItemSlotKey(key)
		if parseErr != nil || uint16(listType) != accountCargoListType {
			continue
		}
		stack := record.Slots[key]
		rows = append(rows, accountCargoRowPayload{
			Slot:       slot,
			Key:        key,
			TemplateID: stack.ItemID,
			Name:       s.itemsName(stack.ItemID),
			Count:      stack.Count,
			Durability: accountCargoDurability(stack),
		})
	}
	state["available"] = true
	state["slotCount"] = len(rows)
	if group.Account != nil {
		if accountRecord, accountFound, accountErr := group.Account.Load(ctx, account.AccountID); accountErr == nil && accountFound {
			if level := cargoMetadataInt(accountRecord.Metadata, cargoMetadataLevelKey); level > 0 {
				state["listParam16"] = level
			}
		}
	}
	return rows, state
}

// accountCargoDurability 与背包视图同口径：只读 `stack.Extra["durability"]`
// （api_items.go:624 同源），拿不到就是 0，不猜、不从物品模板补。
func accountCargoDurability(stack dnfrepo.ItemStack) int64 {
	raw := strings.TrimSpace(stack.Extra["durability"])
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}
