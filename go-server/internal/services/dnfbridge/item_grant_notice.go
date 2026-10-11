package dnfbridge

// 本文件实现「道具入包后客户端该收到哪种 item-list 通知」这一条形状开关。
//
// 现场缺陷（操作者清单第 ⑥ 条，诊断见 `_dist/item-grant-notice-diagnosis.md`）：
// 开（徽章等）礼盒之后奖励落在哪个分页，客户端**不点亮那个分页**，玩家不知道东西进了哪一页；
// A21 在完全同一件事上会点亮。客户端没有"分页高亮"专用包，高亮是它对**逐槽 item 更新**
// （class0/msg 14 = 0x000E）的反应；而本仓这条链过去一律回放**整容器**
// （class0/msg 13 = 0x000D，"destroys and recreates the item objects"），整容器重发
// 没有"这是新增的一行"这个语义。
//
// 三处奖励发放路径（本次修法的对象）：
//  1. op160 礼盒（`gameplay_module_booster.go`）：源槽 op14 + 每个 ChangedList 整容器；
//  2. op208/op1128 魔盒（packageitem owner → `PostActionRefreshSelectedItemContainers`
//     → `game_aligned.go`）：默认档 5 个整容器；
//  3. op518 商城礼包（`cera_package.go`）：每个 ChangedListType 整容器。
//
// 开关（默认 = 修好的口径，符合本仓"新口径为默认、旧口径为显式回退值"的惯例）：
//
//	DNFBRIDGE_ITEM_GRANT_NOTICE=delta|containers
//	    delta（默认，含未设置/空/未知取值）= 只对**本次真正写过的 (list, slot)** 发
//	    class0/msg14 逐槽增量，奖励容器不再回放整容器；
//	    containers = 一键回退，逐字节回到加这个开关之前的线格式。
//
// **两条硬例外（有意为之，不随开关翻转）**：
//   - list 7（宠物栏）**绝不发 op14 新建行**：现场实测（角色 3 领宠物更名卡，list 7 slot 191）
//     客户端在库存界面模块 0x23082D3 直接闪退。本文件把 list 7 的写入放进 `Delta`，
//     由 `sendSelectedIncrementalItemSlotRefreshes` 既有的一段改期逻辑（1500 ms 后整容器，
//     `current_item_list_send.go:222-230/265-326`）承担 —— 即"仍然走整容器"，只是延迟发。
//   - list 38（公会勋章/守护珠）**首刀仍走整容器**：`inventory/handler.go:877-883` 明确
//     记过"list 38 与 list 7 一样不能走 list 0/1/2 的 op14 增量行"。本文件把它放进
//     `ContainerLists`，由调用方按既有整容器路径重发。
//
// 与既有开关的关系：`DNFBRIDGE_BOX_OPEN_CONTAINER_REFRESH`（`box_open_container_refresh.go`）
// 决定"哪些容器要重发"，本开关决定"用什么形状重发"。delta 档下例外容器集合由本次发放的
// 真实写入推导（严格强于 changed_only 的解析器记录），所以 delta 档不再经过那个开关；
// containers 档下它逐字节照旧。

import (
	"context"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// errItemGrantNoticeSnapshotUnavailable 表示"这次开盒没有可用的前后快照"：调用方必须
// 回落到今天的整容器回放，绝不按猜测少发容器。
var errItemGrantNoticeSnapshotUnavailable = errors.New("dnfbridge item grant notice requires a character inventory record")

const (
	itemGrantNoticeEnv        = "DNFBRIDGE_ITEM_GRANT_NOTICE"
	itemGrantNoticeDelta      = "delta"
	itemGrantNoticeContainers = "containers"
)

// normalizeItemGrantNoticeMode 归一化 DNFBRIDGE_ITEM_GRANT_NOTICE。
// 只有**显式**写 containers（及其回退别名）才回到整容器档；其余取值（含未设置、空、
// 无法识别的值）都是新的 delta 档 —— "零值即修复档"，与 `dungeonHellRequestByteUsesFlag1`
// 同一约定。别名集合照 equip_move_refresh.go:130-137 的写法。
func normalizeItemGrantNoticeMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case itemGrantNoticeContainers, "container", "containers_only", "full", "legacy", "current", "off", "0", "false", "no":
		return itemGrantNoticeContainers
	default:
		return itemGrantNoticeDelta
	}
}

