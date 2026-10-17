// Package collectbox owns the durable mutation for the COLLECT_BOX_WINDOW
// "put one bead/orb into the selected box" action. Wire dispatch and PVF
// parsing stay in dnfbridge; this package only owns the atomic item move.
//
// 持久化口径沿用同仓 titlebook 的先例（internal/modules/dnf/titlebook/owner.go）：
// 收集箱登记条目**不是**客户端可请求的容器，所以它借用 InventoryRecord.Slots
// 上的一个保留 list type 作为持久化伪容器。这样：
//
//  1. 「从背包扣掉宝珠」与「把宝珠登记进箱子」落在**同一次**
//     SaveInventoryFields / 同一个事务里（见 WithinCharacterSettlement），
//     不会出现"扣了没登记"或"登记了没扣"的中间态；
//  2. 重登后由同一份 InventoryRecord 读回，天然保留；
//  3. 不新增表、不写迁移，与 dnf_legacy_character_items 现有 schema 完全兼容。
package collectbox

import (
	"context"
	"errors"
	"fmt"
	dnfitemlock "longheng.io/server/internal/modules/dnf/itemlock"
	"sort"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// ListType 是收集箱登记条目在 InventoryRecord.Slots 里的保留伪容器号。
	//
	// 101 与 titlebook.ListType = 100 相邻，两者都不在任何客户端可请求的容器
	// 清单里：dnfbridge.currentSelectedItemListTypes 只含 {0,1,2,7,38}，另有
	// 3(装备)/4(交易)/12(账号仓库) 由各自的请求解析器显式寻址。因此 101 上的
	// 条目不会被 current_item_list / incremental slot refresh 下发给客户端
	// ——收集箱的界面状态只由 class0/op1027 的 body 承载。
	ListType byte = 101
	// SlotBase 把 (boxIndex, slotIndex) 编码进一个 int16 槽位号：
	// slot = boxIndex*SlotBase + slotIndex。SlotBase=256 时 boxIndex 最大 127、
	// slotIndex 最大 255，正好铺满 int16 正值域而不与任何真实容器槽位重叠。
	SlotBase     int32 = 256
	MaxSlotIndex int32 = 255
	MaxBoxIndex  int32 = 127
)

var (
	ErrOwnerUnavailable  = errors.New("collectbox owner unavailable")
	ErrCharacterRequired = errors.New("collectbox selected character id required")
	ErrInventoryNotFound = errors.New("collectbox inventory record not found")
	ErrSourceNotFound    = errors.New("collectbox source item not found")
	ErrSourceLocked      = errors.New("collectbox source item is locked")
	ErrItemNotAllowed    = errors.New("collectbox item is not one of the PVF box slots")
	ErrBoxUnknown        = errors.New("collectbox box index is outside the PVF catalog")
	ErrBoxExpired        = errors.New("collectbox box has expired")
	ErrBoxFull           = errors.New("collectbox box is full")
	ErrBoxIndexInvalid   = errors.New("collectbox box index is invalid")
)

// Policy is the fully resolved, server-authoritative rule set for one box.
// The bridge builds it exclusively from the active Script.pvf document
// (localization/collectbox/collectboxinfo.etc), never from client data.
type Policy struct {
	// BoxIndex 是 PVF [Collect Box Info] 段里的 [Index]。
	BoxIndex int32
	// SlotCount 是 PVF [Item Position Info] 声明的槛位总数（行数 × 每行个数）。
	SlotCount int
	// AllowedItemIDs 是该箱 PVF 槛位上的物品 id 集合；不属于它的物品一律拒绝。
	AllowedItemIDs []int32
	// ExpiresAt / HasExpiry 来自 PVF [base expiration date]。
	ExpiresAt time.Time
	HasExpiry bool
}

// PutCommand is the trusted portion of the current-client op1022 request.
// Body shape (86JP Network/Handlers/CollectionBoxHandler.cs
// HandleInsertCollectBoxItem，body.Length >= 8):
//
//	[0:2] u16 LE box index
//	[2:4] u16 LE source inventory slot
//	[4:8] u32 LE item id
type PutCommand struct {
	SelectedCharacterID uint16
	// SourceList 是来源容器号；当前客户端固定 0（主背包）。
	SourceList byte
	SourceSlot int16
	ItemID     int32
	UpdatedAt  time.Time
}

// PutResult reports the committed move.
type PutResult struct {
	CharacterID string
	BoxIndex    int32
	SlotIndex   int32
	ItemID      int32
	SourceSlot  int16
	// SourceRemaining 是来源槽位扣减后的剩余数量（0 表示该槽已空）。
	SourceRemaining int64
	// SourceEmptied 报告来源槽位是否已被清空（决定客户端刷新形状）。
	SourceEmptied bool
	// TargetKey 是登记条目落地的槽位键（取证/日志用）。
	TargetKey string
	// RegisteredItemIDs is the complete box snapshot from the committed move.
	RegisteredItemIDs []int32
}

