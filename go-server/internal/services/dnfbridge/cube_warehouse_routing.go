package dnfbridge

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件落实运维的总规则：**不管从哪条路径获得小晶块/灵魂，都进晶块仓库**
// （账号级 list 0 固定格 354..365），而不是主背包的材料页（121..176）。
//
// ── 现场（2026-09-28/29，线上 packet_log.txt）─────────────────────────────
//
// 报障原文：「GM工具发放无色小晶体邮件 客户端领取后闪退 然后无色小晶块出现在
// 物品栏材料而不是晶块仓库」。
//
// 崩溃现场（连接被 RST = 客户端进程消失，不是服务端断线）：
//
//	196749  SEND msg_id=97  mailbox snapshot: attachment item 0x0bdd(3037) count 0x270f(9999)
//	196757  RECV type=95     op95 claim mails=[55]
//	196766  SEND msg_id=14   op14 row: slot=0x0082(130) item=0x0bdd(3037) amount=0x270f(9999)
//	196770  EVENT game-read-close conn_id=game-000011 error="read: connection reset by peer"
//	        t=2026-09-28T18:38:03.107Z（op14 之后 1.30 s）
//
// 落库现场（SQL，`dnf_inventory_items`）：
//
//	character_id=1 collection=slots entry_key=0:130 item_id=3037 item_count=16233
//	Extra: item_kind=stackable / pvf_path=stackable/material/cubepiece_clear.stk /
//	       stackable_type=[material]（**没有** source 键 = 邮件领取堆栈的指纹）
//
// 同一客户端对**晶块仓库格**的 op14 行一直正常：全日志数百条
// `… 6601 dd0b0000 …`（slot=0x0166=358, item=3037）的行，会话全部存活
// （例：conn game-000035 16:31:12/16:31:18，conn game-000009 的
// `quest 2710 reward` 行 19607）。⇒ 「把晶块写进晶块仓库格」这条下行形状是
// **已证实可用**的；「把晶块写进主背包材料页」才是崩的那条。
//
// ── 两条实现 ───────────────────────────────────────────────────────────
//
//	A. 领取期改道：`mailboxItemResolutionFromPVF` 对晶块/灵魂标
//	   `AccountWarehouseSlot`，mail 模块不写背包行、改调本文件注入的闭包并进账号
//	   仓库，op14 行落在 354..365（已被证实可用）。GM 面板发的东西**只走邮件**
//	   （gmtool.handleItemGrant 拒绝直写背包），所以这一处同时覆盖"GM 发放"与
//	   "玩家领附件"。
//	B. 登录期清扫：`purgeCurrentInventoryCubes` 在选角背包引导**之前**，把角色
//	   背包/仓库里已有的晶块按数量并入账号晶块仓库并从背包移除（幂等）。它是
//	   **所有其余路径的统一收口**：地面拾取、翻牌、开盒子/礼包、点券套餐、任务
//	   奖励、兑换、合成、交易……只要写进了背包，下一次登录就会被搬进晶块仓库。
//	   （同一手法见 epic_piece_grant.go 的存量碎片清扫。）
//
// ── 开关 ───────────────────────────────────────────────────────────────
//
// `DNFBRIDGE_CUBE_WAREHOUSE_ROUTING`（默认 = on，即修复后的行为）：
//
//	未设置/on/warehouse/enabled → A + B 都生效
//	legacy/off/0/false/disabled  → 逐字节回到改动前：晶块照旧进背包、不清扫
//
// 回退是一次 env 改动，不需要重新部署二进制。
const (
	cubeWarehouseRoutingEnv      = "DNFBRIDGE_CUBE_WAREHOUSE_ROUTING"
	cubeWarehouseRoutingValueOn  = "on"
	cubeWarehouseRoutingValueOff = "legacy"
)

// cubeWarehouseRoutingMode 是 cubeWarehouseRoutingEnv 的归一化取值。
type cubeWarehouseRoutingMode int

