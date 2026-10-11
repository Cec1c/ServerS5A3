package gmtool

// A：账号金库（account cargo，list type 12）三条写路由。
//
//	POST /api/accounts/{id}/cargo/max     把账号金库扩到满级（最后一档容量）
//	POST /api/accounts/{id}/cargo/clear   清空账号金库（只删 list 12 的槽位）
//	POST /api/accounts/{id}/cargo/delete  按槽位删除一条（前端 body 是 {"slot":N}）
//
// # 前端契约（wwwroot/js/sidebar.js:198-241，逐字段对齐，不许改名）
//
//   - max：`const r = await post(...); toast('账号金库满级已设置: ' + r.listParam16)`
//     ⇒ 响应必须带 **listParam16**（与既有 personal-cargo/max、avatar-inventory/max
//     同名字段，见 api_characters.go:967-971/1012-1016）。
//   - clear：`const r = await post(...); toast('已清空账号金库 (' + r.deleted + ' 件)')`
//     ⇒ 响应必须带 **deleted**（本次真实删掉的条数）。
//   - delete：`post(..., { slot: item.slot })` ⇒ 请求字段名就是 **slot**。
//     页面上的行来自 `GET /api/accounts/{id}/detail` 的 `cargo[]`（每条是
//     `{slot, templateId, name, count, durability}`，vendor GmService.Accounts.cs:145-158）。
//     本仓 api_accounts.go:109 目前把 `cargo` 硬编码成 `[]`（那是既有文件的既有行为，
//     本批不动它），所以面板里的"删除"按钮今天不会出现在任何一行上；但**接口本身**
//     必须真能用 —— 按 slot 删除是它的完整语义。
//
// # 数据归属（90CN 侧唯一的真源，已核对，不是猜的）
//
//   - 金库物品 = `AccountInventoryRecord.Slots` 里 key 形如 `12:<slot>` 的行。
//     依据：modules/dnf/inventory/account_cargo.go:13-16/113-127 —— "AccountInventory is
//     deliberately the sole durable owner of list 12"，且 accountCargoItemMap 把
//     list 12 映射到 `cargo.Slots`；list 12 绝不会出现在角色背包 map 里
//     （repository/item_normalization_audit.go:163-167 把"list 12 落在角色 Slots"
//     判成 warehouse_item_in_inventory_map 错误）。
//   - 金库容量 = 账号元数据 `account_cargo_level`（dnfbridge/current_item_list_account.go:69-88
//     把它当 selection key/u16 下发给客户端；inventory/account_cargo.go:93-111 用它当
//     槽位上界 capacity，越界 move 直接拒绝）。**它不是"等级"，就是格数。**
//   - 金库是否已创建 = `account_cargo_created`（inventory/account_cargo.go:99
//     把 `level > 0` 也算作已创建）。
//   - 金库金币是另一个键 `account_cargo_gold`（api_accounts.go:390-411 的 J16 已实现），
//     本文件**不碰**它。
//
// # 满级档位的取值来自唯一那份成长表，不写死数字
//
// 客户端侧的金库成长梯子是 modules/dnf/cargo/owner.go:33 的
// `accountCargoTiers = {1,8,16,24,32,40,48,56,64}`，并且该包**已导出**
// `NextAccountCargoTier`（owner.go:392-397，注释明说"keeping one progression prevents
// the shop from silently skipping a normal client-visible tier"，点券商店升级
// dnfbridge/cera_shop_purchase.go:808-824 用的就是它）。本文件顺着这条链取**终点**，
// 于是"满级"永远等于游戏自己的最后一档；PVF/表变了也不会漂移成第二个数字。
// 对照：A21 C# 工具（vendor GmService.Accounts.cs:378-408）写死 `AccountCargoSlotEnd -
// AccountCargoSlotStart + 1 = 120`（A21InventorySlotPolicy.cs:44-45），那是 A21 自己的
// 槽位表；90CN 的梯子终点是 64，两者不是同一套数据，**不能照抄 120**。
//
// # 与上游的逐段对拍
//
//	vendor Program.cs:221-226                三条路由（delete 带 SlotRequest，clear/max 无 body）
//	vendor GmService.Accounts.cs:365-370     DeleteAccountCargoAt：删不掉就返回失败（"该槽位没有物品"）
//	vendor GmService.Accounts.cs:372-376     ClearAccountCargo：返回 deleted 计数
//	vendor GmService.Accounts.cs:378-408     MaxAccountCargo：写 selection_key = 满档，
//	                                         返回 {accountId, listType, listParam16, affectedRows}，
//	                                         状态行不存在时明确失败（"账号仓库状态不存在"）
//
// # 本仓与上游的有意差异（都是收紧；拒绝路径零写入）
//
//	CARGO-D1 账号共享背包记录缺失时 clear/delete **明确失败**，不静默成功。
//	         上游 ClearAccountCargo 对不存在的行返回 deleted=0 + success（sqlite 的
//	         DELETE 影响 0 行照样成功）。本仓与 api_items_delete.go:77-88 的 D3
//	         （container=account 且账号记录缺失 ⇒ 明确失败）同口径：仓库读不出来时
//	         "已清空 0 件"是一句可能骗人的话。
//	CARGO-D2 clear 只删能解析出 list_type==12 的键；解析不出来的坏键**保留**并计入
//	         `skipped`（与 api_items_delete.go:948-970 的 D4 同一立场：不删看不懂的东西）。
//	CARGO-D3 写入走 `dnfrepo.SaveAccountMetadataEntry`（键级 UPSERT）而不是整记录
//	         `Account.Save`。理由与 api_accounts.go:289-299 的 J16-1 逐字相同：整记录写
//	         对并发写入是 last-write-wins，会覆盖别的 GM 刚改的荣誉/点券。
//	         代价是两键分两次写（先 created 再 level），任何一次失败都在响应里点名。
//	CARGO-D4 账号级在线守卫（requireAccountOffline，online_guard.go:158-216）：金库物品与
//	         容量都被在线会话的投影缓存着（dnfbridge/current_item_list_account.go:12-67），
//	         只写库会让在线客户端继续看到旧值。默认档（DNFBRIDGE_GM_REQUIRE_OFFLINE 未开）
//	         不检查 —— 与 P2/P3/P4 全部新写路由同一约定。
//	CARGO-D5 **没有**账号级物品事务可用：`Group.AccountItems.WithinAccountCharacterItems`
//	         要求非空 characterID（mysql_account_inventory_transaction.go:24-28），而账号级
//	         金库操作可能发生在"该账号一个角色都没有"的时候。因此这里用
//	         `Group.AccountInventory.Load/Save` 这条**账号记录唯一的持久化路径**
//	         （与 inventory/account_cargo.go:237-242 事务内调用的同一个仓储/同一方法），
//	         不做第二套写库逻辑，也绝不自己拼 SQL。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	dnfcargo "longheng.io/server/internal/modules/dnf/cargo"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// cargoListType 是账号金库的 list type。与 api_characters.go:33 的
	// listTypeAccountCargo（12）是同一个值；单独起名只是让本文件的代码读得懂。
	// 交叉证据：modules/dnf/inventory/request.go:20、repository/item_normalization_audit.go:328。
	cargoListType = listTypeAccountCargo

	// cargoMetadataLevelKey / cargoMetadataCreatedKey 是账号元数据里金库容量与
	// "已创建"标记的键，与 cargo/owner.go:99-101/152-153、inventory/account_cargo.go:95-99、
	// dnfbridge/current_item_list_account.go:70 完全同名。
	cargoMetadataLevelKey   = "account_cargo_level"
	cargoMetadataCreatedKey = "account_cargo_created"

	// cargoMaxTierSteps 是顺着成长表往上爬的最大步数（表只有 9 档）。超过就说明
	// 表成环或没有终点，那时必须明确失败，绝不猜一个"满级"。
	cargoMaxTierSteps = 64

	// cargoSlotMaxExclusive 是槽位字段的语法上界（ItemStack 的 key 用 int16 槽位，
	// 与 parseItemSlotKey 的 int16 解析同界）。它**不是**容量：容量由
	// dnf_account_metadata["account_cargo_level"] 决定，本文件不拿它做准入判断
	// （上游 DeleteAccountCargoAt 同样只按 slot 删，不查容量）。
	cargoSlotMaxExclusive = 1 << 15
)

