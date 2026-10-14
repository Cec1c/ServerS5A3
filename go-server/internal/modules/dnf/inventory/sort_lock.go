package inventory

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// sort_lock.go —— DOC-067「物品栏位置锁定」的持久化、整理跳过与搬运携带规则。
//
// 当前客户端证据（DNF.exe SHA256 8bf1d39df74c6ca6fdbea30a95e055398aad9729305c7ea510ce01af2141e387；
// 父代理离线取证 lock-client-evidence/conclusions.json + registration.json，本席复核解析链）：
//
//	C2S 713 `ENUM_CMDPACKET_REQUEST_ITEM_SORT_LOCK`   = 置 1（不是 toggle）
//	C2S 714 `ENUM_CMDPACKET_REQUEST_ITEM_SORT_UNLOCK` = 置 0
//	body = u8 list + u16 little-endian slot（客户端 0x32d3767/0x32d3746 写 u8 后写 u16）
//	class1 成功回包 = u8 1 + u8 list + u16 slot；失败 = u8 0 + u8 error
//	客户端收到 713 写 item+0x2aa=1、714 写 0（0x1faee80/0x1faeee0），所以回包必须用**同一 opcode**；
//	旧 C# 的 toggle + 714/715 口径在本客户端会把加锁翻成解锁，禁止照搬。
//
//	0x77 物品行的 0x60 字节 → item 结构 +0xaa → item+0x2aa（字节 0/1）：
//	解析器 0x1fd80e0 在 0x1fd824c 读 row+0x60、0x1fd8260 存栈槽、0x1fd8c38 写 struct+0xaa；
//	0x1d45fc5/0x1d45fcc 把它拷进 item+0x2aa。op13 处理器 0x2024cf0 与 op14 处理器 0x2025a90
//	都用同一个 0x77 行缓冲调用同一组行解析 helper（0x242d4a0 / 0x2436fe0），因此登录整表与增量行
//	都会把这一字节投影给客户端。
//
// 语义边界（照 A21 InventoryLockService/InventorySortService/InventoryMoveService 口径，90CN 已证部分）：
//   - 支持列表 = 主背包 / 时装 / 个人金库（与 checkSortListType 同一批，其余列表缺 owner 复用）；
//   - 主背包 0:354..0:365 是账号共享晶块/灵魂格，属于账号，不参与位置锁；
//   - 整理跳过锁定物品，并且不把别的物品排进被锁槽位；
//   - 搬运时：涉及穿戴栏、跨 list、主背包快捷槽 3..8 一律清锁；个人金库内部保留；
//     其余要求源槽与目标槽落在同一个整理分段（同 kind 同 list）才保留。
const (
	// SortLockExtraKey 是物品栈上的持久化键；只有 "1" 表示锁定。
	// 定义在 dnfrepo，因为穿戴 owner 也要按同一条规则清锁（equip 不能 import inventory）。
	SortLockExtraKey = dnfrepo.SortLockExtraKey
	// SortLockRowOffset 是 0x77 物品行里承载位置锁的字节（客户端读进 item+0x2aa）。
	SortLockRowOffset = 0x60
	// SortLockRowLocked / SortLockRowUnlocked 是该字节的两个取值。
	SortLockRowLocked   byte = 1
	SortLockRowUnlocked byte = 0

	sortLockValueLocked = "1"
)

var (
	ErrSortLockUnsupportedList  = errors.New("sort lock list type is not supported")
	ErrSortLockSlotEmpty        = errors.New("sort lock slot is empty")
	ErrSortLockAccountShared    = errors.New("sort lock slot is account shared")
	ErrSortLockForeignCharacter = errors.New("sort lock character is not owned by the account")
	ErrSortLockSlotMismatch     = errors.New("sort lock target slots do not match the movable items")
)

// IsSortLocked 报告该物品栈是否带位置锁。
func IsSortLocked(stack dnfrepo.ItemStack) bool {
	if stack.Extra == nil {
		return false
	}
	return strings.TrimSpace(stack.Extra[SortLockExtraKey]) == sortLockValueLocked
}

// SortLockRowValue 给出该物品栈要投影进 0x77 行 0x60 字节的值（显式 0/1）。
func SortLockRowValue(stack dnfrepo.ItemStack) byte {
	if IsSortLocked(stack) {
		return SortLockRowLocked
	}
	return SortLockRowUnlocked
}

// setSortLockStack 就地复制 Extra 后写入/清除位置锁键，避免改动调用方共享的 map。
func setSortLockStack(stack dnfrepo.ItemStack, locked bool) dnfrepo.ItemStack {
	if !locked {
		return clearSortLockStack(stack)
	}
	if IsSortLocked(stack) {
		return stack
	}
	next := make(map[string]string, len(stack.Extra)+1)
	for key, value := range stack.Extra {
		next[key] = value
	}
	next[SortLockExtraKey] = sortLockValueLocked
	stack.Extra = next
	return stack
}