// Owner executes one collectbox put inside the character settlement
// transaction supplied by the repository group.
type Owner struct {
	settlements dnfrepo.CharacterSettlementUnitOfWork
}

// NewOwner 复用 titlebook 用的同一条角色事务边界：它把整个 Group 交给回调，
// 因此背包扣减与收集箱登记共用一次提交，不需要新增 UnitOfWork 方法。
func NewOwner(repos dnfrepo.Group) (*Owner, error) {
	if repos.Inventory == nil || repos.CharacterSettlement == nil {
		return nil, ErrOwnerUnavailable
	}
	return &Owner{settlements: repos.CharacterSettlement}, nil
}

// Put 把来源槽位上的 itemID 移入 policy 指定的收集箱的空槛位。
//
// 校验顺序刻意是"先证来源、再证 PVF、最后找空位"：任何一步失败都在写库之前
// 返回，事务回滚后背包保持原样。
func (o *Owner) Put(ctx context.Context, cmd PutCommand, policy Policy) (PutResult, error) {
	if o == nil || o.settlements == nil {
		return PutResult{}, ErrOwnerUnavailable
	}
	if cmd.SelectedCharacterID == 0 {
		return PutResult{}, ErrCharacterRequired
	}
	if err := validatePolicy(policy); err != nil {
		return PutResult{}, err
	}
	if !policyAllowsItem(policy, cmd.ItemID) {
		return PutResult{}, fmt.Errorf("%w: box=%d item=%d", ErrItemNotAllowed, policy.BoxIndex, cmd.ItemID)
	}
	if policy.HasExpiry && !policy.ExpiresAt.After(mutationTime(cmd.UpdatedAt)) {
		return PutResult{}, fmt.Errorf("%w: box=%d expired_at=%s", ErrBoxExpired, policy.BoxIndex, policy.ExpiresAt.Format(time.RFC3339))
	}
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)

	var result PutResult
	err := o.settlements.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		inventory, loadErr := loadInventory(ctx, tx.Inventory, characterID)
		if loadErr != nil {
			return loadErr
		}
		sourceKey := inventoryKey(cmd.SourceList, cmd.SourceSlot)
		source, found := inventory.Slots[sourceKey]
		if !found || source.ItemID <= 0 || source.ItemID != int64(cmd.ItemID) || source.Count <= 0 {
			return fmt.Errorf("%w: list=%d slot=%d item=%d", ErrSourceNotFound, cmd.SourceList, cmd.SourceSlot, cmd.ItemID)
		}
		if itemLocked(source) {
			return fmt.Errorf("%w: list=%d slot=%d", ErrSourceLocked, cmd.SourceList, cmd.SourceSlot)
		}
		slotIndex, found := firstFreeSlot(inventory.Slots, policy)
		if !found {
			return fmt.Errorf("%w: box=%d slot_count=%d", ErrBoxFull, policy.BoxIndex, policy.SlotCount)
		}

		// 剩余数量必须在改写 source.Count 之前算出来，否则多堆叠物品会少记一个。
		remainingCount := maxInt64(source.Count-1, 0)

		registered := source
		registered.Count = 1
		if registered.Extra == nil {
			registered.Extra = make(map[string]string, 4)
		}
		registered.Extra["collectbox_box_index"] = strconv.FormatInt(int64(policy.BoxIndex), 10)
		registered.Extra["collectbox_slot_index"] = strconv.FormatInt(int64(slotIndex), 10)
		registered.Extra["source"] = "collectbox_put"

		delete(inventory.Slots, sourceKey)
		if remainingCount > 0 {
			source.Count = remainingCount
			inventory.Slots[sourceKey] = source
		}
		targetKey := RegisteredSlotKey(policy.BoxIndex, slotIndex)
		inventory.Slots[targetKey] = registered
		inventory.UpdatedAt = mutationTime(cmd.UpdatedAt)
		if saveErr := dnfrepo.SaveInventoryFields(ctx, tx.Inventory, inventory, dnfrepo.InventoryFieldSlots); saveErr != nil {
			return saveErr
		}
		result = PutResult{
			CharacterID:       characterID,
			BoxIndex:          policy.BoxIndex,
			SlotIndex:         slotIndex,
			ItemID:            cmd.ItemID,
			SourceSlot:        cmd.SourceSlot,
			SourceRemaining:   remainingCount,
			SourceEmptied:     remainingCount <= 0,
			TargetKey:         targetKey,
			RegisteredItemIDs: RegisteredItemIDs(inventory, policy.BoxIndex),
		}
		return nil
	})
	if err != nil {
		return PutResult{}, err
	}
	return result, nil
}