var (
	cargoErrAccountRepoUnavailable   = errors.New("账号仓储未就绪")
	cargoErrInventoryRepoUnavailable = errors.New("账号共享背包仓储未就绪")
	cargoErrAccountMissing           = errors.New("账号不存在")
	cargoErrStorageMissing           = errors.New("该账号没有账号共享背包记录（dnf_account_inventories 里没有这个账号的行）")
	cargoErrTierUnbounded            = errors.New("账号金库成长表没有终点（NextAccountCargoTier 走不到最后一档）")
	cargoErrTierOutOfRange           = errors.New("账号金库满级档位超出客户端可表示的 u16 范围")
)

// cargoDeleteRequest 是 `POST …/cargo/delete` 的请求体：只有 slot 一个字段。
//
// 用指针的理由与 api_items_delete.go:66-74 的 D1、api_account_cubes.go:54-59 的 D3 一样：
// **槽位 0 是合法金库槽**，所以"没给 slot"与"给了 slot=0"必须区分开 ——
// 少了这个区分，一个空 body 会被静默解释成"删 12:0 那件"。
type cargoDeleteRequest struct {
	Slot *int `json:"slot"`
}

// ---------------------------------------------------------------------------
// 路由
// ---------------------------------------------------------------------------

func (s *Server) handleAccountCargoMax(w http.ResponseWriter, r *http.Request) {
	panelID, ok := cargoPanelID(w, r)
	if !ok {
		return
	}
	ctx, group, account, ok := s.cargoAccountForPanel(w, r, panelID)
	if !ok {
		return
	}
	if !s.requireAccountOffline(ctx, w, "account-cargo-max", group, account.AccountID, int(panelID)) {
		return
	}
	s.cargoApplyMax(w, ctx, group, account, panelID)
}