// clearSortLockStack 去掉位置锁键；其余 Extra 键逐项保留。
func clearSortLockStack(stack dnfrepo.ItemStack) dnfrepo.ItemStack {
	if !IsSortLocked(stack) {
		return stack
	}
	next := make(map[string]string, len(stack.Extra))
	for key, value := range stack.Extra {
		if key == SortLockExtraKey {
			continue
		}
		next[key] = value
	}
	if len(next) == 0 {
		stack.Extra = nil
		return stack
	}
	stack.Extra = next
	return stack
}

// SortLockCanApply 报告某列表/槽位是否允许位置锁。
// 主背包 354..365 是账号共享晶块/灵魂格（dnfrepo.IsAccountSharedInventorySlot），
// 它的持久归属是账号而不是角色，所以拒绝加锁，也不会被整理碰到。
func SortLockCanApply(listType byte, slot int16) bool {
	if checkSortListType(listType) != nil {
		return false
	}
	if slot < 0 {
		return false
	}
	return !dnfrepo.IsAccountSharedInventorySlot(listType, slot)
}

// sortLockSlotKind 给出槽位所属的整理分段；空串 = 不在任何可整理分段里。
// 主背包分段与 sortSegment 的 category 一一对应（9..64 装备 / 65..120 消耗品 /
// 121..176 材料 / 177..232 任务 / 233..288 副职业材料），快捷槽 3..8 单独标记。
func sortLockSlotKind(listType byte, slot int16) string {
	if slot < 0 {
		return ""
	}
	switch listType {
	case listTypeMain:
		switch {
		case isMainItemQuickSlot(listType, slot):
			return "quick"
		case slot >= 9 && slot <= 64:
			return "equipment"
		case slot >= 65 && slot <= 120:
			return "consumable"
		case slot >= 121 && slot <= 176:
			return "material"
		case slot >= 177 && slot <= 232:
			return "quest"
		case slot >= 233 && slot <= 288:
			return "expert_job_material"
		default:
			return ""
		}
	case listTypeAvatar:
		if slot <= 209 {
			return "avatar"
		}
		return ""
	case listTypePersonalCargo:
		if slot <= 151 {
			return "cargo"
		}
		return ""
	default:
		return ""
	}
}

// sortLockSurvivesMove 是 A21 Game/Inventory/Services/InventoryMoveService.cs:794
// ShouldPreserveSortLock 的 90CN 版（列表/快捷槽边界用本仓已证常量）：
//
//	涉及穿戴栏（list 3）→ 不携带（穿戴栏不展示位置锁，穿/脱一律清锁）；
//	跨 list → 不携带；
//	个人金库内部 → 携带；
//	主背包快捷槽 3..8 → 不携带；
//	其余：源槽与目标槽必须落在同一个整理分段才携带（同 kind 同 list 保留）。
func sortLockSurvivesMove(sourceList byte, sourceSlot int16, destinationList byte, destinationSlot int16) bool {
	if sourceList == listTypeEquipment || destinationList == listTypeEquipment {
		return false
	}
	if sourceList != destinationList {
		return false
	}
	if sourceList == listTypePersonalCargo {
		return true
	}
	if isMainItemQuickSlot(sourceList, sourceSlot) || isMainItemQuickSlot(destinationList, destinationSlot) {
		return false
	}
	sourceKind := sortLockSlotKind(sourceList, sourceSlot)
	destinationKind := sortLockSlotKind(destinationList, destinationSlot)
	return sourceKind != "" && sourceKind == destinationKind
}

// applySortLockOnMove 把被搬运的物品副本按上面的规则落地：不携带时清掉位置锁。
func applySortLockOnMove(stack dnfrepo.ItemStack, sourceList byte, sourceSlot int16, destinationList byte, destinationSlot int16) dnfrepo.ItemStack {
	if !IsSortLocked(stack) {
		return stack
	}
	if sortLockSurvivesMove(sourceList, sourceSlot, destinationList, destinationSlot) {
		return stack
	}
	return clearSortLockStack(stack)
}

// SortLockRequest 是 C2S 713/714 的请求体：u8 list + u16 slot。
type SortLockRequest struct {
	ListType  byte
	SlotIndex int16
}

// DecodeSortLockRequest 解析 713/714 的 3 字节请求体。
// 客户端自身写体顺序是 u8 list + u16 slot（0x32d36d0 的 0x374f390/0x374f3c0），
// 所以长度不合法时直接拒绝，不猜测偏移。
func DecodeSortLockRequest(body []byte) (SortLockRequest, error) {
	if len(body) != 3 {
		return SortLockRequest{}, fmt.Errorf("invalid sort lock body length: got %d want 3", len(body))
	}
	return SortLockRequest{ListType: body[0], SlotIndex: int16(binary.LittleEndian.Uint16(body[1:3]))}, nil
}

