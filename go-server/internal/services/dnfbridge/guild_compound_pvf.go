package dnfbridge

// 本文件只做公会合成（op835 守护珠 / op864 勋章）的 PVF 与 list38 适配。
//
// 规则（成功率、失败扣料、同族升品、随机候选）由 modules/dnf/guildcompound
// 的 Rules 组装与裁决；这里不实现 handler，不写 Service 字段，也不把任何
// 合成配方写死在本文件。
//
// list38 分页已由 item_target_container.go 的唯一容器判定固定：
//   - 勋章 [equipment type] `[flag]` -> 38:0..48
//   - 守护珠 [stackable type] `[flag gem]` -> 38:49..97
//
// 域接口约定：
//   guildcompound.MaterialDefinition{Family MaterialFamily; Rarity uint32}
//   guildcompound.LoadCompoundCatalog(pvf.Source)
// 本文件只消费 MaterialDefinition 与 CompoundKind；目录加载与 Rules 由主线程
// 组装，避免桥接层和域层各自维护一份配方。

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"

	"longheng.io/server/internal/modules/dnf/guildcompound"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	// errGuildCompoundNotMaterial 只表示"解析成功，但该物品不属于本次合成的
	// 勋章/守护珠家族"。候选池用它跳过路径预筛的误命中，不吞掉真正的 PVF
	// 读取或解析错误。
	errGuildCompoundNotMaterial = errors.New("dnf guild compound item is not a medal or guardian gem")
	// errGuildCompoundStackInvalid 是 updateStack 的输入拒绝哨兵。
	errGuildCompoundStackInvalid = errors.New("dnf guild compound stack is invalid")
)

// currentGuildCompoundDefinition 把真实 PVF 物品解析成域层材料定义。
//
// 只接受 list38 的两个已证实家族，并同时核对：
//   - 装备类型确为 `[flag]`，容器页是 0..48；
//   - 可堆叠类型确为 `[flag gem]`，容器页是 49..97；
//   - rarity 落在 u32 可表示范围。
//
// 该函数不判断"四件必须同 ID"或"同品级"；这些是 Rules 的裁决。
func currentGuildCompoundDefinition(
	catalog *pvfDungeonDropCatalog,
	id uint32,
) (guildcompound.MaterialDefinition, error) {
	if catalog == nil || catalog.source == nil {
		return guildcompound.MaterialDefinition{}, errDungeonDropSourceRequired
	}
	if id == 0 {
		return guildcompound.MaterialDefinition{}, fmt.Errorf(
			"%w: item id is zero", errGuildCompoundNotMaterial)
	}
	definition, err := catalog.ResolveItem(id)
	if err != nil {
		return guildcompound.MaterialDefinition{}, err
	}
	if definition.ItemID != id {
		return guildcompound.MaterialDefinition{}, fmt.Errorf(
			"%w: resolved item=%d for requested id=%d",
			errGuildCompoundNotMaterial, definition.ItemID, id)
	}

	container := currentPVFItemTargetContainer(definition)
	if container.ListType != currentGuildMedalInventoryListType {
		return guildcompound.MaterialDefinition{}, fmt.Errorf(
			"%w: item=%d kind=%s list=%d",
			errGuildCompoundNotMaterial, id, definition.Kind, container.ListType)
	}

	var family guildcompound.MaterialFamily
	switch {
	case container.GuildMedal:
		if definition.Kind != dungeonDropItemEquipment {
			return guildcompound.MaterialDefinition{}, fmt.Errorf(
				"%w: item=%d medal container has kind=%s",
				errGuildCompoundNotMaterial, id, definition.Kind)
		}
		if normalizeEquipmentPlacementPVFType(definition.EquipmentType) != currentGuildMedalPVFType {
			return guildcompound.MaterialDefinition{}, fmt.Errorf(
				"%w: item=%d equipment_type=%q",
				errGuildCompoundNotMaterial, id, definition.EquipmentType)
		}
		if container.SlotStart != guildcompound.GuildMedalPageFirst ||
			container.SlotEnd != guildcompound.GuildMedalPageLast {
			return guildcompound.MaterialDefinition{}, fmt.Errorf(
				"%w: item=%d medal page=%d..%d",
				errGuildCompoundNotMaterial, id, container.SlotStart, container.SlotEnd)
		}
		family = guildcompound.MaterialFamilyGuildMedal

	case container.GuardianGem:
		if definition.Kind != dungeonDropItemStackable {
			return guildcompound.MaterialDefinition{}, fmt.Errorf(
				"%w: item=%d gem container has kind=%s",
				errGuildCompoundNotMaterial, id, definition.Kind)
		}
		if normalizeDungeonDropStackableType(definition.StackableType) != "[flag gem]" {
			return guildcompound.MaterialDefinition{}, fmt.Errorf(
				"%w: item=%d stackable_type=%q",
				errGuildCompoundNotMaterial, id, definition.StackableType)
		}
		if container.SlotStart != guildcompound.GuardianGemPageFirst ||
			container.SlotEnd != guildcompound.GuardianGemPageLast {
			return guildcompound.MaterialDefinition{}, fmt.Errorf(
				"%w: item=%d gem page=%d..%d",
				errGuildCompoundNotMaterial, id, container.SlotStart, container.SlotEnd)
		}
		family = guildcompound.MaterialFamilyGuardianGem

	default:
		return guildcompound.MaterialDefinition{}, fmt.Errorf(
			"%w: item=%d is not in a list38 guild page",
			errGuildCompoundNotMaterial, id)
	}

	rarity := definition.Rarity
	if family == guildcompound.MaterialFamilyGuardianGem {
		// 当前共享 dungeonDropItemDefinition 只为装备分支填写 Rarity；
		// stackable 的 [rarity] 仍留在文档里。这里只重读已经通过 flag 预筛、
		// 且确认是 [flag gem] 的守护珠文档，不扫描其它 stackable。
		rarity, err = currentGuildCompoundGemRarity(catalog, definition)
		if err != nil {
			return guildcompound.MaterialDefinition{}, err
		}
	}
	if rarity < 0 || rarity > math.MaxUint32 {
		return guildcompound.MaterialDefinition{}, fmt.Errorf(
			"%w: item=%d rarity=%d", errGuildCompoundStackInvalid, id, rarity)
	}
	return guildcompound.MaterialDefinition{
		Family: family,
		Rarity: uint32(rarity),
	}, nil
}