const (
	// cubeWarehouseRoutingOn 默认档（零值）：领取期改道 + 登录期清扫。
	cubeWarehouseRoutingOn cubeWarehouseRoutingMode = iota
	// cubeWarehouseRoutingLegacy 回退档：晶块照旧进背包，不清扫。
	cubeWarehouseRoutingLegacy
)

func (mode cubeWarehouseRoutingMode) String() string {
	if mode == cubeWarehouseRoutingLegacy {
		return cubeWarehouseRoutingValueOff
	}
	return cubeWarehouseRoutingValueOn
}

func (mode cubeWarehouseRoutingMode) enabled() bool {
	return mode != cubeWarehouseRoutingLegacy
}

// readCubeWarehouseRoutingMode 解析 DNFBRIDGE_CUBE_WAREHOUSE_ROUTING。
// 默认 on：空/未设置/非法值都按 on；只有显式写回退字面量才回到旧行为。
func readCubeWarehouseRoutingMode(value string) cubeWarehouseRoutingMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case cubeWarehouseRoutingValueOff, "off", "0", "false", "disabled":
		return cubeWarehouseRoutingLegacy
	default:
		return cubeWarehouseRoutingOn
	}
}

// cubeWarehouseRouting 返回当前进程的归一化档位（每次读 env，便于一次改动生效）。
func (s *Service) cubeWarehouseRouting() cubeWarehouseRoutingMode {
	return readCubeWarehouseRoutingMode(envString(cubeWarehouseRoutingEnv, ""))
}

// cubeWarehouseRoutingEnabled 是"领取期改道 + 登录期清扫"的唯一判定。
func (s *Service) cubeWarehouseRoutingEnabled() bool {
	return s != nil && s.cubeWarehouseRouting().enabled()
}

// ─────────────────── A. 领取期：账号晶块仓库入账闭包 ───────────────────

