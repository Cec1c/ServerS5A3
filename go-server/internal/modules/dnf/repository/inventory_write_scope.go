// 本文件是 op19 两条落库路径**共用**的增量写（O-B）口径的唯一实现：
//
//	equip.Owner.saveMove            —— 穿/脱（一个背包端点 + 一个穿戴端点）
//	inventory.Owner.saveMoveResult  —— 背包内移动（含主背包 ↔ 仓库的跨容器移动）
//
// 背景（_dist/reference-inventory-persistence-study.md §2.2/§2.4/§2.6/§5.2）：厂商参考
// 实现里"穿/脱"与"背包内移动"本来就是**同一条链路**（opcode 0x0013 移动物品 →
// InventoryMoveCommitService → OnlineInventoryMutationCommitCoordinator →
// InventoryMoveService.TryMove）：在线背包常驻内存，内存变更只往一个
// Dictionary<InventoryListType, HashSet<short>> 记槽号，事务提交时
// 写入集合 = 脏位集合（InventoryPersistenceService.cs:323-331 SaveDirtyItems →
// :396-417 CollectDirtyItemSlots → :419-454 SaveSlot），所以一次穿脱/一次背包内互换
// 都是 8 条 SQL，与背包里有多少件完全无关。
//
// 我们的等价物是"逐 op 显式给出本次真正改动的 key 集合"（O-B）。它必须只有**一份**
// 实现：穿/脱与背包内移动走同一套开关、同一套回退口径、同一个缺口映射，否则两条路
// 会各自漂移（参考实现里它们连函数都是同一个）。
//
// 与旧实现的差别（study §5.2-2 点名的坑）：旧的 equip.inventoryCollectionName 只接受
// **恰好一个**字段，因此无法表达"主背包 ↔ 仓库"这种一次动两个 collection 的移动。
// 本文件的 InventoryCollectionNames 接受任意字段集合并返回其 collection 名列表。
//
// 三个安全性质（与已落地的 O-B 完全一致）：
//  1. **类型断言**探测增量能力：memory / 交易作用域 / 租借作用域 / 结算作用域等实现
//     不满足 ItemStackSlotDeltaWriter（或 EquipmentEntryDeltaWriter）⇒ 留在整容器路径；
//  2. 拿不到 key 集合 ⇒ **回退整容器写 + 记一条回退事件**（默认观察者写 slog WARN），
//     绝不静默不写；
//  3. 空 key 在仓储最底层是**硬错误**（mysql 的 replaceItemStackKeys /
//     replaceEquipmentEntryKeys），所以"一条 SQL 都不发"在结构上不可达。
//
// 开关口径沿用本仓习惯（write_events.go:1-11、equip 侧旧实现）：DNFBRIDGE_* 环境变量、
// 消费点自读、按原始字符串缓存、只认显式字面量、默认档 = 今天的字节、偏离默认必留事件。
package repository

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
)

const (
	// InventorySaveScopeEnvName 同时管住 equip 的穿/脱与 inventory 的背包内移动
	// （参考实现里它们是同一条链路、同一个脏位机制，所以共用一个开关）。
	InventorySaveScopeEnvName = "DNFBRIDGE_INVENTORY_SAVE_SCOPE"
	// EquipmentSaveScopeEnvName 管住穿戴表（entries）的落库粒度。
	EquipmentSaveScopeEnvName = "DNFBRIDGE_EQUIPMENT_SAVE_SCOPE"

	// SaveScopeCollection 是**今天**的口径：整容器重写（默认档，回滚字面量见下）。
	SaveScopeCollection = "collection"
	// SaveScopeSlot 是参考口径：只写本次改动的槽（背包）。
	SaveScopeSlot = "slot"
	// SaveScopeEntry 是参考口径：只写本次改动的 entry（穿戴表）。
	SaveScopeEntry = "entry"
)

// ItemStackSlotDeltaWriter 由 MySQL 背包仓储实现
// （repository/mysql/mysql_bag.go 的 SaveItemStackSlotDelta）。
//
// 接口定义在**消费方能看见的公共位置**（repository）而不是某一个消费包里：域层只依赖
// InventoryRepository，增量能力是可选的、用类型断言探测的加固路径。没有实现它的仓储
// （memory / 交易作用域 / 租借作用域 / 结算作用域 / 任何测试替身）因此自动留在今天的
// 整容器路径上。equip 与 inventory 两个包断言的是**同一个**接口。
type ItemStackSlotDeltaWriter interface {
	SaveItemStackSlotDelta(context.Context, InventoryRecord, string, []string) error
}