// currentGuildCompoundGemRarity 读取守护珠文档里的 [rarity]。
//
// [rarity] 缺失表示品级 0（真实 PVF 的 90000/90010/... 档就是这样）；
// 1..3 档会在文档中显式列出。这里不使用 ID 相邻关系猜品级。
func currentGuildCompoundGemRarity(
	catalog *pvfDungeonDropCatalog,
	definition dungeonDropItemDefinition,
) (int64, error) {
	docPath, text, err := readDungeonDropText(catalog.source, "stackable", definition.PVFPath)
	if err != nil {
		return 0, fmt.Errorf(
			"read guild guardian gem item=%d path=%q: %w",
			definition.ItemID, definition.PVFPath, err)
	}
	document, err := dnfpvf.Parse(docPath, text)
	if err != nil {
		return 0, fmt.Errorf(
			"parse guild guardian gem item=%d path=%q: %w",
			definition.ItemID, docPath, err)
	}
	rarity, found := document.Int("rarity")
	if !found {
		return 0, nil
	}
	return rarity, nil
}

// currentGuildCompoundCandidatePool 返回指定家族、指定下一品级的真实候选。
//
// 顺序严格采用 PVF 的 .lst 首次出现顺序（不是 item ID 排序），并按 ID 去重；
// 这样随机选择在相同 PVF 下可复现，也不会因为 map 遍历改变结果。
//
// 性能边界：先用已经解析好的引用路径做大小写无关的 "flag" 预筛，再对命中项
// 调用 ResolveItem。不会为一个候选池扫描全部 stackable/equipment 文档。
// 路径预筛只是廉价漏斗，最终家族仍由 currentGuildCompoundDefinition 的
// PVF 类型与 list38 分页判定。
func currentGuildCompoundCandidatePool(
	catalog *pvfDungeonDropCatalog,
	kind guildcompound.CompoundKind,
	rarity uint32,
) ([]uint32, error) {
	if catalog == nil || catalog.source == nil {
		return nil, errDungeonDropSourceRequired
	}

	var (
		ids      []uint32
		wantKind guildcompound.MaterialFamily
	)
	switch kind {
	case guildcompound.CompoundGem:
		// stackableIDs 已由 loadDungeonDropList 保留真实 .lst 顺序并去重。
		ids = append([]uint32(nil), catalog.stackableIDs...)
		wantKind = guildcompound.MaterialFamilyGuardianGem
	case guildcompound.CompoundFlag:
		// equipment 目录没有常驻顺序切片；重新读取 .lst 只解析引用表，
		// 不读取任何装备文档。
		_, equipmentIDs, err := loadDungeonDropList(catalog.source, dungeonDropEquipmentList)
		if err != nil {
			return nil, fmt.Errorf("load guild compound flag list: %w", err)
		}
		ids = equipmentIDs
		wantKind = guildcompound.MaterialFamilyGuildMedal
	default:
		return nil, fmt.Errorf(
			"%w: unknown compound kind=%d", errGuildCompoundNotMaterial, kind)
	}

	out := make([]uint32, 0, 16)
	seen := make(map[uint32]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}

		reference, known := catalog.itemRefs[id]
		if !known || !strings.Contains(strings.ToLower(reference.path), "flag") {
			continue
		}
		material, err := currentGuildCompoundDefinition(catalog, id)
		if err != nil {
			// 仅跳过"解析成功，但类型/分页不属于本家族"的误命中。
			// 读取、解析、非法品级等真实错误必须上抛，不能把损坏的 PVF
			// 静默变成"没有候选"。
			if errors.Is(err, errGuildCompoundNotMaterial) {
				continue
			}
			return nil, err
		}
		if material.Family != wantKind || material.Rarity != rarity {
			continue
		}
		out = append(out, id)
	}
	return out, nil
}