// alignedWarehouseCreditorForCommand 只在"领取邮件附件"（op95）这条会写背包的
// 路径上返回账号晶块仓库的入账闭包；其它命令一律返回 nil，mail 模块因此对它们
// 保持逐字节不变的行为。
//
// 没有 `WithinAccountCharacterItems` 可用是有意的：op95 已经在
// `WithinMailboxAssets` 事务里（它锁着同一角色的 `dnf_inventory_items` 行），
// 再开一个覆盖同一批行的事务会互相等锁。所以这里用账号仓库自己的 Store 做一次
// 读改写，并把已知窗口写进注释（与史诗碎片账本同一取舍：宁可可能重复，绝不丢失）。
func (s *Service) alignedWarehouseCreditorForCommand(
	opcode dnfenum.CmdPacket,
	accountID string,
) alignedcmd.MailboxWarehouseCreditor {
	if s == nil || opcode != dnfenum.CmdPacketMailboxExtractItem {
		return nil
	}
	if !s.cubeWarehouseRoutingEnabled() {
		return nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.AccountInventory == nil {
		return nil
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil
	}
	return func(ctx context.Context, slot int16, itemID uint32, count int64, extra map[string]string) (int64, error) {
		fixed, mapped := dnfrepo.AccountWarehouseSlotForItem(int64(itemID))
		if !mapped || fixed != slot || !dnfrepo.IsAccountSharedInventorySlot(dnfrepo.MainInventoryListType, slot) {
			return 0, fmt.Errorf("dnf cube warehouse credit rejected slot=%d item=%d mapped=%v", slot, itemID, mapped)
		}
		if count <= 0 {
			return 0, fmt.Errorf("dnf cube warehouse credit rejected slot=%d item=%d count=%d", slot, itemID, count)
		}
		return s.creditCurrentAccountWarehouseStack(ctx, repositories.AccountInventory, accountID, slot, itemID, count, extra)
	}
}

// creditCurrentAccountWarehouseStack 把 count 并入账号仓库的固定格并返回该格的
// 绝对数量。身份 Extra 只在新建堆栈时写（与 currentGrantDisjointWarehouseStack
// 同一口径）；栈顶已有别的东西时**拒绝**而不是挤掉它。
func (s *Service) creditCurrentAccountWarehouseStack(
	ctx context.Context,
	repository dnfrepo.AccountInventoryRepository,
	accountID string,
	slot int16,
	itemID uint32,
	count int64,
	extra map[string]string,
) (int64, error) {
	if repository == nil {
		return 0, fmt.Errorf("dnf cube warehouse credit requires account inventory repository")
	}
	record, found, err := repository.Load(ctx, accountID)
	if err != nil {
		return 0, err
	}
	if !found {
		record = dnfrepo.AccountInventoryRecord{AccountID: accountID, Slots: make(map[string]dnfrepo.ItemStack)}
	} else {
		record = dnfrepo.CloneAccountInventory(record)
	}
	if record.Slots == nil {
		record.Slots = make(map[string]dnfrepo.ItemStack)
	}
	record.AccountID = accountID
	key := dnfrepo.AccountSharedInventorySlotKey(slot)
	stack, exists := record.Slots[key]
	if exists && stack.ItemID > 0 && stack.ItemID != int64(itemID) {
		return 0, fmt.Errorf("dnf cube warehouse slot %d already holds item %d", slot, stack.ItemID)
	}
	limit := int64(0)
	if definition, resolveErr := s.currentCubeWarehouseDefinition(itemID); resolveErr == nil {
		limit = definition.StackLimit
	} else {
		return 0, resolveErr
	}
	if limit > 0 && stack.Count >= limit {
		return stack.Count, fmt.Errorf("dnf cube warehouse slot %d is full (%d)", slot, stack.Count)
	}
	room := count
	if limit > 0 {
		if space := limit - stack.Count; space < room {
			room = space
		}
	}
	if room <= 0 {
		return stack.Count, fmt.Errorf("dnf cube warehouse slot %d has no room", slot)
	}
	if !exists || stack.ItemID <= 0 {
		stack = dnfrepo.ItemStack{ItemID: int64(itemID), Extra: map[string]string{"source": "cube_warehouse_routing"}}
	}
	if stack.Extra == nil {
		stack.Extra = make(map[string]string, len(extra)+1)
	}
	if _, exists := stack.Extra["source"]; !exists {
		stack.Extra["source"] = "cube_warehouse_routing"
	}
	for name, value := range extra {
		if value == "" {
			continue
		}
		if _, exists := stack.Extra[name]; !exists {
			stack.Extra[name] = value
		}
	}
	if limit > 0 {
		stack.Extra["stack_limit"] = strconv.FormatInt(limit, 10)
	}
	stack.Count += room
	// 与 disjoint 路径共用同一条 0x77 行编码器（客户端 op14 行必须是这个形状）。
	entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, stack)
	stack.RawEntry = append([]byte(nil), entry.data[:]...)
	record.Slots[key] = stack
	if err := repository.Save(ctx, record); err != nil {
		return 0, err
	}
	return stack.Count, nil
}

// currentCubeWarehouseDefinition 取一件晶块/灵魂的运行时 PVF 定义（栈上限等）。
func (s *Service) currentCubeWarehouseDefinition(itemID uint32) (dungeonDropItemDefinition, error) {
	if s == nil || itemID == 0 {
		return dungeonDropItemDefinition{}, fmt.Errorf("dnf cube warehouse credit requires an item id")
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return dungeonDropItemDefinition{}, err
	}
	definition, err := catalog.ResolveItem(itemID)
	if err != nil {
		return dungeonDropItemDefinition{}, err
	}
	return definition, nil
}

// ─────────────────── B. 登录期：存量晶块清扫 ───────────────────