func (s *Server) handleAccountCargoClear(w http.ResponseWriter, r *http.Request) {
	panelID, ok := cargoPanelID(w, r)
	if !ok {
		return
	}
	ctx, group, account, ok := s.cargoAccountForPanel(w, r, panelID)
	if !ok {
		return
	}
	if !s.requireAccountOffline(ctx, w, "account-cargo-clear", group, account.AccountID, int(panelID)) {
		return
	}
	s.cargoApplyClear(w, ctx, group, account, panelID)
}

func (s *Server) handleAccountCargoDelete(w http.ResponseWriter, r *http.Request) {
	// 顺序与 api_accounts.go:177-199 的 handleAccountCurrency 一致：
	// 面板账号号 → 请求体 → （本路由特有的槽位校验）→ 仓储 → 账号行 → 守卫 → 落库。
	// 槽位校验刻意排在查库之前：它不依赖任何数据库状态，坏请求不该先去碰账号表。
	panelID, ok := cargoPanelID(w, r)
	if !ok {
		return
	}
	body, ok := decodeBody[cargoDeleteRequest](w, r)
	if !ok {
		return
	}
	if body.Slot == nil {
		s.logEvent("gm-account-cargo-rejected",
			"action", "delete", "panel_id", panelID, "account_id", "",
			"reason", "missing_slot", "result", "rejected")
		writeFailure(w, "缺少 slot（金库槽位 0 也是合法槽位，必须显式给出；未做任何写入）。")
		return
	}
	if *body.Slot < 0 || *body.Slot >= cargoSlotMaxExclusive {
		s.logEvent("gm-account-cargo-rejected",
			"action", "delete", "panel_id", panelID, "account_id", "",
			"slot", *body.Slot, "reason", "slot_out_of_range", "result", "rejected")
		writeFailure(w, fmt.Sprintf("slot 必须在 0..%d 之间，当前 %d（未做任何写入）。",
			cargoSlotMaxExclusive-1, *body.Slot))
		return
	}
	ctx, group, account, ok := s.cargoAccountForPanel(w, r, panelID)
	if !ok {
		return
	}
	if !s.requireAccountOffline(ctx, w, "account-cargo-delete", group, account.AccountID, int(panelID)) {
		return
	}
	s.cargoApplyDelete(w, ctx, group, account, panelID, *body.Slot)
}

