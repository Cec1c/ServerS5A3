package guildcompound

import (
	"errors"
	"fmt"
)

// 本文件把 op835/op864 的 MaterialRefs 解释为 list38 容器槽位。
//
// 证据（v152 EXE，SHA-256 44AECEE7C48BC1C89582EF1CD95694950569951CAEEF3B22EC3478C917B8CE7E）：
//   - op14 接收器 0x02025A90 先读行首 u16 slot，再以行内 list type 调用
//     0x02531A80 取得容器；list type 0x26 返回 [0x056A37B8]（list38）。
//   - 对已有行，0x0202638C 把该 u16 slot 直接传给 0x025EB680；后者以
//     container+0x58/+0x5C 的指针向量做边界检查并返回 vector[slot]。
//   - 对新行，0x020269F7 把同一 slot 传给 0x025F04E0；其 0x025F05CD 执行
//     vector[slot] = item。因此 op14 的行 slot 就是容器向量下标。
//   - 835 的发送器用 0x025ECA10 按物品 ID 反查同一向量，返回下标；864 的
//     选择器直接把容器行 +8 的索引写入材料数组。两者写入窗口数组的值都是
//     该下标，而不是物品模板 ID。
//
// 因而 MaterialRefs 的每个 u32 值在 list38 中与 op13/op14 行的 slot 字段
// 恒等。本文件只使用这一条已闭合事实；它不猜测合成概率、产物、每槽消耗
// 数量或成功回包。
const (
	// List38SlotCount 是 list38（勋章 0..48 + 守护珠 49..97）的 98 个槽位。
	List38SlotCount = 98

	// GuildMedalPageFirst/Last 是 864（勋章合成）使用的勋章页。
	GuildMedalPageFirst int16 = 0
	GuildMedalPageLast  int16 = 48
	// GuardianGemPageFirst/Last 是 835（守护珠合成）使用的守护珠页。
	GuardianGemPageFirst int16 = 49
	GuardianGemPageLast  int16 = 97
)

// MaterialFamily 选择 opcode 对应的 list38 分页。
type MaterialFamily uint8

const (
	// MaterialFamilyUnspecified 表示调用方没有给出 opcode 家族，拒绝解析。
	MaterialFamilyUnspecified MaterialFamily = iota
	// MaterialFamilyGuildMedal 对应 op864，引用必须落在 0..48。
	MaterialFamilyGuildMedal
	// MaterialFamilyGuardianGem 对应 op835，引用必须落在 49..97。
	MaterialFamilyGuardianGem
)

// page 返回该家族在 list38 中允许的闭区间。
func (family MaterialFamily) page() (int16, int16, bool) {
	switch family {
	case MaterialFamilyGuildMedal:
		return GuildMedalPageFirst, GuildMedalPageLast, true
	case MaterialFamilyGuardianGem:
		return GuardianGemPageFirst, GuardianGemPageLast, true
	default:
		return 0, 0, false
	}
}

// List38Slot 是服务端已提交的 list38 快照中的一个槽。Slot 与 wire 0x77 行
// 的 +0x00 u16、以及请求 MaterialRefs 的值同一坐标；Quantity 是该槽当前
// 可用的堆叠数量。
type List38Slot struct {
	Slot     int16
	ItemID   uint32
	Quantity uint32
}

// ResolvedMaterial 是一个保序聚合后的材料槽。
//
// Requested 是同一槽在请求中出现的次数。op835 的原生选择链会为每一行从
// 同一堆叠预留并减一，因此该家族要求 Available >= Requested；op864 尚无
// 同一层级的数量消费证据，这里只保留出现次数，不把次数冒充扣料量。
type ResolvedMaterial struct {
	Slot      int16
	ItemID    uint32
	Available uint32
	Requested uint32
}

// MaterialResolution 是严格解析结果。Materials 按槽在请求中首次出现的
// 顺序排列；重复槽聚合到同一项并累加 Requested。
type MaterialResolution struct {
	Materials           []ResolvedMaterial
	TotalReferences     int
	TotalRequestedUnits uint32
}