// purgeCurrentInventoryCubes 把角色背包（含个人仓库）里已有的小晶块/灵魂按数量
// 并入账号晶块仓库的固定格，并从背包移除。它是**幂等**的：第二次跑没有命中就一个
// 字节都不写。
//
// 为什么必须做：运维要的是"不管如何获得小晶块都进入晶块仓库"。逐条改发放路径
// （拾取/翻牌/盒子/礼包/任务/兑换/交易……）永远会漏；把收口放在"下一次进游戏之前"
// 则对任何路径都成立，而且和 epic_piece_grant.go 的存量碎片清扫是同一手法。
//
// 落位规则与领取期完全一致（同一个 dnfrepo.AccountWarehouseSlotForItem 映射、
// 同一把 0x77 行编码器）；栈顶被别的道具占着、或仓库格已满时，**放不下的部分留在
// 背包里**（绝不删除放不下的数量）。
//
// 返回 (清扫的行数, 错误)。失败一律不阻断登录（背包里多一堆晶块是可修的数据问题，
// 登不进去是可用性问题）。
func (s *Service) purgeCurrentInventoryCubes(
	ctx context.Context,
	session *gameSession,
	source string,
) (int, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return 0, nil
	}
	if !s.cubeWarehouseRoutingEnabled() {
		s.logGameEvent(session, "game-cube-warehouse-purge-skipped",
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "cube_warehouse_routing_disabled",
			"env", cubeWarehouseRoutingEnv)
		return 0, nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.AccountInventory == nil || repositories.AccountItems == nil {
		s.logGameEvent(session, "game-cube-warehouse-purge-skipped",
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "repository_group_unavailable",
			"account_inventory", ok && repositories.AccountInventory != nil,
			"account_items_uow", ok && repositories.AccountItems != nil)
		return 0, nil
	}
	// 账号未绑定 ⇒ fail-closed：晶块留在背包，也不能并入一个错误的账号仓库。
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		s.logGameEvent(session, "game-cube-warehouse-purge-skipped",
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "account_id_missing")
		return 0, nil
	}
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	catalog, catalogErr := s.currentPVFItemCatalog()
	if catalogErr != nil || catalog == nil {
		s.logGameEvent(session, "game-cube-warehouse-purge-skipped",
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "pvf_catalog_unavailable",
			"error", catalogErr)
		return 0, nil
	}
	purged := 0
	err := repositories.AccountItems.WithinAccountCharacterItems(ctx, accountID, characterID, func(
		accounts dnfrepo.AccountInventoryRepository,
		inventory dnfrepo.InventoryRepository,
	) error {
		record, found, loadErr := inventory.Load(ctx, characterID)
		if loadErr != nil {
			return loadErr
		}
		if !found || (len(record.Slots) == 0 && len(record.Warehouse) == 0) {
			return nil
		}
		updated := dnfrepo.CloneInventory(record)
		account, accountFound, accountErr := accounts.Load(ctx, accountID)
		if accountErr != nil {
			return accountErr
		}
		if !accountFound {
			account = dnfrepo.AccountInventoryRecord{AccountID: accountID, Slots: make(map[string]dnfrepo.ItemStack)}
		} else {
			account = dnfrepo.CloneAccountInventory(account)
		}
		if account.Slots == nil {
			account.Slots = make(map[string]dnfrepo.ItemStack)
		}
		account.AccountID = accountID
		changed := false
		for _, container := range []struct {
			name  string
			slots map[string]dnfrepo.ItemStack
		}{
			{name: "slots", slots: updated.Slots},
			{name: "warehouse", slots: updated.Warehouse},
		} {
			if len(container.slots) == 0 {
				continue
			}
			keys := make([]string, 0, len(container.slots))
			for key := range container.slots {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				stack := container.slots[key]
				if stack.ItemID <= 0 || stack.Count <= 0 {
					continue
				}
				slot, mapped := dnfrepo.AccountWarehouseSlotForItem(stack.ItemID)
				if !mapped {
					continue
				}
				definition, resolveErr := catalog.ResolveItem(uint32(stack.ItemID))
				if resolveErr != nil {
					return resolveErr
				}
				placed, placeErr := s.mergeCurrentAccountWarehouseStack(&account, definition, slot, stack.Count, stack.Extra)
				if placeErr != nil {
					return placeErr
				}
				if placed <= 0 {
					// 仓库格被占或不接受这件道具：留在背包（绝不删除）。
					continue
				}
				changed = true
				purged++
				s.logGameEvent(session, "game-cube-warehouse-purge-row",
					"source", source,
					"char_id", session.selectedCharacterID,
					"account_id", accountID,
					"container", container.name,
					"entry_key", key,
					"slot", slot,
					"item_id", stack.ItemID,
					"count", placed,
					"balance", account.Slots[dnfrepo.AccountSharedInventorySlotKey(slot)].Count)
				if placed >= stack.Count {
					delete(container.slots, key)
					continue
				}
				remainder := stack
				remainder.Count -= placed
				remainder.RawEntry = nil
				container.slots[key] = remainder
			}
		}
		if !changed {
			return nil
		}
		if err := dnfrepo.SaveInventoryFields(ctx, inventory, updated, dnfrepo.InventoryFieldSlots); err != nil {
			return err
		}
		return accounts.Save(ctx, account)
	})
	if err != nil {
		return 0, err
	}
	return purged, nil
}