// cargoPanelID 解析路径里的面板账号号。
func cargoPanelID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	panelID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil {
		writeFailure(w, "账号编号无效")
		return 0, false
	}
	return panelID, true
}

// cargoAccountForPanel 解析仓储组与启动器账号行（三条路由共用的前半段，
// 与 api_accounts.go:69-86 的 handleAccountDetail 逐段同序）。
//
// 抽成"账号已解析之后"的边界，是为了让核心逻辑能被单测直接调用（单测里没有 s.db）；
// 与 api_accounts.go:201-206 对 applyAccountCurrency 的拆分理由完全相同。
func (s *Server) cargoAccountForPanel(
	w http.ResponseWriter,
	r *http.Request,
	panelID int64,
) (context.Context, dnfrepo.Group, webAccount, bool) {
	ctx := r.Context()
	group, repoErr := s.repositories()
	if repoErr != nil {
		writeFailure(w, repoErr.Error())
		return ctx, dnfrepo.Group{}, webAccount{}, false
	}
	account, err := s.webAccountByPanelID(ctx, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("账号不存在: %d", panelID))
		return ctx, group, webAccount{}, false
	}
	return ctx, group, account, true
}

// ---------------------------------------------------------------------------
// 满级：POST /api/accounts/{id}/cargo/max
// ---------------------------------------------------------------------------

// cargoMaxLevel 顺着 cargo.NextAccountCargoTier 走到终点，返回满级格数。
//
// 起点 1 是 cargo/owner.go:153 建库时写入的第一档（accountCargoTiers[0]）。整条链
// 只有一个实现（那个包导出的函数），这里只负责"走到没有下一档为止"。
func cargoMaxLevel() (int64, error) {
	level := int64(1)
	for step := 0; step < cargoMaxTierSteps; step++ {
		next, ok := dnfcargo.NextAccountCargoTier(level)
		if !ok || next <= level {
			if level <= 0 || level > 65535 {
				return 0, cargoErrTierOutOfRange
			}
			return level, nil
		}
		level = next
	}
	return 0, cargoErrTierUnbounded
}