// EquipmentEntryDeltaWriter 由 MySQL 穿戴仓储实现
// （repository/mysql/mysql_equipment.go 的 SaveEquipmentEntryDelta）。
type EquipmentEntryDeltaWriter interface {
	SaveEquipmentEntryDelta(context.Context, EquipmentRecord, []string) error
}

// InventorySlotDeltaKeys 是"本次操作真正改动的槽 key"，按 collection 名
// （= InventoryField 的字面量 "slots" / "warehouse"）分组。
//
// **nil / 缺某个 collection / 某个 collection 为空切片 = "取不到"**：整容器写 + 事件。
// 跨容器移动（主背包 ↔ 仓库）必须同时给出两个 collection 的 key，缺一个即视为取不到，
// 宁可慢，也绝不只写半个容器。
type InventorySlotDeltaKeys map[string][]string

// InventoryWriteFallbackEvent 是"想增量写、但退回了整容器写"的事件。
//
// 为什么必须记：退回整容器只是变慢（= 今天的口径），而"什么都没写"是数据丢失。
// 两者在代码上只差一个分支，所以回退分支必须留下可观测痕迹。
type InventoryWriteFallbackEvent struct {
	// Target: "inventory" | "equipment"，或开关名（unknown_scope_literal 档）。
	Target string
	// Reason: keys_unavailable | collection_unmapped | repository_does_not_support_delta
	//         | unknown_scope_literal
	Reason string
	// Repo 是仓储实现的具体类型（%T），用于区分"是 memory 还是 mysql"。
	Repo   string
	Detail string
}

var (
	writeFallbackMu       sync.RWMutex
	writeFallbackObserver = defaultWriteFallbackObserver
)

// SetInventoryWriteFallbackObserver 替换回退事件观察者；传 nil = 恢复默认（slog 默认日志）。
// 默认观察者永远非 nil，所以回退分支不会静默。
func SetInventoryWriteFallbackObserver(observer func(InventoryWriteFallbackEvent)) {
	writeFallbackMu.Lock()
	defer writeFallbackMu.Unlock()
	if observer == nil {
		writeFallbackObserver = defaultWriteFallbackObserver
		return
	}
	writeFallbackObserver = observer
}

// NotifyInventoryWriteFallback 把一条回退事件交给观察者（各消费包的薄适配层共用）。
func NotifyInventoryWriteFallback(event InventoryWriteFallbackEvent) {
	writeFallbackMu.RLock()
	observer := writeFallbackObserver
	writeFallbackMu.RUnlock()
	if observer == nil {
		return
	}
	observer(event)
}

// defaultWriteFallbackObserver 写 slog 默认日志。日志消息保持 O-B 落地时的原文
// "equip-move-save-fallback"：它已经是既有文档/运维 grep 的锚点
// （_dist/equip-commit-span-optimization-implementation.md:237,455），而 target 字段
// 已经能区分 inventory / equipment 两个消费方。
func defaultWriteFallbackObserver(event InventoryWriteFallbackEvent) {
	fields := []any{"target", event.Target, "reason", event.Reason}
	if event.Repo != "" {
		fields = append(fields, "repo", event.Repo)
	}
	if event.Detail != "" {
		fields = append(fields, "detail", event.Detail)
	}
	slog.Default().Warn("equip-move-save-fallback", fields...)
}

// scopeSetting 是某个开关取值的解析缓存（同一取值只记一次非法字面量事件）。
type scopeSetting struct {
	raw        string
	mode       string
	bad        bool
	notifiedOn bool
}

var (
	writeScopeMu    sync.Mutex
	writeScopeCache = map[string]scopeSetting{}
)

// resolveWriteScope 解析一个开关；无法识别的字面量一律回落 fallback（= 今天的口径）
// 并记一条事件（每个不同取值只记一次，避免每次操作刷屏）。
func resolveWriteScope(envName, fallback string, normalize func(string) (string, bool)) string {
	raw := strings.TrimSpace(os.Getenv(envName))

	writeScopeMu.Lock()
	cached, ok := writeScopeCache[envName]
	if !ok || cached.raw != raw {
		mode, valid := normalize(raw)
		cached = scopeSetting{raw: raw, mode: mode, bad: !valid}
		writeScopeCache[envName] = cached
	}
	shouldNotify := cached.bad && !cached.notifiedOn
	if shouldNotify {
		cached.notifiedOn = true
		writeScopeCache[envName] = cached
	}
	writeScopeMu.Unlock()

	if shouldNotify {
		NotifyInventoryWriteFallback(InventoryWriteFallbackEvent{
			Target: envName,
			Reason: "unknown_scope_literal",
			Detail: envName + "=" + raw,
		})
	}
	if cached.mode == "" {
		return fallback
	}
	return cached.mode
}