// currentGuildCompoundUpdateStack 在材料扣减后重建一条 list38 的权威 0x77 行。
//
// 原始 item_kind 决定模板语义：stackable（守护珠）允许 Count>0 的可堆叠数量，
// equipment（勋章）必须是单件实例。写入 RawEntry 时显式把 +0x06 的数量字段
// 校正为 stack.Count，避免旧 amount 别名或陈旧 RawEntry 把扣减前/后的数量
// 重新投影回客户端；其余字段仍由 currentItemListEntryFromStack 按原 kind
// 与 RawEntry 保留。
//
// 异常输入一律拒绝：非 list38 槽、跨页槽、未知/自相矛盾的 kind、非法件数、
// 非 0x77 的 RawEntry、RawEntry 槽/item ID 与仓储行不一致。
func currentGuildCompoundUpdateStack(
	stack dnfrepo.ItemStack,
	slot int16,
) (dnfrepo.ItemStack, error) {
	if slot < guildcompound.GuildMedalPageFirst ||
		slot > guildcompound.GuardianGemPageLast {
		return dnfrepo.ItemStack{}, fmt.Errorf(
			"%w: slot=%d outside list38", errGuildCompoundStackInvalid, slot)
	}
	if stack.ItemID <= 0 || stack.ItemID > math.MaxUint32 {
		return dnfrepo.ItemStack{}, fmt.Errorf(
			"%w: slot=%d item=%d", errGuildCompoundStackInvalid, slot, stack.ItemID)
	}
	if stack.Count <= 0 || stack.Count > math.MaxUint32 {
		return dnfrepo.ItemStack{}, fmt.Errorf(
			"%w: slot=%d count=%d", errGuildCompoundStackInvalid, slot, stack.Count)
	}

	kind, err := currentGuildCompoundStackKind(stack)
	if err != nil {
		return dnfrepo.ItemStack{}, err
	}
	switch kind {
	case dungeonDropItemStackable:
		if slot < guildcompound.GuardianGemPageFirst ||
			slot > guildcompound.GuardianGemPageLast {
			return dnfrepo.ItemStack{}, fmt.Errorf(
				"%w: stackable slot=%d is outside guardian-gem page",
				errGuildCompoundStackInvalid, slot)
		}
	case dungeonDropItemEquipment:
		if slot < guildcompound.GuildMedalPageFirst ||
			slot > guildcompound.GuildMedalPageLast {
			return dnfrepo.ItemStack{}, fmt.Errorf(
				"%w: equipment slot=%d is outside medal page",
				errGuildCompoundStackInvalid, slot)
		}
		if stack.Count != 1 {
			return dnfrepo.ItemStack{}, fmt.Errorf(
				"%w: medal slot=%d count=%d must be 1",
				errGuildCompoundStackInvalid, slot, stack.Count)
		}
	default:
		return dnfrepo.ItemStack{}, fmt.Errorf(
			"%w: slot=%d kind=%s", errGuildCompoundStackInvalid, slot, kind)
	}

	if len(stack.RawEntry) != 0 {
		if len(stack.RawEntry) != currentItemListEntryWireSize {
			return dnfrepo.ItemStack{}, fmt.Errorf(
				"%w: slot=%d raw length=%d",
				errGuildCompoundStackInvalid, slot, len(stack.RawEntry))
		}
		if int16(binary.LittleEndian.Uint16(stack.RawEntry[0x00:0x02])) != slot {
			return dnfrepo.ItemStack{}, fmt.Errorf(
				"%w: slot=%d raw slot=%d",
				errGuildCompoundStackInvalid, slot,
				binary.LittleEndian.Uint16(stack.RawEntry[0x00:0x02]))
		}
		if binary.LittleEndian.Uint32(stack.RawEntry[0x02:0x06]) != uint32(stack.ItemID) {
			return dnfrepo.ItemStack{}, fmt.Errorf(
				"%w: slot=%d raw item=%d stack item=%d",
				errGuildCompoundStackInvalid, slot,
				binary.LittleEndian.Uint32(stack.RawEntry[0x02:0x06]),
				stack.ItemID)
		}
	}

	entry := currentItemListEntryFromStack(currentGuildMedalInventoryListType, slot, stack)
	// list38 的 +0x06 是这一行的件数。currentItemListStackAmount 对部分
	// legacy Extra 别名有优先级，这里再按 durable Count 校正一次，保证扣料后
	// 的行不会把旧数量带回去。
	binary.LittleEndian.PutUint32(entry.data[0x06:0x0A], uint32(stack.Count))
	stack.RawEntry = append([]byte(nil), entry.data[:]...)
	if got := binary.LittleEndian.Uint32(stack.RawEntry[0x06:0x0A]); got != uint32(stack.Count) {
		return dnfrepo.ItemStack{}, fmt.Errorf(
			"%w: slot=%d raw count=%d durable count=%d",
			errGuildCompoundStackInvalid, slot, got, stack.Count)
	}
	return stack, nil
}