// itemGrantNoticeMode 运行时读取开关（不经过 options.go：本仓已有
// `dungeonHellRequestByteEnv` / `dungeonEntryRequiredItemReasonEnv` 的 os.Getenv 先例）。
func itemGrantNoticeMode() string {
	return normalizeItemGrantNoticeMode(os.Getenv(itemGrantNoticeEnv))
}

// itemGrantNoticeUsesDelta 报告当前是否处于修复档（默认）。
func itemGrantNoticeUsesDelta() bool {
	return itemGrantNoticeMode() == itemGrantNoticeDelta
}

// itemGrantNoticeListUsesContainerPath 报告某个容器在 delta 档下是否**必须**整容器回放。
// 首刀只有 list 38（公会勋章/守护珠）。list 7 不在这里：它的 op14 行由发送侧改期成
// 整容器（见文件头"两条硬例外"）。
func itemGrantNoticeListUsesContainerPath(listType byte) bool {
	return listType == currentGuildMedalInventoryListType
}

// itemGrantNoticePlan 是 delta 档下"这次发放用哪些包通知客户端"的划分。
type itemGrantNoticePlan struct {
	// Delta 是走 class0/0x000E 逐槽增量的 (list, slot)，按 (list, slot) 升序去重。
	// list 7 也在里面：发送侧会把它改期成 1500 ms 后的整容器。
	Delta []alignedcmd.ItemSlotRefresh
	// ContainerLists 是仍然走 class0/0x000D 整容器的例外容器（首刀只有 list 38），
	// 按 `currentSelectedItemListTypes` 的规范顺序排列。
	ContainerLists []byte
}

// buildItemGrantNoticePlan 把"本次真正写过的 (list, slot)"划分成逐槽增量与例外容器。
// 纯函数（不读环境变量、不碰仓储），所以档位语义可以单独做表驱动测试。
func buildItemGrantNoticePlan(writes []alignedcmd.ItemSlotRefresh) itemGrantNoticePlan {
	seen := make(map[string]struct{}, len(writes))
	delta := make([]alignedcmd.ItemSlotRefresh, 0, len(writes))
	containers := make(map[byte]struct{}, 1)
	for _, write := range writes {
		if write.SlotIndex < 0 {
			continue
		}
		key := itemGrantNoticeSlotKey(write.ListType, write.SlotIndex)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		if itemGrantNoticeListUsesContainerPath(write.ListType) {
			containers[write.ListType] = struct{}{}
			continue
		}
		delta = append(delta, write)
	}
	sort.Slice(delta, func(i, j int) bool {
		if delta[i].ListType != delta[j].ListType {
			return delta[i].ListType < delta[j].ListType
		}
		return delta[i].SlotIndex < delta[j].SlotIndex
	})
	return itemGrantNoticePlan{
		Delta:          delta,
		ContainerLists: orderCurrentContainerListTypes(containers),
	}
}

func itemGrantNoticeSlotKey(listType byte, slot int16) string {
	return strconv.Itoa(int(listType)) + ":" + strconv.Itoa(int(slot))
}

// itemGrantNoticeListInSelectedSet 报告某容器是否属于今天会被点名的容器集合
// （`currentSelectedItemListTypes` = 0/1/2/7/38）。快照差分只在这个集合里产出通知，
// 保证 delta 档不会为今天根本不刷新的容器（list 3/11/12）多发一条包。
func itemGrantNoticeListInSelectedSet(listType byte) bool {
	for _, selected := range currentSelectedItemListTypes {
		if selected == listType {
			return true
		}
	}
	return false
}