var (
	// ErrNoMaterialRefs 表示 count 为 0，没有可解析的材料引用。
	ErrNoMaterialRefs = errors.New("guild compound request has no material references")
	// ErrMaterialRefAbsent 表示 count 范围内出现 0xFFFFFFFF。
	ErrMaterialRefAbsent = errors.New("guild compound material reference is the absent-slot marker")
	// ErrMaterialRefOutOfRange 表示引用超出 list38 的 0..97。
	ErrMaterialRefOutOfRange = errors.New("guild compound material reference is outside list38")
	// ErrMaterialRefOutsidePage 表示引用落在另一家族的 49 格分页。
	ErrMaterialRefOutsidePage = errors.New("guild compound material reference is outside the opcode page")
	// ErrMaterialRefMissing 表示引用的槽在服务端快照中不存在。
	ErrMaterialRefMissing = errors.New("guild compound material reference has no list38 slot")
	// ErrMaterialRefEmpty 表示槽存在但没有可用物品。
	ErrMaterialRefEmpty = errors.New("guild compound material reference slot is empty")
	// ErrMaterialRefQuantity 表示槽内可用数量不足保存的槽引用数。
	ErrMaterialRefQuantity = errors.New("guild compound material reference has insufficient quantity")
	// ErrMaterialSnapshotDuplicate 表示调用方的快照自身对同一槽给了多行。
	ErrMaterialSnapshotDuplicate = errors.New("guild compound list38 snapshot contains a duplicate slot")
)

// ResolveMaterialRefs 把请求前 count 个 MaterialRefs 解析成 list38 槽。
//
// 校验顺序固定：家族/件数 -> 每个引用 -> 槽存在与可用性 -> 835 的计数
// 可用量。两个家族都保留同槽重复：835 由 r143 的同槽连续选择原生回放证明，
// 864 的 0x01047F7C..0x01047FE6 添加路径同样以当前行数写下一项，没有去重；
// 但只有 835 已证明每次选择消耗一份，因此只对 835 校验 Available >= Requested。
// 任何一项失败都返回零结果，调用方不得据此扣料或发奖。
func ResolveMaterialRefs(family MaterialFamily, count uint32, refs [MaterialRefCount]uint32, slots []List38Slot) (MaterialResolution, error) {
	var result MaterialResolution
	first, last, ok := family.page()
	if !ok {
		return result, fmt.Errorf("%w: family=%d", ErrMaterialRefOutsidePage, family)
	}
	if count > MaxMaterialCount {
		return result, fmt.Errorf("%w: got %d", ErrMaterialCount, count)
	}
	if count == 0 {
		return result, ErrNoMaterialRefs
	}

	bySlot := make(map[int16]List38Slot, len(slots))
	for _, slot := range slots {
		if _, exists := bySlot[slot.Slot]; exists {
			return result, fmt.Errorf("%w: slot=%d", ErrMaterialSnapshotDuplicate, slot.Slot)
		}
		bySlot[slot.Slot] = slot
	}

	materialIndex := make(map[int16]int, count)
	result.Materials = make([]ResolvedMaterial, 0, count)
	for index := uint32(0); index < count; index++ {
		ref := refs[index]
		if ref == AbsentMaterialRef {
			return MaterialResolution{}, fmt.Errorf("%w: index=%d", ErrMaterialRefAbsent, index)
		}
		if ref >= List38SlotCount {
			return MaterialResolution{}, fmt.Errorf("%w: index=%d value=%d", ErrMaterialRefOutOfRange, index, ref)
		}
		if int16(ref) < first || int16(ref) > last {
			return MaterialResolution{}, fmt.Errorf("%w: index=%d value=%d page=%d..%d", ErrMaterialRefOutsidePage, index, ref, first, last)
		}
		slot, exists := bySlot[int16(ref)]
		if !exists {
			return MaterialResolution{}, fmt.Errorf("%w: index=%d value=%d", ErrMaterialRefMissing, index, ref)
		}
		if existing, exists := materialIndex[slot.Slot]; exists {
			result.Materials[existing].Requested++
			continue
		}
		if slot.ItemID == 0 {
			return MaterialResolution{}, fmt.Errorf("%w: index=%d value=%d", ErrMaterialRefEmpty, index, ref)
		}
		if slot.Quantity == 0 {
			return MaterialResolution{}, fmt.Errorf("%w: index=%d value=%d", ErrMaterialRefQuantity, index, ref)
		}
		materialIndex[slot.Slot] = len(result.Materials)
		result.Materials = append(result.Materials, ResolvedMaterial{
			Slot:      slot.Slot,
			ItemID:    slot.ItemID,
			Available: slot.Quantity,
			Requested: 1,
		})
	}
	if family == MaterialFamilyGuardianGem {
		for _, material := range result.Materials {
			if material.Available < material.Requested {
				return MaterialResolution{}, fmt.Errorf("%w: slot=%d need=%d available=%d", ErrMaterialRefQuantity, material.Slot, material.Requested, material.Available)
			}
		}
	}
	result.TotalReferences = int(count)
	for _, material := range result.Materials {
		result.TotalRequestedUnits += material.Requested
	}
	return result, nil
}