// RegisteredItemIDs 返回某个箱子里**已登记**的条目 id，按槛位号升序。
// 只读，不修改记录；调用方给 nil 记录时返回 nil。
func RegisteredItemIDs(record dnfrepo.InventoryRecord, boxIndex int32) []int32 {
	if boxIndex < 0 || boxIndex > MaxBoxIndex {
		return nil
	}
	items := make([]registeredEntry, 0, 4)
	for key, stack := range record.Slots {
		index, slotIndex, ok := parseRegisteredSlotKey(key)
		if !ok || index != boxIndex || stack.ItemID <= 0 {
			continue
		}
		items = append(items, registeredEntry{slotIndex: slotIndex, itemID: int32(stack.ItemID)})
	}
	return sortRegisteredEntries(items)
}

// RegisteredItemIDsByBox 一次遍历返回全部箱子的登记条目，避免初始化时按箱
// 反复扫同一张 map。
func RegisteredItemIDsByBox(record dnfrepo.InventoryRecord) map[int32][]int32 {
	grouped := make(map[int32][]registeredEntry)
	for key, stack := range record.Slots {
		index, slotIndex, ok := parseRegisteredSlotKey(key)
		if !ok || stack.ItemID <= 0 {
			continue
		}
		grouped[index] = append(grouped[index], registeredEntry{slotIndex: slotIndex, itemID: int32(stack.ItemID)})
	}
	out := make(map[int32][]int32, len(grouped))
	for index, entries := range grouped {
		out[index] = sortRegisteredEntries(entries)
	}
	return out
}

// RegisteredSlotKey 返回 (boxIndex, slotIndex) 对应的持久化槽位键。
func RegisteredSlotKey(boxIndex, slotIndex int32) string {
	return inventoryKey(ListType, int16(boxIndex*SlotBase+slotIndex))
}

type registeredEntry struct {
	slotIndex int32
	itemID    int32
}

func sortRegisteredEntries(entries []registeredEntry) []int32 {
	if len(entries) == 0 {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].slotIndex < entries[j].slotIndex })
	out := make([]int32, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.itemID)
	}
	return out
}

// parseRegisteredSlotKey 反向解析伪容器槽位键；非本容器返回 false。
func parseRegisteredSlotKey(key string) (boxIndex, slotIndex int32, ok bool) {
	listType, slot, parsed := parseSlotKey(key)
	if !parsed || listType != ListType || slot < 0 {
		return 0, 0, false
	}
	index := int32(slot) / SlotBase
	remainder := int32(slot) % SlotBase
	if index < 0 || index > MaxBoxIndex || remainder < 0 || remainder > MaxSlotIndex {
		return 0, 0, false
	}
	return index, remainder, true
}

func parseSlotKey(key string) (byte, int16, bool) {
	listText, slotText, found := strings.Cut(key, ":")
	if !found {
		return 0, 0, false
	}
	list, err := strconv.ParseUint(strings.TrimSpace(listText), 10, 8)
	if err != nil {
		return 0, 0, false
	}
	slot, err := strconv.ParseInt(strings.TrimSpace(slotText), 10, 16)
	if err != nil {
		return 0, 0, false
	}
	return byte(list), int16(slot), true
}

func firstFreeSlot(items map[string]dnfrepo.ItemStack, policy Policy) (int32, bool) {
	for slotIndex := int32(0); slotIndex < int32(policy.SlotCount); slotIndex++ {
		if _, occupied := items[RegisteredSlotKey(policy.BoxIndex, slotIndex)]; !occupied {
			return slotIndex, true
		}
	}
	return -1, false
}

func validatePolicy(policy Policy) error {
	if policy.BoxIndex <= 0 || policy.BoxIndex > MaxBoxIndex {
		return fmt.Errorf("%w: box=%d", ErrBoxIndexInvalid, policy.BoxIndex)
	}
	if policy.SlotCount <= 0 || policy.SlotCount > int(MaxSlotIndex)+1 {
		return fmt.Errorf("%w: box=%d slot_count=%d", ErrBoxIndexInvalid, policy.BoxIndex, policy.SlotCount)
	}
	return nil
}

func policyAllowsItem(policy Policy, itemID int32) bool {
	if itemID <= 0 {
		return false
	}
	for _, allowed := range policy.AllowedItemIDs {
		if allowed == itemID {
			return true
		}
	}
	return false
}

func loadInventory(ctx context.Context, repository dnfrepo.InventoryRepository, characterID string) (dnfrepo.InventoryRecord, error) {
	if repository == nil {
		return dnfrepo.InventoryRecord{}, ErrOwnerUnavailable
	}
	inventory, found, err := repository.Load(ctx, characterID)
	if err != nil {
		return dnfrepo.InventoryRecord{}, err
	}
	if !found || strings.TrimSpace(inventory.CharacterID) != characterID {
		return dnfrepo.InventoryRecord{}, fmt.Errorf("%w: character=%s", ErrInventoryNotFound, characterID)
	}
	inventory = dnfrepo.CloneInventory(inventory)
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	return inventory, nil
}

func inventoryKey(listType byte, slot int16) string {
	return fmt.Sprintf("%d:%d", listType, slot)
}

func mutationTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func itemLocked(stack dnfrepo.ItemStack) bool {
	return dnfitemlock.IsLocked(stack)
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