// itemGrantSlotFingerprint 是"这一格在库里长什么样"的可比指纹。四个字段都是可比较类型，
// 所以指纹可以直接用 `==` 比较。RawEntry 与 Extra 一并参与比较：模块会就地重写行的
// 数量/到期投影，只比 item+count 会漏掉这类"同一件道具、同一数量、不同行字节"的写入。
type itemGrantSlotFingerprint struct {
	itemID   int64
	count    int64
	rawEntry string
	extra    string
}

// itemGrantInventorySnapshot 是一次命令**之前**的槽位指纹快照。op208/op1128 的写入发生在
// packageitem 的事务里（`internal/modules/dnf/**` 是本次改动的禁区），bridge 拿不到
// "写了哪些键"的返回值，所以用"提交前后各读一次仓储做差分"来得到**精确的** (list, slot)
// 集合 —— 差分只报告真的变了的行，不会猜到别的格。
type itemGrantInventorySnapshot struct {
	characterID string
	accountID   string
	slots       map[string]itemGrantSlotFingerprint
	accountSlot map[string]itemGrantSlotFingerprint
}

// isMagicBoxGrantOpcode 报告这条 opcode 是不是"开盒子"（op208 单开 / op1128 连开）。
// op160 礼盒与 op518 商城礼包走 bridge 原生路径，它们的 (list, slot) 由 owner 自己给出，
// 不需要快照差分。
func isMagicBoxGrantOpcode(opcode dnfenum.CmdPacket) bool {
	return opcode == dnfenum.CmdPacketUseRandomboxItem || opcode == dnfenum.CmdPacketUseRandomboxItemExpand
}

// armItemGrantSnapshot 在把 op208/op1128 交给 packageitem 之前读一次"发放前"的槽位指纹。
// 只有 delta 档 + 开盒子 opcode 才装快照；containers 档**一次仓储都不多读**，
// 逐字节保持今天的路径。返回 nil 表示"这次不接管形状"，调用方必须回落到今天的整容器回放。
func (s *Service) armItemGrantSnapshot(
	session *gameSession,
	repos dnfrepo.Group,
	opcode dnfenum.CmdPacket,
) *itemGrantInventorySnapshot {
	if s == nil || session == nil {
		return nil
	}
	if !itemGrantNoticeUsesDelta() || !isMagicBoxGrantOpcode(opcode) {
		return nil
	}
	// 先复位：上一次命令留下的例外容器集合绝不能泄漏到本次的后置动作里。
	session.beginItemGrantNotice()
	if session.selectedCharacterID == 0 || repos.Inventory == nil {
		return nil
	}
	snapshot, err := s.loadItemGrantSnapshot(context.Background(), session, repos)
	if err != nil {
		s.logPacketEvent("game-item-grant-notice-snapshot-unavailable",
			"conn_id", sessionConnID(session),
			"char_id", session.selectedCharacterID,
			"reason", "pre_command_inventory_snapshot_failed",
			"fallback", "replay_all_selected_containers",
			"switch", itemGrantNoticeEnv,
			"mode", itemGrantNoticeDelta,
			"err", err)
		return nil
	}
	return snapshot
}