func (s *Server) cargoApplyMax(
	w http.ResponseWriter,
	ctx context.Context,
	group dnfrepo.Group,
	account webAccount,
	panelID int64,
) {
	maxLevel, err := cargoMaxLevel()
	if err != nil {
		s.logEvent("gm-account-cargo-max-failed",
			"panel_id", panelID, "account_id", account.AccountID,
			"reason", "tier_table_unusable", "error", err.Error(), "result", "failed")
		writeFailure(w, "读取账号金库成长表失败: "+err.Error())
		return
	}
	if group.Account == nil {
		s.logEvent("gm-account-cargo-max-failed",
			"panel_id", panelID, "account_id", account.AccountID,
			"reason", "account_repository_missing", "result", "failed")
		writeFailure(w, cargoErrAccountRepoUnavailable.Error())
		return
	}
	record, found, err := group.Account.Load(ctx, account.AccountID)
	if err != nil {
		writeFailure(w, "读取账号失败: "+err.Error())
		return
	}
	if !found {
		writeFailure(w, cargoErrAccountMissing.Error()+"。")
		return
	}

	current := cargoMetadataInt(record.Metadata, cargoMetadataLevelKey)
	// 与 inventory/account_cargo.go:99 同一判据：level > 0 也算已创建。
	created := cargoMetadataBool(record.Metadata, cargoMetadataCreatedKey) || current > 0
	if created && current >= maxLevel {
		// 已经满级：幂等成功，**零写入**（与 api_accounts.go:357-368 的 delta=0 noop 同形）。
		s.logEvent("gm-account-cargo-max",
			"panel_id", panelID, "account_id", account.AccountID,
			"previous_level", current, "max_level", maxLevel, "changed", false, "result", "noop")
		writeOK(w, map[string]any{
			"accountId":     account.PanelID,
			"accountName":   account.Account,
			"listType":      cargoListType,
			"listParam16":   maxLevel,
			"previousLevel": current,
			"created":       created,
			"changed":       false,
			"affectedKeys":  []string{},
		})
		return
	}

	now := time.Now().UTC()
	affected := make([]string, 0, 2)
	if !created {
		// 先写 created 再写 level：万一第二次失败，留下的状态是"已创建但 0 格"
		// （= 客户端看到的空金库），而不是"未创建却写着 64 格"这种自相矛盾的组合。
		if err := dnfrepo.SaveAccountMetadataEntry(ctx, group.Account, record,
			cargoMetadataCreatedKey, "true", now); err != nil {
			s.logEvent("gm-account-cargo-max-failed",
				"panel_id", panelID, "account_id", account.AccountID,
				"previous_level", current, "max_level", maxLevel,
				"reason", "write_failed", "key", cargoMetadataCreatedKey,
				"error", err.Error(), "result", "failed")
			writeFailure(w, "写入账号金库创建标记失败: "+err.Error())
			return
		}
		affected = append(affected, cargoMetadataCreatedKey)
	}
	if err := dnfrepo.SaveAccountMetadataEntry(ctx, group.Account, record,
		cargoMetadataLevelKey, strconv.FormatInt(maxLevel, 10), now); err != nil {
		s.logEvent("gm-account-cargo-max-failed",
			"panel_id", panelID, "account_id", account.AccountID,
			"previous_level", current, "max_level", maxLevel,
			"reason", "write_failed", "key", cargoMetadataLevelKey,
			"already_written", strings.Join(affected, ","),
			"error", err.Error(), "result", "failed")
		writeFailure(w, fmt.Sprintf(
			"写入账号金库容量失败: %s（已写入 %s，当前容量仍是 %d，未做其它改动）。",
			err.Error(), strings.Join(affected, ","), current))
		return
	}
	affected = append(affected, cargoMetadataLevelKey)

	s.logEvent("gm-account-cargo-max",
		"panel_id", panelID, "account_id", account.AccountID,
		"previous_level", current, "max_level", maxLevel, "created_before", created,
		"affected_keys", strings.Join(affected, ","), "changed", true, "result", "applied")
	writeOK(w, map[string]any{
		"accountId":     account.PanelID,
		"accountName":   account.Account,
		"listType":      cargoListType,
		"listParam16":   maxLevel,
		"previousLevel": current,
		"created":       true,
		"changed":       true,
		"affectedKeys":  affected,
	})
}

// ---------------------------------------------------------------------------
// 清空：POST /api/accounts/{id}/cargo/clear
// ---------------------------------------------------------------------------

// cargoRemovedPayload 是一条被删掉的槽位的如实记录（进响应与审计事件）。
type cargoRemovedPayload struct {
	Slot       int    `json:"slot"`
	Key        string `json:"key"`
	ItemID     int64  `json:"itemId"`
	Name       string `json:"name"`
	Count      int64  `json:"count"`
	RawDropped bool   `json:"rawEntryDropped"`
}