// currentGuildCompoundStackKind 只从仓储行携带的 PVF 身份恢复原始 kind。
// item_kind 是发放/导入路径写入的权威家族；缺少它时才用 stackable_type /
// equipment_type 兜底。两个家族字段若同时出现或与 item_kind 矛盾，直接拒绝。
func currentGuildCompoundStackKind(stack dnfrepo.ItemStack) (dungeonDropItemKind, error) {
	itemKind := strings.ToLower(strings.TrimSpace(stack.Extra["item_kind"]))
	stackableType := normalizeDungeonDropStackableType(stack.Extra["stackable_type"])
	equipmentType := normalizeEquipmentPlacementPVFType(stack.Extra["equipment_type"])

	switch itemKind {
	case string(dungeonDropItemStackable):
		if equipmentType != "" {
			return "", fmt.Errorf(
				"%w: item_kind=stackable with equipment_type=%q",
				errGuildCompoundStackInvalid, equipmentType)
		}
		if stackableType != "" && stackableType != "[flag gem]" {
			return "", fmt.Errorf(
				"%w: item_kind=stackable with stackable_type=%q",
				errGuildCompoundStackInvalid, stackableType)
		}
		return dungeonDropItemStackable, nil

	case string(dungeonDropItemEquipment):
		if stackableType != "" {
			return "", fmt.Errorf(
				"%w: item_kind=equipment with stackable_type=%q",
				errGuildCompoundStackInvalid, stackableType)
		}
		if equipmentType != "" && equipmentType != currentGuildMedalPVFType {
			return "", fmt.Errorf(
				"%w: item_kind=equipment with equipment_type=%q",
				errGuildCompoundStackInvalid, equipmentType)
		}
		return dungeonDropItemEquipment, nil

	case "":
		switch {
		case stackableType != "" && equipmentType == "":
			if stackableType != "[flag gem]" {
				return "", fmt.Errorf(
					"%w: stackable_type=%q", errGuildCompoundStackInvalid, stackableType)
			}
			return dungeonDropItemStackable, nil
		case equipmentType != "" && stackableType == "":
			if equipmentType != currentGuildMedalPVFType {
				return "", fmt.Errorf(
					"%w: equipment_type=%q", errGuildCompoundStackInvalid, equipmentType)
			}
			return dungeonDropItemEquipment, nil
		default:
			return "", fmt.Errorf(
				"%w: missing item_kind and ambiguous family fields stackable=%q equipment=%q",
				errGuildCompoundStackInvalid, stackableType, equipmentType)
		}

	default:
		return "", fmt.Errorf(
			"%w: unknown item_kind=%q", errGuildCompoundStackInvalid, itemKind)
	}
}