// NewSortLockCommand 构造位置锁命令计划；Locked 由 opcode 决定（713=锁、714=解锁）。
func NewSortLockCommand(req alignedcmd.Request, parsed SortLockRequest, locked bool) Command {
	operation := "request_item_sort_unlock"
	if locked {
		operation = "request_item_sort_lock"
	}
	return Command{
		Operation:           operation,
		AccountID:           strings.TrimSpace(req.AccountID),
		SelectedCharacterID: req.SelectedCharacterID,
		SourceListType:      parsed.ListType,
		SourceSlotIndex:     parsed.SlotIndex,
		SortLock:            locked,
		SortLockRequested:   true,
		NeedsOwner:          "inventory owner + sort_lock persistence + same-opcode class1 ACK",
	}
}

// SortLockResult 描述一次位置锁写库结果；Changed=false 表示已是目标状态（幂等）。
type SortLockResult struct {
	CharacterID string
	ListType    byte
	SlotIndex   int16
	Locked      bool
	Changed     bool
}

// ApplySortLock 持久化一次 713/714：校验列表/槽位/物品归属后写入或清除 sort_lock。
// 已是目标状态时不写库（幂等）；写库失败原样返回错误，调用方不得回成功包。
func (o *Owner) ApplySortLock(ctx context.Context, cmd Command) (SortLockResult, error) {
	if o == nil || o.repo == nil {
		return SortLockResult{}, ErrOwnerUnavailable
	}
	if cmd.SelectedCharacterID == 0 {
		return SortLockResult{}, ErrCharacterRequired
	}
	if !cmd.SortLockRequested {
		return SortLockResult{}, fmt.Errorf("sort lock command missing request intent")
	}
	if err := checkSortListType(cmd.SourceListType); err != nil {
		return SortLockResult{}, fmt.Errorf("%w: %v", ErrSortLockUnsupportedList, err)
	}
	if dnfrepo.IsAccountSharedInventorySlot(cmd.SourceListType, cmd.SourceSlotIndex) {
		return SortLockResult{}, fmt.Errorf("%w: list=%d slot=%d", ErrSortLockAccountShared, cmd.SourceListType, cmd.SourceSlotIndex)
	}
	if !SortLockCanApply(cmd.SourceListType, cmd.SourceSlotIndex) {
		return SortLockResult{}, fmt.Errorf("%w: list=%d slot=%d", ErrSortLockUnsupportedList, cmd.SourceListType, cmd.SourceSlotIndex)
	}

	characterID := fmt.Sprintf("%d", cmd.SelectedCharacterID)
	if !o.inItemTransaction {
		var result SortLockResult
		err := o.withinInventoryTransaction(ctx, characterID, func(txOwner *Owner) error {
			var err error
			result, err = txOwner.ApplySortLock(ctx, cmd)
			return err
		})
		if err != nil {
			return SortLockResult{}, err
		}
		return result, nil
	}

	record, ok, err := o.repo.Load(ctx, characterID)
	if err != nil {
		return SortLockResult{}, err
	}
	if !ok {
		return SortLockResult{}, ErrInventoryNotFound
	}
	if accountID := strings.TrimSpace(cmd.AccountID); accountID != "" {
		if o.characters == nil {
			return SortLockResult{}, ErrOwnerUnavailable
		}
		character, found, err := o.characters.Load(ctx, characterID)
		if err != nil {
			return SortLockResult{}, err
		}
		if !found || strings.TrimSpace(character.AccountID) != accountID {
			return SortLockResult{}, fmt.Errorf("%w: char=%s account=%q", ErrSortLockForeignCharacter, characterID, accountID)
		}
	}

	record = dnfrepo.CloneInventory(record)
	record.CharacterID = characterID
	if record.Slots == nil {
		record.Slots = make(map[string]dnfrepo.ItemStack)
	}
	if record.Warehouse == nil {
		record.Warehouse = make(map[string]dnfrepo.ItemStack)
	}
	items, field := itemMapForList(&record, cmd.SourceListType)
	key := slotKey(cmd.SourceListType, cmd.SourceSlotIndex)
	stack, found := items[key]
	// 仓储有效物品契约：CurrentInventoryItemProjects / item_stack_contract.go 都以
	// ItemID <= 0 表示「这一格没有物品」（0:0 金币行同理）。Count 不参与判据：
	// 永久装扮是真实占格但 Count==0 的行（见 equip/owner.go 的说明），用 Count>0
	// 会把时装页的位置锁整体拒掉。
	if !found || stack.ItemID <= 0 {
		return SortLockResult{}, fmt.Errorf("%w: list=%d slot=%d", ErrSortLockSlotEmpty, cmd.SourceListType, cmd.SourceSlotIndex)
	}

	result := SortLockResult{
		CharacterID: characterID,
		ListType:    cmd.SourceListType,
		SlotIndex:   cmd.SourceSlotIndex,
		Locked:      cmd.SortLock,
	}
	if IsSortLocked(stack) == cmd.SortLock {
		return result, nil
	}
	items[key] = setSortLockStack(stack, cmd.SortLock)
	record.UpdatedAt = time.Now().UTC()
	if err := dnfrepo.SaveInventoryFields(ctx, o.repo, record, field); err != nil {
		return SortLockResult{}, err
	}
	result.Changed = true
	return result, nil
}