// NormalizeInventorySaveScope 归一化 DNFBRIDGE_INVENTORY_SAVE_SCOPE。
// 显式打开值 = slot（别名 slots/dirty/delta/on/1/true/yes/enable/enabled）；
// 今天的口径 = collection（别名空/full/today/legacy/off/0/false/no/disable/disabled/none）；
// 其它取值 ⇒ (collection, false)，即回落今天并记事件。
func NormalizeInventorySaveScope(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "collection", "full", "today", "legacy", "off", "0", "false", "no", "disable", "disabled", "none":
		return SaveScopeCollection, true
	case "slot", "slots", "dirty", "delta", "on", "1", "true", "yes", "enable", "enabled":
		return SaveScopeSlot, true
	default:
		return SaveScopeCollection, false
	}
}

// NormalizeEquipmentSaveScope 归一化 DNFBRIDGE_EQUIPMENT_SAVE_SCOPE。
func NormalizeEquipmentSaveScope(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "collection", "full", "today", "legacy", "off", "0", "false", "no", "disable", "disabled", "none":
		return SaveScopeCollection, true
	case "entry", "entries", "dirty", "delta", "slot", "slots", "on", "1", "true", "yes", "enable", "enabled":
		return SaveScopeEntry, true
	default:
		return SaveScopeCollection, false
	}
}

// InventorySaveScopeMode 返回背包落库粒度（默认 collection = 今天）。
func InventorySaveScopeMode() string {
	return resolveWriteScope(InventorySaveScopeEnvName, SaveScopeCollection, NormalizeInventorySaveScope)
}

// EquipmentSaveScopeMode 返回穿戴表落库粒度（默认 collection = 今天）。
func EquipmentSaveScopeMode() string {
	return resolveWriteScope(EquipmentSaveScopeEnvName, SaveScopeCollection, NormalizeEquipmentSaveScope)
}

// InventoryCollectionName 把单个背包字段映射成仓储口径的 collection 名。
func InventoryCollectionName(field InventoryField) (string, bool) {
	switch field {
	case InventoryFieldSlots:
		return string(InventoryFieldSlots), true
	case InventoryFieldWarehouse:
		return string(InventoryFieldWarehouse), true
	default:
		return "", false
	}
}

// InventoryCollectionNames 返回一批背包字段涉及的 collection 名（按字段注册顺序去重）。
//
// 这是旧 equip.inventoryCollectionName（只接受恰好一个字段）的**多容器**版本：
// "主背包 ↔ 仓库"的一次移动同时改 slots 与 warehouse，落库必须对两个 collection
// 各写一次，否则会退化成整容器写（study §5.2-2 / 风险 R3）。
//
// 空字段集合 = (nil, true)；出现本文件认不得的已注册字段 = (nil, false)。
func InventoryCollectionNames(fields []InventoryField) ([]string, bool) {
	normalized := InventoryFields.Normalize(fields)
	if len(normalized) == 0 {
		return nil, true
	}
	names := make([]string, 0, len(normalized))
	for _, field := range normalized {
		name, ok := InventoryCollectionName(field)
		if !ok {
			return nil, false
		}
		names = append(names, name)
	}
	return names, true
}

// InventorySlotDeltaKeysForFields 把"扁平 key 集合 + 字段集合"折成按 collection 分组的
// 形态，供只动一个容器的调用方（equip 的穿/脱）使用。
//
// 只接受**恰好一个**已识别字段；其余情况（零个/多个/不认识）返回 nil —— 由
// SaveInventoryFieldsScoped 先判字段映射、再判 key，因此"字段不认识"仍然记
// collection_unmapped、"key 给不出来"仍然记 keys_unavailable，与 O-B 落地时的口径
// 逐条一致（equip 的两条回退测试钉的就是这两个 reason）。
func InventorySlotDeltaKeysForFields(fields []InventoryField, keys []string) InventorySlotDeltaKeys {
	normalized := InventoryFields.Normalize(fields)
	if len(normalized) != 1 || len(keys) == 0 {
		return nil
	}
	name, ok := InventoryCollectionName(normalized[0])
	if !ok {
		return nil
	}
	return InventorySlotDeltaKeys{name: keys}
}