func (s *Server) cargoApplyClear(
	w http.ResponseWriter,
	ctx context.Context,
	group dnfrepo.Group,
	account webAccount,
	panelID int64,
) {
	record, ok := s.cargoLoadStorage(w, ctx, group, account, panelID, "clear")
	if !ok {
		return
	}
	removed := make([]cargoRemovedPayload, 0, len(record.Slots))
	skipped := int64(0)
	for _, key := range cargoSortedKeys(record.Slots) {
		listType, slot, parseErr := parseItemSlotKey(key)
		if parseErr != nil {
			// CARGO-D2：解析不出来的键保留（不删看不懂的东西），如实计入 skipped。
			skipped++
			continue
		}
		if int(listType) != cargoListType {
			continue
		}
		stack := record.Slots[key]
		removed = append(removed, cargoRemovedPayload{
			Slot:       int(slot),
			Key:        key,
			ItemID:     stack.ItemID,
			Name:       s.itemsName(stack.ItemID),
			Count:      stack.Count,
			RawDropped: len(stack.RawEntry) > 0,
		})
		delete(record.Slots, key)
	}
	if len(removed) == 0 {
		// 金库本来就是空的：如实回报 deleted=0，**不写库**（UpdatedAt 不动）。
		s.logEvent("gm-account-cargo-clear",
			"panel_id", panelID, "account_id", account.AccountID,
			"deleted", 0, "skipped", skipped, "remaining", len(record.Slots),
			"result", "noop")
		writeOK(w, map[string]any{
			"accountId": account.PanelID,
			"listType":  cargoListType,
			"deleted":   0,
			"skipped":   skipped,
			"remaining": len(record.Slots),
			"removed":   []cargoRemovedPayload{},
		})
		return
	}
	record.UpdatedAt = time.Now().UTC()
	if err := group.AccountInventory.Save(ctx, record); err != nil {
		s.logEvent("gm-account-cargo-clear-failed",
			"panel_id", panelID, "account_id", account.AccountID,
			"deleted", len(removed), "reason", "write_failed",
			"error", err.Error(), "result", "failed")
		writeFailure(w, "清空账号金库失败: "+err.Error())
		return
	}
	s.logEvent("gm-account-cargo-clear",
		"panel_id", panelID, "account_id", account.AccountID,
		"deleted", len(removed), "skipped", skipped, "remaining", len(record.Slots),
		"removed_slots", cargoRemovedSlotList(removed), "result", "applied")
	writeOK(w, map[string]any{
		"accountId": account.PanelID,
		"listType":  cargoListType,
		"deleted":   len(removed),
		"skipped":   skipped,
		"remaining": len(record.Slots),
		"removed":   removed,
	})
}

// ---------------------------------------------------------------------------
// 删除一格：POST /api/accounts/{id}/cargo/delete
// ---------------------------------------------------------------------------

func (s *Server) cargoApplyDelete(
	w http.ResponseWriter,
	ctx context.Context,
	group dnfrepo.Group,
	account webAccount,
	panelID int64,
	slot int,
) {
	slotValue := int16(slot)
	key := itemSlotKey(byte(cargoListType), slotValue)
	record, ok := s.cargoLoadStorage(w, ctx, group, account, panelID, "delete")
	if !ok {
		return
	}
	stack, found := record.Slots[key]
	if !found || stack.Count <= 0 {
		// 上游 GmService.Accounts.cs:367-369：删不掉就失败（"该槽位没有物品"）。
		s.logEvent("gm-account-cargo-rejected",
			"action", "delete", "panel_id", panelID, "account_id", account.AccountID,
			"slot", slot, "key", key, "reason", "slot_not_found", "result", "rejected")
		writeFailure(w, fmt.Sprintf("账号金库槽位 %d 没有物品（未做任何写入）。", slot))
		return
	}
	removed := cargoRemovedPayload{
		Slot:       slot,
		Key:        key,
		ItemID:     stack.ItemID,
		Name:       s.itemsName(stack.ItemID),
		Count:      stack.Count,
		RawDropped: len(stack.RawEntry) > 0,
	}
	delete(record.Slots, key)
	record.UpdatedAt = time.Now().UTC()
	if err := group.AccountInventory.Save(ctx, record); err != nil {
		s.logEvent("gm-account-cargo-delete-failed",
			"panel_id", panelID, "account_id", account.AccountID,
			"slot", slot, "item_id", removed.ItemID, "count", removed.Count,
			"reason", "write_failed", "error", err.Error(), "result", "failed")
		writeFailure(w, "删除账号金库物品失败: "+err.Error())
		return
	}
	s.logEvent("gm-account-cargo-delete",
		"panel_id", panelID, "account_id", account.AccountID,
		"slot", slot, "key", key, "item_id", removed.ItemID, "name", removed.Name,
		"count", removed.Count, "remaining", len(record.Slots), "result", "applied")
	writeOK(w, map[string]any{
		"accountId": account.PanelID,
		"listType":  cargoListType,
		"slot":      slot,
		"key":       key,
		"itemId":    removed.ItemID,
		"name":      removed.Name,
		"count":     removed.Count,
		"deleted":   removed.Count,
		"remaining": len(record.Slots),
		"removed":   removed,
	})
}