// applyItemGrantSnapshot 在模块提交之后算出差分，把逐槽增量填进
// `result.ItemSlotRefreshes`（bridge 在 `sendAlignedUpperResponses` 里无条件发成
// class0/msg14），并把仍需整容器的例外容器记到会话上，供后置动作读取。
//
// 快照/差分不可用时**什么都不做**：调用方随即回落到今天的整容器回放。少发一个容器的代价是
// 客户端投影过期，所以这里只允许"精确"或"今天的全量"，绝不猜。
func (s *Service) applyItemGrantSnapshot(
	session *gameSession,
	repos dnfrepo.Group,
	snapshot *itemGrantInventorySnapshot,
	result *alignedcmd.Result,
) {
	if s == nil || session == nil || snapshot == nil || result == nil || !result.Handled {
		return
	}
	after, err := s.loadItemGrantSnapshot(context.Background(), session, repos)
	if err != nil {
		s.logPacketEvent("game-item-grant-notice-snapshot-unavailable",
			"conn_id", sessionConnID(session),
			"char_id", session.selectedCharacterID,
			"reason", "post_command_inventory_snapshot_failed",
			"fallback", "replay_all_selected_containers",
			"switch", itemGrantNoticeEnv,
			"mode", itemGrantNoticeDelta,
			"err", err)
		return
	}
	writes := snapshot.changedSlots(after)
	plan := buildItemGrantNoticePlan(writes)
	// 即使一条都没变也要置位：那代表"这次命令没有写任何需要通知的行"，
	// 后置动作因此必须**一条整容器都不发**，而不是退回五个。
	session.setItemGrantNoticePlan(plan.ContainerLists)
	result.ItemSlotRefreshes = append(result.ItemSlotRefreshes, plan.Delta...)
	s.logPacketEvent("game-item-grant-notice-plan",
		"conn_id", sessionConnID(session),
		"operation", result.Operation,
		"char_id", session.selectedCharacterID,
		"mode", itemGrantNoticeDelta,
		"delta_slots", currentItemSlotRefreshesSummary(plan.Delta),
		"delta_row_count", len(plan.Delta),
		"container_lists", plan.ContainerLists,
		"evidence", "item_grant_notice_delta_replaces_full_container_replay")
}

// changedSlots 报告"提交前后指纹不同"的 (list, slot)：新增或就地改写的行，以及被删掉的
// 行（源格用完、材料扣光）都要通知客户端，后者由发送侧投影成空行。
func (s *itemGrantInventorySnapshot) changedSlots(after *itemGrantInventorySnapshot) []alignedcmd.ItemSlotRefresh {
	if s == nil || after == nil {
		return nil
	}
	changes := make([]alignedcmd.ItemSlotRefresh, 0, 8)
	appendChanged := func(before, current map[string]itemGrantSlotFingerprint) {
		for key, fingerprint := range current {
			if previous, found := before[key]; found && previous == fingerprint {
				continue
			}
			listType, slot, ok := parseSceneInventorySlotKey(key)
			if !ok || !itemGrantNoticeListInSelectedSet(listType) {
				continue
			}
			changes = append(changes, alignedcmd.ItemSlotRefresh{ListType: listType, SlotIndex: slot})
		}
		for key := range before {
			if _, found := current[key]; found {
				continue
			}
			listType, slot, ok := parseSceneInventorySlotKey(key)
			if !ok || !itemGrantNoticeListInSelectedSet(listType) {
				continue
			}
			changes = append(changes, alignedcmd.ItemSlotRefresh{ListType: listType, SlotIndex: slot})
		}
	}
	appendChanged(s.slots, after.slots)
	appendChanged(s.accountSlot, after.accountSlot)
	return changes
}