// deltaKeysCoverCollections 要求 keys 覆盖 collections 里的**每一个** collection，
// 且每个都非空。任何一个缺失都算"取不到" ⇒ 整容器写。
func deltaKeysCoverCollections(keys InventorySlotDeltaKeys, collections []string) bool {
	if len(keys) == 0 || len(collections) == 0 {
		return false
	}
	for _, collection := range collections {
		if len(keys[collection]) == 0 {
			return false
		}
	}
	return true
}

// SaveInventoryFieldsScoped 是背包侧的**唯一**落库口径入口：
//
//	scope=collection（默认，含一切未知取值）⇒ 今天的整容器重写，逐字节不变；
//	scope=slot ⇒ 只写 keys 里列出的槽，按 fields 涉及的 collection 各调一次增量写
//	             （跨容器移动因此对 slots 与 warehouse 各写一次）。
//
// 回退顺序与 O-B 落地时一致：字段不认识 → key 取不到 → 仓储不支持增量。
func SaveInventoryFieldsScoped(
	ctx context.Context,
	repo InventoryRepository,
	record InventoryRecord,
	fields []InventoryField,
	keys InventorySlotDeltaKeys,
) error {
	if InventorySaveScopeMode() != SaveScopeSlot {
		return SaveInventoryFields(ctx, repo, record, fields...)
	}
	normalized := InventoryFields.Normalize(fields)
	if len(normalized) == 0 {
		// 今天的口径：没有 dirty 字段 ⇒ 一条 SQL 都不发。
		return nil
	}
	collections, mapped := InventoryCollectionNames(normalized)
	if !mapped {
		NotifyInventoryWriteFallback(InventoryWriteFallbackEvent{
			Target: "inventory",
			Reason: "collection_unmapped",
			Repo:   repoTypeName(repo),
			Detail: "character_id=" + record.CharacterID,
		})
		return SaveInventoryFields(ctx, repo, record, fields...)
	}
	if !deltaKeysCoverCollections(keys, collections) {
		NotifyInventoryWriteFallback(InventoryWriteFallbackEvent{
			Target: "inventory",
			Reason: "keys_unavailable",
			Repo:   repoTypeName(repo),
			Detail: "character_id=" + record.CharacterID,
		})
		return SaveInventoryFields(ctx, repo, record, fields...)
	}
	writer, ok := repo.(ItemStackSlotDeltaWriter)
	if !ok {
		NotifyInventoryWriteFallback(InventoryWriteFallbackEvent{
			Target: "inventory",
			Reason: "repository_does_not_support_delta",
			Repo:   repoTypeName(repo),
			Detail: "character_id=" + record.CharacterID,
		})
		return SaveInventoryFields(ctx, repo, record, fields...)
	}
	for _, collection := range collections {
		if err := writer.SaveItemStackSlotDelta(ctx, record, collection, keys[collection]); err != nil {
			return err
		}
	}
	return nil
}

// SaveEquipmentFieldsScoped 是穿戴表侧的落库口径入口：
// 默认（含一切未知取值）= 今天的整容器写；scope=entry 且 keys 非空 = 只写这些 entry。
func SaveEquipmentFieldsScoped(
	ctx context.Context,
	repo EquipmentRepository,
	record EquipmentRecord,
	keys []string,
) error {
	if EquipmentSaveScopeMode() != SaveScopeEntry {
		return SaveEquipmentFields(ctx, repo, record, EquipmentFieldEntries)
	}
	if len(keys) == 0 {
		NotifyInventoryWriteFallback(InventoryWriteFallbackEvent{
			Target: "equipment",
			Reason: "keys_unavailable",
			Repo:   repoTypeName(repo),
			Detail: "character_id=" + record.CharacterID,
		})
		return SaveEquipmentFields(ctx, repo, record, EquipmentFieldEntries)
	}
	writer, ok := repo.(EquipmentEntryDeltaWriter)
	if !ok {
		NotifyInventoryWriteFallback(InventoryWriteFallbackEvent{
			Target: "equipment",
			Reason: "repository_does_not_support_delta",
			Repo:   repoTypeName(repo),
			Detail: "character_id=" + record.CharacterID,
		})
		return SaveEquipmentFields(ctx, repo, record, EquipmentFieldEntries)
	}
	return writer.SaveEquipmentEntryDelta(ctx, record, keys)
}

func repoTypeName(repo any) string {
	if repo == nil {
		return ""
	}
	return fmt.Sprintf("%T", repo)
}