// mergeCurrentAccountWarehouseStack 把 count 并进（内存里的）账号固定格，返回真正
// 并入的数量。它只在**同一个** AccountCharacterItems 事务里被调用，所以不做任何
// 仓储 I/O——落库由调用方一次完成。
//
// 与 creditCurrentAccountWarehouseStack 的差别只是"谁负责 Save"；两者的落位口径
// （固定格映射、栈顶占用即拒绝、栈上限、身份 Extra 只在新栈上写）逐条一致。
func (s *Service) mergeCurrentAccountWarehouseStack(
	account *dnfrepo.AccountInventoryRecord,
	definition dungeonDropItemDefinition,
	slot int16,
	count int64,
	extra map[string]string,
) (int64, error) {
	if account == nil {
		return 0, fmt.Errorf("dnf cube warehouse merge requires an account record")
	}
	if count <= 0 {
		return 0, nil
	}
	if int64(definition.ItemID) != 0 {
		fixed, mapped := dnfrepo.AccountWarehouseSlotForItem(int64(definition.ItemID))
		if !mapped || fixed != slot {
			return 0, fmt.Errorf("dnf cube warehouse merge slot=%d item=%d mapped=%v", slot, definition.ItemID, mapped)
		}
	}
	key := dnfrepo.AccountSharedInventorySlotKey(slot)
	stack, exists := account.Slots[key]
	if exists && stack.ItemID > 0 && stack.ItemID != int64(definition.ItemID) {
		return 0, nil
	}
	room := count
	if definition.StackLimit > 0 {
		if space := definition.StackLimit - stack.Count; space < room {
			room = space
		}
	}
	if room <= 0 {
		return 0, nil
	}
	if !exists || stack.ItemID <= 0 {
		stack = dnfrepo.ItemStack{ItemID: int64(definition.ItemID), Extra: map[string]string{"source": "cube_warehouse_routing"}}
	}
	if stack.Extra == nil {
		stack.Extra = make(map[string]string, len(extra)+1)
	}
	if _, exists := stack.Extra["source"]; !exists {
		stack.Extra["source"] = "cube_warehouse_routing"
	}
	for name, value := range extra {
		if value == "" {
			continue
		}
		if _, exists := stack.Extra[name]; !exists {
			stack.Extra[name] = value
		}
	}
	if definition.Kind != "" {
		if _, exists := stack.Extra["item_kind"]; !exists {
			stack.Extra["item_kind"] = string(definition.Kind)
		}
	}
	if pvfPath := strings.TrimSpace(definition.PVFPath); pvfPath != "" {
		if _, exists := stack.Extra["pvf_path"]; !exists {
			stack.Extra["pvf_path"] = pvfPath
		}
	}
	if stackableType := strings.TrimSpace(definition.StackableType); stackableType != "" {
		if _, exists := stack.Extra["stackable_type"]; !exists {
			stack.Extra["stackable_type"] = stackableType
		}
	}
	if definition.StackLimit > 0 {
		stack.Extra["stack_limit"] = strconv.FormatInt(definition.StackLimit, 10)
	}
	stack.Count += room
	entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, stack)
	stack.RawEntry = append([]byte(nil), entry.data[:]...)
	account.Slots[key] = stack
	return room, nil
}