// currentGuildCompoundGrant 把产物放进 list38 对应页，一次只发一个实例。
//
// 复用现有官方发放 helper grantCurrentGuildMedalPageItem（与
// grantCurrentNPCShopProduct 的 list38 分支相同），保留它现有的同身份合并、
// 锁定行不特殊处理、期限匹配和 StackLimit 规则。勋章内部强制 StackLimit=1；
// 守护珠按 PVF StackLimit 合并。
//
// 返回实际落位槽。页满时返回 guildcompound.ErrNoSpace；任何非 list38 或
// 非单槽结果也视为无空间，避免产物落到其它容器。
func currentGuildCompoundGrant(
	catalog *pvfDungeonDropCatalog,
	inventory *dnfrepo.InventoryRecord,
	id uint32,
) (int16, error) {
	if catalog == nil || catalog.source == nil {
		return 0, errDungeonDropSourceRequired
	}
	if inventory == nil {
		return 0, fmt.Errorf("%w: inventory is nil", errGuildCompoundNotMaterial)
	}
	definition, err := catalog.ResolveItem(id)
	if err != nil {
		return 0, err
	}
	if _, err := currentGuildCompoundDefinition(catalog, id); err != nil {
		return 0, err
	}
	container := currentPVFItemTargetContainer(definition)
	if container.ListType != currentGuildMedalInventoryListType ||
		(!container.GuildMedal && !container.GuardianGem) {
		return 0, fmt.Errorf(
			"%w: item=%d list=%d medal=%t gem=%t",
			errGuildCompoundNotMaterial, id, container.ListType,
			container.GuildMedal, container.GuardianGem)
	}

	slots, err := grantCurrentGuildMedalPageItem(inventory, definition, container, 1)
	if err != nil {
		if errors.Is(err, errDungeonPickupInventoryFull) {
			return 0, fmt.Errorf("%w: item=%d: %v", guildcompound.ErrNoSpace, id, err)
		}
		return 0, err
	}
	if len(slots) != 1 {
		return 0, fmt.Errorf(
			"%w: item=%d landed in %d slots %v",
			guildcompound.ErrNoSpace, id, len(slots), slots)
	}
	if slots[0] < container.SlotStart || slots[0] > container.SlotEnd {
		return 0, fmt.Errorf(
			"%w: item=%d landed at 38:%d outside %d..%d",
			guildcompound.ErrNoSpace, id, slots[0],
			container.SlotStart, container.SlotEnd)
	}
	key := currentCeraShopInventorySlotKey(currentGuildMedalInventoryListType, slots[0])
	stack, found := inventory.Slots[key]
	if !found || stack.ItemID != int64(id) || stack.Count <= 0 {
		return 0, fmt.Errorf(
			"%w: item=%d did not materialize at 38:%d",
			guildcompound.ErrNoSpace, id, slots[0])
	}
	// 与 rules 的 Grant closure 使用同一条已提交行重建路径：发放 helper
	// 可能把同身份守护珠合并进旧槽，这里按最终 Count 再校正一次 RawEntry。
	updated, err := currentGuildCompoundUpdateStack(stack, slots[0])
	if err != nil {
		return 0, err
	}
	inventory.Slots[key] = updated
	return slots[0], nil
}