// cargoLoadStorage 读账号共享背包记录（三条写路由里 clear/delete 都要它）。
//
// **缺记录 = 明确失败**（CARGO-D1），与 api_items_delete.go:641-645 的 D3 同口径：
// 账号共享背包这一行不存在时，"清空 0 件"是一句可能骗人的话。
func (s *Server) cargoLoadStorage(
	w http.ResponseWriter,
	ctx context.Context,
	group dnfrepo.Group,
	account webAccount,
	panelID int64,
	action string,
) (dnfrepo.AccountInventoryRecord, bool) {
	if group.AccountInventory == nil {
		s.logEvent("gm-account-cargo-rejected",
			"action", action, "panel_id", panelID, "account_id", account.AccountID,
			"reason", "account_inventory_repository_missing", "result", "rejected")
		writeFailure(w, cargoErrInventoryRepoUnavailable.Error())
		return dnfrepo.AccountInventoryRecord{}, false
	}
	record, found, err := group.AccountInventory.Load(ctx, account.AccountID)
	if err != nil {
		s.logEvent("gm-account-cargo-rejected",
			"action", action, "panel_id", panelID, "account_id", account.AccountID,
			"reason", "account_inventory_load_failed", "error", err.Error(), "result", "rejected")
		writeFailure(w, "读取账号金库失败: "+err.Error())
		return dnfrepo.AccountInventoryRecord{}, false
	}
	if !found {
		s.logEvent("gm-account-cargo-rejected",
			"action", action, "panel_id", panelID, "account_id", account.AccountID,
			"reason", "account_inventory_missing", "result", "rejected")
		// 文案说清"读不到"与"已清空"不是一回事（CARGO-D1）。
		writeFailure(w, cargoErrStorageMissing.Error()+
			"；读不到记录不等于金库是空的，本接口不会静默当成已清空（未做任何写入）。")
		return dnfrepo.AccountInventoryRecord{}, false
	}
	record = dnfrepo.CloneAccountInventory(record)
	if record.Slots == nil {
		record.Slots = make(map[string]dnfrepo.ItemStack)
	}
	if strings.TrimSpace(record.AccountID) == "" {
		record.AccountID = account.AccountID
	}
	return record, true
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// cargoMetadataInt 与 cargo/owner.go:463-473 的 metadataInt 同口径：
// 缺键 / 解析失败都按 0（不是错误），因为"没有这一格"与"写着 0 格"在金库语义上等价。
func cargoMetadataInt(metadata map[string]string, key string) int64 {
	raw := strings.TrimSpace(metadata[key])
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// cargoMetadataBool 与 cargo/owner.go:475-482 的 metadataBool 逐字相同
// （1/true/yes/on 都算真；其余为假）。
func cargoMetadataBool(metadata map[string]string, key string) bool {
	switch strings.ToLower(strings.TrimSpace(metadata[key])) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// cargoSortedKeys 让响应与审计里的顺序稳定（Go map 是随机序），
// 与 api_items_delete.go:987-1000 的"排序是为了输出可读"同一意图。
func cargoSortedKeys(items map[string]dnfrepo.ItemStack) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cargoRemovedSlotList(removed []cargoRemovedPayload) string {
	slots := make([]string, 0, len(removed))
	for _, entry := range removed {
		slots = append(slots, strconv.Itoa(entry.Slot))
	}
	return strings.Join(slots, ",")
}