// loadItemGrantSnapshot 读角色背包（含仓库键）与账号金库的槽位指纹。账号金库读不到时
// 只是少一份账号级槽（list 0 的 354..365），不因此让开盒失败 —— 差分只用于"发什么形状"，
// 不参与任何写库判定。
func (s *Service) loadItemGrantSnapshot(
	ctx context.Context,
	session *gameSession,
	repos dnfrepo.Group,
) (*itemGrantInventorySnapshot, error) {
	if s == nil || session == nil || repos.Inventory == nil {
		return nil, errItemGrantNoticeSnapshotUnavailable
	}
	snapshotCtx, cancel := context.WithTimeout(ctx, currentRepositorySnapshotTimeout)
	defer cancel()
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	record, found, err := repos.Inventory.Load(snapshotCtx, characterID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errItemGrantNoticeSnapshotUnavailable
	}
	snapshot := &itemGrantInventorySnapshot{
		characterID: characterID,
		accountID:   strings.TrimSpace(s.accountIDForSession(session)),
		slots:       make(map[string]itemGrantSlotFingerprint, len(record.Slots)+len(record.Warehouse)),
		accountSlot: map[string]itemGrantSlotFingerprint{},
	}
	for key, stack := range record.Slots {
		snapshot.slots[key] = itemGrantSlotFingerprintOf(stack)
	}
	for key, stack := range record.Warehouse {
		snapshot.slots[key] = itemGrantSlotFingerprintOf(stack)
	}
	if repos.AccountInventory == nil || snapshot.accountID == "" {
		return snapshot, nil
	}
	account, accountFound, accountErr := repos.AccountInventory.Load(snapshotCtx, snapshot.accountID)
	if accountErr != nil || !accountFound {
		return snapshot, nil
	}
	for key, stack := range account.Slots {
		snapshot.accountSlot[key] = itemGrantSlotFingerprintOf(stack)
	}
	return snapshot, nil
}

func itemGrantSlotFingerprintOf(stack dnfrepo.ItemStack) itemGrantSlotFingerprint {
	return itemGrantSlotFingerprint{
		itemID:   stack.ItemID,
		count:    stack.Count,
		rawEntry: string(stack.RawEntry),
		extra:    itemGrantExtraFingerprint(stack.Extra),
	}
}

// itemGrantExtraFingerprint 把 Extra 归一化成稳定的字符串（键排序），只用于比较。
func itemGrantExtraFingerprint(extra map[string]string) string {
	if len(extra) == 0 {
		return ""
	}
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(extra[key])
		builder.WriteByte('\n')
	}
	return builder.String()
}

// currentItemSlotRefreshesSummary 只用于日志：把 (list, slot) 列表写成 "0:75,1:0"。
func currentItemSlotRefreshesSummary(refreshes []alignedcmd.ItemSlotRefresh) string {
	if len(refreshes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(refreshes))
	for _, refresh := range refreshes {
		parts = append(parts, itemGrantNoticeSlotKey(refresh.ListType, refresh.SlotIndex))
	}
	return strings.Join(parts, ",")
}

// beginItemGrantNotice 复位"本次命令的发放通知计划"。**必须在模块被调用之前**做，
// 否则上一次开盒的例外容器集合会在本次失败时留下残影（与
// `beginBoxOpenContainerRecord` 同一约定）。记录只在同一条连接的处理 goroutine 内
// 串行读写，因此不需要额外加锁。
func (s *gameSession) beginItemGrantNotice() {
	if s == nil {
		return
	}
	s.itemGrantNoticeArmed = false
	s.itemGrantNoticeContainers = s.itemGrantNoticeContainers[:0]
}

// setItemGrantNoticePlan 记录本次命令在 delta 档下仍需整容器回放的例外容器
// （首刀只有 list 38；可以为空 = 一条整容器都不发）。置位本身就代表"形状已经由 delta 接管"。
func (s *gameSession) setItemGrantNoticePlan(containerLists []byte) {
	if s == nil {
		return
	}
	s.itemGrantNoticeArmed = true
	s.itemGrantNoticeContainers = append(s.itemGrantNoticeContainers[:0], containerLists...)
}

// takeItemGrantNoticeContainers 读出并消费本次命令的例外容器集合。
// 第二个返回值为 false 表示"这次没有 delta 计划"（containers 档、不是开盒子、或快照不可用），
// 调用方必须走今天的整容器回放。
func (s *gameSession) takeItemGrantNoticeContainers() ([]byte, bool) {
	if s == nil || !s.itemGrantNoticeArmed {
		return nil, false
	}
	s.itemGrantNoticeArmed = false
	return append([]byte(nil), s.itemGrantNoticeContainers...), true
}
