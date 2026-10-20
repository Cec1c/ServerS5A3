package dnfbridge

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件是"深渊柱子（操作者清单 item 2）打破后没有掉落物"这一条的两个**互相独立**的
// 改动，全部按操作者任务书要求**默认 = 今天的线上行为**（这是本仓"默认即修复档"纪律的
// 一次**显式例外**：客户端在 op29/actor 行数上崩过，而本文件的机制**未经实机验证**，
// 见下"为什么默认不修"）。
//
// ── 已证机制（`_dist/pillar-drop-wire-diagnosis.md`，本轮不重新推导）─────────────
//
//  1. "柱子没掉落 = 我们少发 op29 的 extra entry"**已被证否**：map 60083 的柱子行
//     `30528 594 301 0 1 \`[hellparty]\` …` 没有 `[item]` 子行，厂商自己的掉落计划器
//     （`PassiveObjectDropPlanningService.cs:138`/`:146-155` 只认 `[item]`）在同一张图上
//     也是 `extraCount = 0`；现场帧（`packet_log.txt:73366`，pkt_seq=905，body 254）
//     与厂商在这一段逐字节相同。
//  2. 真正的缺口是**柱子破坏事件根本没到服务端**：房间窗口 `73364..73700` 里
//     `RECV msg_id=39` 恰好 11 条 = 6 只房图怪 + 5 只深渊波次行，没有第 12 条给柱子的。
//     我们这一侧柱子**没有任何注册**，所以既认不出它、也就无从接到它的破坏事件。
//  3. 厂商的注册/接收口径（**本轮实现的依据**，全部有 file:line）：
//     · 注册 = 房间地图的 `Maze.SpecialPassiveObjects`（map 的 `[special passive object]`
//     列表）**按列表下标记账**，`DungeonInstance.cs:378-404`
//     （`_mapOwnedActorDeaths.Add(index, fact)`），并且同处 `:392` 注释明确
//     “MAP-owned actors have no START_MAP wire sequence”——**柱子不发 op29 actor 行**。
//     · 接收 = 仍然走 DIE_MONSTER（op39 / msg_id 39），
//     `Network\Handlers\Dungeon\DungeonCombatHandler.cs:56-153`：
//     `localIndex`（我们解码器的 `RuntimeObjectKey` 低 16 位）就是**对象 code**（30528），
//     map-owned 签名是 `body[4:6] == 0xFFFF`
//     （`Network\Parsers\Dungeon\DieMonsterRequest.cs:53-54`，
//     本仓解码器把同一字段叫 `OwnerObjectKey`，`dungeoncmd/request.go:230`），
//     命中判据是把 code 逐行比对 `Maze.SpecialPassiveObjects`
//     （`DungeonCombatHandler.cs:378-404` 的同一列表、`ShouldTreatAsPassiveObject:171-186`
//     还要求"目标不是已公告的 actor 序号"）。
//     · 该分支**不回 op38**（`DungeonCombatHandler.cs:117-152` 只写日志 + 走任务/机制钩子）。
//  4. `10158700`（= 0x009B026C，dgn `[special passive object item]` 组，权重 10000）是该
//     dgn 里唯一的候选物品，但它的消费口（map 的 `[item]` 子行）在 map 60083 上**不存在**
//     ⇒ **"柱子应该掉 10158700"本身未证**。它只作为 probe 档的探针值出现，不许当成结论。
//
// ── 本轮实现（两个独立开关，都默认今天）────────────────────────────────────────
//
// ① `DNFBRIDGE_MAP_OWNED_PASSIVE_OBJECT`（默认 `legacy` = 今天；`register` = 新行为）
//
//	把房图 `[special passive object]` 列表按**下标**注册成 map-owned 表（下标 → 对象
//	code，与厂商 `DungeonInstance.cs:378-404` 同键序），并在 op39 上接住
//	`owner 签名 0xFFFF + 目标 code 命中注册表` 的破坏事件：记进本房间台账（同一下标只认
//	一次，重复=幂等忽略并写 `…-duplicate`）、**不发 op38**、然后走掉落钩子。
//	掉落钩子 = 若这个下标在 START_MAP 里投影过掉落行（只可能由 ② 产生），把它标记为
//	“已释放”并写 `…-drop-released`；没有行就写 `…-drop-absent`（这正是 `probe` 档的
//	判别信号：说明"事件到了、但这一档没有行"）。
//	默认 `legacy` 时**整个函数在一行 env 判定处返回**，op39 的处理路径逐字节等于今天。
//
//	⚠ 未证（写进报告）：现场那次会话里客户端**没有**发这根柱子的 op39。厂商材料能钉死
//	"如果客户端发，它长什么样、我们该怎么接"，但**钉不死**"当前客户端到底发不发"。
//	因此本项默认关闭；它的可观测判据只能由操作者的 A/B 实机给出。
//
// ② `DNFBRIDGE_PASSIVE_OBJECT_DROP_ROW`（默认/其它任何值 = `legacy` = 今天：extraCount 恒 0；
//
//	   `probe` = 为深渊房间的 `[hellparty]` 柱子补发**一条** 19 字节掉落行，并把这条行
//	   按 `SceneSlot = GlobalSeq` 登记进我们的掉落表）。
//
//	   行字节 = 厂商 19 字节布局（`DungeonNotificationBuilder.cs:191-203`）：
//
//		00 | <slot u16 LE> | 6c 02 9b 00 | 01 00 00 00 | 00 00 | 00 | 00 00 | 00 00 | 00
//		u8 ObjectIndex | u16 GlobalSeq | u32 itemTemplateId | u32 stackCount |
//		u16 endurance | u8 ampType | u16 ampValue | u16 0 | u8 0
//
//	   现场锚点：`3:0` 的那条 op29 body 254 → **273** 字节（+19，"计数字节本来就存在"），
//	   hex 里出现 `6c029b00`。若物品在 PVF 里解析不出来，**不发这一行**（宁可一个字节都不发，
//	   也不发一条拾取不了的掉落行），并写 `…-row-blocked reason=item_unresolved`。
//
// ③ `DNFBRIDGE_START_MAP_EXTRA_ENTRY_WIDTH`（默认未设置 = `auto`；`u16`/`vendor` =
//
//	厂商 19 字节；`u32`/`current` = 本仓今天的 21 字节）
//	只决定**编码器**写哪种布局。`auto` 下：没有行时不变（extraCount=0，零字节差），
//	`probe` 时取厂商 19 字节（诊断 §4.1 明确要求 probe 不能混入第二个变量）。
//	显式 `u32` + `probe` ⇒ 21 字节行（body 275）——这正是操作者用来**不用改代码就 settle
//	"21 还是 19"** 的那一档。默认档下本开关不产生任何线上字节。
//
// ── 为什么默认不修（显式记录）──────────────────────────────────────────────────
//
// 本仓现行纪律是"默认即修复行为，回退档必须显式写值"（`dungeon_special_object_parent_actor.go:53-55`）。
// 本文件两个档**故意反过来**，理由：① 机制未证（客户端是否上报、上报形状未知，见上）；
// ② 客户端历史上在 op29/actor 行数变化上崩过（`current_item_list_send.go:222-230` 同类
// 记录），而 ② 会直接把深渊房间的 op29 从 254 改成 273 字节。所以在拿到操作者那次实机
// A/B 之前，线上行为必须停在今天。
const (
	passiveObjectDropRowEnv          = "DNFBRIDGE_PASSIVE_OBJECT_DROP_ROW"
	startMapExtraEntryWidthEnv       = "DNFBRIDGE_START_MAP_EXTRA_ENTRY_WIDTH"
	mapOwnedPassiveObjectModeEnvName = "DNFBRIDGE_MAP_OWNED_PASSIVE_OBJECT"
)

// 各开关被承认的字面量。未列出的取值一律落到默认档（今天是"不动线上行为"那一档）。
const (
	passiveObjectDropRowLegacyValue     = "legacy"
	passiveObjectDropRowProbeValue      = "probe"
	startMapExtraEntryWidthAutoValue    = "auto"
	startMapExtraEntryWidthVendorValue  = "u16"
	startMapExtraEntryWidthVendorAlias  = "vendor"
	startMapExtraEntryWidthCurrentValue = "u32"
	startMapExtraEntryWidthCurrentAlias = "current"
	mapOwnedPassiveObjectLegacyValue    = "legacy"
	mapOwnedPassiveObjectRegisterValue  = "register"
)

// currentDungeonPassiveObjectProbeItemID 是 probe 行的物品模板 id：`10158700` = 0x009B026C。
//
// 依据只有 `_dist/pillar-drop-wire-diagnosis.md` §1.4/§4.1：dgn 的
// `[special passive object item]` 组里唯一的物品、权重 10000。**未证**它就是柱子该掉的
// 东西（消费口 `[item]` 子行在 map 60083 上不存在）⇒ 报告与日志里都不许把它写成结论。
const currentDungeonPassiveObjectProbeItemID uint32 = 10158700

var (
	errDungeonPassiveObjectDropSlotRange = errors.New("dnf dungeon passive object drop scene slot is unavailable")
	errDungeonPassiveObjectDropKeyRange  = errors.New("dnf dungeon passive object drop object key is unavailable")
)

type passiveObjectDropRowMode int

const (
	// passiveObjectDropRowLegacy 是默认档：一个行都不发，逐字节等于今天。
	passiveObjectDropRowLegacy passiveObjectDropRowMode = iota
	// passiveObjectDropRowProbe 是显式探针档：只发一条柱子掉落行。
	passiveObjectDropRowProbe
)

type startMapExtraEntryWidth int

const (
	// startMapExtraEntryWidthAuto 是默认档：没有行时不产生字节；probe 时取厂商 19 字节。
	startMapExtraEntryWidthAuto startMapExtraEntryWidth = iota
	// startMapExtraEntryWidthVendor 强制厂商 19 字节布局。
	startMapExtraEntryWidthVendor
	// startMapExtraEntryWidthCurrent 强制本仓今天的 21 字节布局。
	startMapExtraEntryWidthCurrent
)

type mapOwnedPassiveObjectMode int

const (
	// mapOwnedPassiveObjectLegacy 是默认档：op39 的 map-owned 分支完全不接管。
	mapOwnedPassiveObjectLegacy mapOwnedPassiveObjectMode = iota
	// mapOwnedPassiveObjectRegister 按厂商口径注册并按 code 接住柱子破坏事件。
	mapOwnedPassiveObjectRegister
)

func readDungeonPassiveObjectDropRowMode(value string) passiveObjectDropRowMode {
	if strings.EqualFold(strings.TrimSpace(value), passiveObjectDropRowProbeValue) {
		return passiveObjectDropRowProbe
	}
	return passiveObjectDropRowLegacy
}

func readDungeonStartMapExtraEntryWidth(value string) startMapExtraEntryWidth {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case startMapExtraEntryWidthVendorValue, startMapExtraEntryWidthVendorAlias:
		return startMapExtraEntryWidthVendor
	case startMapExtraEntryWidthCurrentValue, startMapExtraEntryWidthCurrentAlias:
		return startMapExtraEntryWidthCurrent
	default:
		return startMapExtraEntryWidthAuto
	}
}

func readDungeonMapOwnedPassiveObjectMode(value string) mapOwnedPassiveObjectMode {
	if strings.EqualFold(strings.TrimSpace(value), mapOwnedPassiveObjectRegisterValue) {
		return mapOwnedPassiveObjectRegister
	}
	return mapOwnedPassiveObjectLegacy
}

func dungeonPassiveObjectDropRowModeFromEnv() passiveObjectDropRowMode {
	return readDungeonPassiveObjectDropRowMode(os.Getenv(passiveObjectDropRowEnv))
}

func dungeonStartMapExtraEntryWidthFromEnv() startMapExtraEntryWidth {
	return readDungeonStartMapExtraEntryWidth(os.Getenv(startMapExtraEntryWidthEnv))
}

func dungeonMapOwnedPassiveObjectModeFromEnv() mapOwnedPassiveObjectMode {
	return readDungeonMapOwnedPassiveObjectMode(os.Getenv(mapOwnedPassiveObjectModeEnvName))
}

func (mode passiveObjectDropRowMode) String() string {
	if mode == passiveObjectDropRowProbe {
		return passiveObjectDropRowProbeValue
	}
	return passiveObjectDropRowLegacyValue
}

func (mode startMapExtraEntryWidth) String() string {
	switch mode {
	case startMapExtraEntryWidthVendor:
		return startMapExtraEntryWidthVendorValue
	case startMapExtraEntryWidthCurrent:
		return startMapExtraEntryWidthCurrentValue
	default:
		return startMapExtraEntryWidthAutoValue
	}
}

func (mode mapOwnedPassiveObjectMode) String() string {
	if mode == mapOwnedPassiveObjectRegister {
		return mapOwnedPassiveObjectRegisterValue
	}
	return mapOwnedPassiveObjectLegacyValue
}

// emitsRow 报告该档位是否为柱子发行一条 START_MAP extra entry。
func (mode passiveObjectDropRowMode) emitsRow() bool {
	return mode == passiveObjectDropRowProbe
}

// registers 报告该档位是否接管 op39 的 map-owned 被动对象破坏事件。
func (mode mapOwnedPassiveObjectMode) registers() bool {
	return mode == mapOwnedPassiveObjectRegister
}

// vendorLayout 把宽度档折算成"这一帧到底写 19 还是 21 字节"。
//
// `auto` 的关键语义：**没有行的时候没有任何可观测差**（extraCount 恒 0）；一旦有行
// （今天只有 probe 会产生行），按诊断 §4.1 必须写厂商 19 字节，否则这一轮实验会同时
// 混入两个变量。
func (width startMapExtraEntryWidth) vendorLayout(hasRow bool) bool {
	switch width {
	case startMapExtraEntryWidthVendor:
		return true
	case startMapExtraEntryWidthCurrent:
		return false
	default:
		return hasRow
	}
}

// currentDungeonMapOwnedPassiveObject 是房图 `[special passive object]` 里的一行。
//
// ObjectIndex 就是厂商的记账键（`DungeonInstance.cs:382-404` 的列表下标、以及
// `PassiveObjectDropEntry.ObjectIndex` 的语义：**不是对象 id**）。
type currentDungeonMapOwnedPassiveObject struct {
	ObjectIndex      int
	ObjectCode       int64
	HellPartyEntries int
	SpawnCount       int
}

// currentDungeonPassiveObjectDropRow 是一条已经投影进 op29 的柱子掉落行。
type currentDungeonPassiveObjectDropRow struct {
	NativeObjectKey bool
	ObjectIndex     int
	SceneSlot       uint16
	ObjectKey       uint32
	ItemID          uint32
	StackCount      uint32
	Released        bool
}

func (row *currentDungeonPassiveObjectDropRow) startMapEntry() currentDungeonStartMapExtraEntry {
	sequence := uint32(row.SceneSlot)
	if row.NativeObjectKey {
		sequence = row.ObjectKey
	}
	return currentDungeonStartMapExtraEntry{
		PassiveObjectIndex: byte(row.ObjectIndex),
		GlobalSequence:     sequence,
		ItemID:             row.ItemID,
		StackCount:         row.StackCount,
		// 无 ItemCore（普通可叠放物品）：Endurance / AmplifyType / AmplifyValue 与
		// 厂商硬编码的两个 extended 字段全部为 0（`DungeonNotificationBuilder.cs:198-202`）。
	}
}

// currentDungeonMapOwnedPassiveObjectRegistration 是一个房间的 map-owned 注册表 +
// 破坏台账 + 已投影的掉落行。键是**列表下标**（与厂商 `_mapOwnedActorDeaths` 同键序）。
type currentDungeonMapOwnedPassiveObjectRegistration struct {
	RoomKey      runtimeDungeonRoomKey
	Objects      []currentDungeonMapOwnedPassiveObject
	Deaths       map[int]bool
	Rows         []*currentDungeonPassiveObjectDropRow
	VendorWidth  bool
	DropRowProbe bool
}

func (registration *currentDungeonMapOwnedPassiveObjectRegistration) objectByCode(objectCode int64) (currentDungeonMapOwnedPassiveObject, bool) {
	if registration == nil || objectCode <= 0 {
		return currentDungeonMapOwnedPassiveObject{}, false
	}
	// 厂商 `DungeonInstance.cs:382-404` 是逐下标顺序比对、取**第一个**命中。
	for _, object := range registration.Objects {
		if object.ObjectCode == objectCode {
			return object, true
		}
	}
	return currentDungeonMapOwnedPassiveObject{}, false
}

// hellPillarObject 找出"深渊柱子"那一行：房图 `[special passive object]` 里**带行内
// `[hellparty]`** 的唯一一行。
//
// map 60083 的原文只有这一行（`_dist/hellroom-two-pillars-diagnosis.md` §1：
// `30528 594 301 0 1 \`[hellparty]\` …`），它**没有** `[item]`/`[monster]` 子行
// （⇒ 厂商的掉落计划器会跳过它，见本文件头 §1）。不唯一/找不到就返回 false：宁可一个
// 字节都不发，也不猜哪根是柱子。
func (registration *currentDungeonMapOwnedPassiveObjectRegistration) hellPillarObject() (currentDungeonMapOwnedPassiveObject, bool) {
	if registration == nil {
		return currentDungeonMapOwnedPassiveObject{}, false
	}
	var found currentDungeonMapOwnedPassiveObject
	matches := 0
	for _, object := range registration.Objects {
		if object.ObjectCode <= 0 || object.HellPartyEntries <= 0 {
			continue
		}
		matches++
		found = object
	}
	if matches != 1 {
		return currentDungeonMapOwnedPassiveObject{}, false
	}
	return found, true
}

func (registration *currentDungeonMapOwnedPassiveObjectRegistration) dropRow(objectIndex int) *currentDungeonPassiveObjectDropRow {
	if registration == nil {
		return nil
	}
	for _, row := range registration.Rows {
		if row != nil && row.ObjectIndex == objectIndex {
			return row
		}
	}
	return nil
}

// currentDungeonMapOwnedPassiveObjects 从场景里**逐行照抄** `[special passive object]`，
// 不发明任何值：下标取列表序、code 取 PVF 的 ObjectID、`[hellparty]` 子行数取解析结果。
func currentDungeonMapOwnedPassiveObjects(scene worldmap.DungeonRoomScene) []currentDungeonMapOwnedPassiveObject {
	if len(scene.SpecialPassiveObjects) == 0 {
		return nil
	}
	objects := make([]currentDungeonMapOwnedPassiveObject, 0, len(scene.SpecialPassiveObjects))
	for index, object := range scene.SpecialPassiveObjects {
		objects = append(objects, currentDungeonMapOwnedPassiveObject{
			ObjectIndex:      index,
			ObjectCode:       object.ObjectID,
			HellPartyEntries: len(object.HellParty),
			SpawnCount:       len(object.Spawns),
		})
	}
	return objects
}

// registerCurrentDungeonMapOwnedPassiveObjects 建/复用本房间的注册表（按房间键幂等）。
func (runtime *runtimeDungeonState) registerCurrentDungeonMapOwnedPassiveObjects(
	scene worldmap.DungeonRoomScene,
) *currentDungeonMapOwnedPassiveObjectRegistration {
	if runtime == nil {
		return nil
	}
	key := runtimeDungeonRoomKeyFromScene(scene)
	if existing := runtime.MapOwnedPassiveObjects[key]; existing != nil {
		return existing
	}
	objects := currentDungeonMapOwnedPassiveObjects(scene)
	if len(objects) == 0 {
		return nil
	}
	if runtime.MapOwnedPassiveObjects == nil {
		runtime.MapOwnedPassiveObjects = make(map[runtimeDungeonRoomKey]*currentDungeonMapOwnedPassiveObjectRegistration, 4)
	}
	registration := &currentDungeonMapOwnedPassiveObjectRegistration{
		RoomKey: key,
		Objects: objects,
		Deaths:  make(map[int]bool, len(objects)),
	}
	runtime.MapOwnedPassiveObjects[key] = registration
	return registration
}

// currentDungeonMapOwnedPassiveObjectRegistration 是**纯读**（不建表）。
func (runtime *runtimeDungeonState) currentDungeonMapOwnedPassiveObjectRegistration(
	scene worldmap.DungeonRoomScene,
) *currentDungeonMapOwnedPassiveObjectRegistration {
	if runtime == nil {
		return nil
	}
	return runtime.MapOwnedPassiveObjects[runtimeDungeonRoomKeyFromScene(scene)]
}

// currentDungeonPassiveObjectDropEntries 供 op29 构建器**纯读**取本次要写的那几行。
// 第二个返回值是这一帧要用的布局（true = 厂商 19 字节）。
func currentDungeonPassiveObjectDropEntries(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
) ([]currentDungeonStartMapExtraEntry, bool) {
	registration := runtime.currentDungeonMapOwnedPassiveObjectRegistration(scene)
	if registration == nil || len(registration.Rows) == 0 {
		return nil, false
	}
	entries := make([]currentDungeonStartMapExtraEntry, 0, len(registration.Rows))
	instance := runtime.lockPartyDungeonInstance()
	defer unlockPartyDungeonInstance(instance)
	for _, row := range registration.Rows {
		if row == nil {
			continue
		}
		if runtime.DropOwner != nil {
			if drop := runtime.DropOwner.mapOwnedPassiveObjectDrop(row.SceneSlot); drop != nil && drop.Status == runtimeDungeonDropConsumed {
				continue
			}
		}
		entries = append(entries, row.startMapEntry())
	}
	if len(entries) == 0 {
		return nil, false
	}
	return entries, registration.VendorWidth
}

// currentDungeonPassiveObjectProbeItemDefinition 解析 probe 行的物品定义。
//
// **不发明**：只在 PVF 的 stackable/equipment 列表里解析得到时才算数；解析不出来就
// 整条行都不发（调用方写具名日志）。
func (s *Service) currentDungeonPassiveObjectProbeItemDefinition() (dungeonDropItemDefinition, error) {
	monsterCatalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		return dungeonDropItemDefinition{}, err
	}
	dropCatalog, err := monsterCatalog.DropCatalog()
	if err != nil {
		return dungeonDropItemDefinition{}, err
	}
	definition, err := dropCatalog.ResolveItem(currentDungeonPassiveObjectProbeItemID)
	if err != nil {
		return dungeonDropItemDefinition{}, err
	}
	if definition.ItemID != currentDungeonPassiveObjectProbeItemID {
		return dungeonDropItemDefinition{}, fmt.Errorf(
			"%w: resolved=%d want=%d",
			errDungeonDropItemUnresolved,
			definition.ItemID,
			currentDungeonPassiveObjectProbeItemID,
		)
	}
	return definition, nil
}

// prepareCurrentDungeonPassiveObjectDrops 是 op29 之前的**唯一入口**：
// 建 map-owned 注册表（①）、必要时投影一条柱子掉落行并登记掉落（②）。
//
// 两个档都是 `legacy` 时第一行就返回：一个字节、一个 map、一个随机数都不动。
func (s *Service) prepareCurrentDungeonPassiveObjectDrops(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	source string,
) error {
	if runtime == nil {
		return nil
	}
	return s.prepareCurrentDungeonPassiveObjectDropsAfterActors(session, runtime, scene, source, runtime.NextObjectKey)
}

// A move has already planned its target actors without committing their keys.
// Passive rewards must start after that range, even before the room is entered.
func (s *Service) prepareCurrentDungeonPassiveObjectDropsAfterActors(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	source string,
	minimumObjectKey uint32,
) error {
	if runtime == nil {
		return nil
	}
	if err := s.prepareCurrentDungeonPVFPassiveItemDrops(session, runtime, scene, source, minimumObjectKey); err != nil {
		return err
	}
	rowMode := dungeonPassiveObjectDropRowModeFromEnv()
	mapOwnedMode := dungeonMapOwnedPassiveObjectModeFromEnv()
	if !rowMode.emitsRow() && !mapOwnedMode.registers() {
		return nil
	}
	registration := runtime.registerCurrentDungeonMapOwnedPassiveObjects(scene)
	if registration == nil {
		return nil
	}
	if !rowMode.emitsRow() || currentTeamRaidGroundDropsDisabled(runtime) {
		return nil
	}
	// 非深渊房间**静默跳过**（与 `sendCurrentHellPartyMonsterInfo` 同一风格）：probe 档
	// 只在深渊房间产生字节，别的房间连日志都不加，免得每次换房刷一条。
	if !currentHellPartySceneIsHellRoom(runtime, scene) {
		return nil
	}
	pillar, found := registration.hellPillarObject()
	if !found {
		s.logGameEvent(session, "game-dungeon-passive-object-drop-row-skipped",
			"source", source,
			"dungeon_id", runtime.Dungeon.ID,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"special_object_count", len(registration.Objects),
			"switch", rowMode.String(),
			"reason", "no_unique_special_passive_object_row_with_inline_hellparty")
		return nil
	}
	width := dungeonStartMapExtraEntryWidthFromEnv()
	if existing := registration.dropRow(pillar.ObjectIndex); existing != nil {
		// 幂等：本会话重建 op29（barrier 前后各一次）或**队友先投影过**同一间房时，
		// 复用同一条行/同一个 scene slot，绝不发第二行。
		s.logGameEvent(session, "game-dungeon-passive-object-drop-row-reused",
			"source", source,
			"dungeon_id", runtime.Dungeon.ID,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"object_index", pillar.ObjectIndex,
			"object_id", pillar.ObjectCode,
			"scene_slot", existing.SceneSlot,
			"item_id", existing.ItemID,
			"switch", rowMode.String(),
			"width", width.String())
		return nil
	}
	definition, err := s.currentDungeonPassiveObjectProbeItemDefinition()
	if err != nil {
		s.logGameEvent(session, "game-dungeon-passive-object-drop-row-blocked",
			"source", source,
			"dungeon_id", runtime.Dungeon.ID,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"object_index", pillar.ObjectIndex,
			"object_id", pillar.ObjectCode,
			"item_id", currentDungeonPassiveObjectProbeItemID,
			"switch", rowMode.String(),
			"reason", "item_unresolved_in_real_pvf_and_a_row_without_a_registered_drop_must_not_be_sent",
			"error", err)
		return nil
	}
	runtime.commitDungeonNextObjectKey(max(runtime.NextObjectKey, minimumObjectKey))
	drop, sceneSlot, err := runtime.registerCurrentDungeonPassiveObjectDrop(
		runtimeDungeonRoomKeyFromScene(scene),
		pillar.ObjectIndex,
		definition,
		1,
	)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-passive-object-drop-row-blocked",
			"source", source,
			"dungeon_id", runtime.Dungeon.ID,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"object_index", pillar.ObjectIndex,
			"object_id", pillar.ObjectCode,
			"item_id", definition.ItemID,
			"switch", rowMode.String(),
			"reason", "drop_table_registration_failed",
			"error", err)
		return nil
	}
	registration.VendorWidth = width.vendorLayout(true)
	registration.DropRowProbe = true
	registration.Rows = append(registration.Rows, &currentDungeonPassiveObjectDropRow{
		ObjectIndex: pillar.ObjectIndex,
		SceneSlot:   sceneSlot,
		ObjectKey:   drop.ObjectKey,
		ItemID:      definition.ItemID,
		StackCount:  1,
	})
	s.logGameEvent(session, "game-dungeon-passive-object-drop-row-registered",
		"source", source,
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"object_index", pillar.ObjectIndex,
		"object_id", pillar.ObjectCode,
		"hellparty_entries", pillar.HellPartyEntries,
		"spawn_count", pillar.SpawnCount,
		"scene_slot", sceneSlot,
		"drop_object_key", drop.ObjectKey,
		"item_id", definition.ItemID,
		"stack_count", 1,
		"row_bytes", 19,
		"vendor_width", registration.VendorWidth,
		"width_switch", width.String(),
		"switch", rowMode.String(),
		"item_id_proven", false,
		"source_vendor", "DungeonNotificationBuilder_cs_188-203_and_PassiveObjectDropProjectionService_cs_127-128")
	return nil
}

// handleCurrentDungeonMapOwnedPassiveObjectDeathLocked 接住 map-owned 被动对象（柱子）的
// 破坏事件。返回 (handled, err)：handled=true 表示这条 op39 已经被本分支消费（调用方直接
// return，不再走普通怪/非敌对 actor 路径）。
//
// 判据全部来自厂商（file:line 见文件头 §3），一条都不新增：
//   - map-owned 签名 body[4:6] == 0xFFFF（本仓解码器的 `OwnerObjectKey`）；
//   - 目标不是**已公告的 actor 序号**（`ShouldTreatAsPassiveObject:182-183`），否则交给既有路径；
//   - 目标 code 命中本房间注册表（`DungeonInstance.cs:382-404`，取第一个命中）。
//
// 调用方必须持有 session.dungeon.mu。
func (s *Service) handleCurrentDungeonMapOwnedPassiveObjectDeathLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	request dungeoncmd.DieMonsterRequest,
) (bool, error) {
	if runtime == nil || runtime.Room == nil {
		return false, nil
	}
	registration := runtime.currentDungeonMapOwnedPassiveObjectRegistration(scene)
	if registration == nil {
		return false, nil
	}
	if request.OwnerObjectKey != ^uint16(0) {
		return false, nil
	}
	// 厂商只比对 u16 `localIndex`（`DieMonsterRequest.cs:51`）。本仓解码器把
	// localIndex(u16)+userId(u16) 合成 u32 `RuntimeObjectKey`，所以这里取低 16 位。
	target := uint32(uint16(request.RuntimeObjectKey))
	if runtime.Room.ContainsAnnouncedActorObjectKey(target) {
		return false, nil
	}
	if _, announced := runtime.Room.AnnouncedMonster(target); announced {
		return false, nil
	}
	object, found := registration.objectByCode(int64(target))
	if !found {
		return false, nil
	}
	if registration.Deaths[object.ObjectIndex] {
		s.logGameEvent(session, "game-dungeon-map-owned-passive-object-duplicate",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"object_index", object.ObjectIndex,
			"object_id", object.ObjectCode,
			"object_key", request.RuntimeObjectKey,
			"switch", dungeonMapOwnedPassiveObjectModeFromEnv().String(),
			"reason", "map_owned_passive_object_already_recorded_for_this_room_index",
			"source_vendor", "DungeonCombatHandler_cs_103-110_duplicate_ignored")
		return true, nil
	}
	registration.Deaths[object.ObjectIndex] = true
	drop, released := runtime.releaseCurrentDungeonPassiveObjectDrop(registration, object.ObjectIndex)
	s.logGameEvent(session, "game-dungeon-map-owned-passive-object-destroyed",
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"object_index", object.ObjectIndex,
		"object_id", object.ObjectCode,
		"object_key", request.RuntimeObjectKey,
		"map_owned_signature", true,
		"request_layout", request.Layout,
		"drop_row_projected", registration.dropRow(object.ObjectIndex) != nil,
		"death_ack_sent", false,
		"switch", dungeonMapOwnedPassiveObjectModeFromEnv().String(),
		"reason", "current_exe_op39_map_owned_passive_object_signature_matched_room_special_passive_object_code",
		"source_vendor", "DungeonCombatHandler_cs_78-152_map_owned_branch_sends_no_op38")
	if released && drop != nil {
		s.logGameEvent(session, "game-dungeon-map-owned-passive-object-drop-released",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"object_index", object.ObjectIndex,
			"object_id", object.ObjectCode,
			"scene_slot", drop.SceneSlot,
			"drop_object_key", drop.ObjectKey,
			"item_id", drop.Item.ItemID,
			"stack_count", drop.Amount,
			"pickup_lookup", "scene_slot_or_drop_object_key",
			"source", "map_owned_passive_object_break_released_the_pre_generated_row")
	} else {
		s.logGameEvent(session, "game-dungeon-map-owned-passive-object-drop-absent",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"object_index", object.ObjectIndex,
			"object_id", object.ObjectCode,
			"row_mode", dungeonPassiveObjectDropRowModeFromEnv().String(),
			"reason", "no_start_map_drop_row_was_projected_for_this_object_index",
			"next_step", "set_DNFBRIDGE_PASSIVE_OBJECT_DROP_ROW_probe_to_project_one_row")
	}
	return true, nil
}

// releaseCurrentDungeonPassiveObjectDrop 是"破坏 → 掉落"这一层的钩子：把该下标已经投影
// 进 op29 的那条掉落行标记为已释放，并把掉落表里的那条记录返回给日志。
//
// 本轮**只做记账**、不额外发任何"掉落物落到地上"的包：厂商在这一层也只有
// `PassiveObjectDropProjectionService.cs:122-140` 的预生成行 + `run.Drops[SceneSlot]`，
// 客户端在柱子被打破时自己把它落到地上（`_dist/pillar-drop-wire-diagnosis.md` §3.1）。
// 由服务端直接补一条地面掉落属于**另一套未证语义**，不在本轮。
func (runtime *runtimeDungeonState) releaseCurrentDungeonPassiveObjectDrop(
	registration *currentDungeonMapOwnedPassiveObjectRegistration,
	objectIndex int,
) (*runtimeDungeonDrop, bool) {
	row := registration.dropRow(objectIndex)
	if row == nil {
		return nil, false
	}
	row.Released = true
	if runtime == nil || runtime.DropOwner == nil {
		return nil, false
	}
	drop := runtime.DropOwner.mapOwnedPassiveObjectDrop(row.SceneSlot)
	if drop == nil {
		return nil, false
	}
	return drop, true
}

// registerCurrentDungeonPassiveObjectDrop 把一条预生成掉落登记进掉落表，语义与厂商
// `PassiveObjectDropProjectionService.cs:122-140` 一致：entry 既进 START_MAP，也进
// `run.Drops[SceneSlot]`（本仓的对应物是 `runtimeDungeonDropOwner`，键是 objectKey，
// 同时按 scene slot 建索引；见 dungeon_drop_runtime.go）。
//
// scene slot 取我们自己的发号器（`owner.nextSceneSlot`），objectKey 取共享的
// objectKey 发号器 —— 两个都必须在实例锁下读改写（队伍共享实例下账本是共用的）。
func (runtime *runtimeDungeonState) registerCurrentDungeonPassiveObjectDrop(
	room runtimeDungeonRoomKey,
	objectIndex int,
	item dungeonDropItemDefinition,
	amount uint32,
) (*runtimeDungeonDrop, uint16, error) {
	if runtime == nil || item.ItemID == 0 || amount == 0 {
		return nil, 0, errDungeonDropNotFound
	}
	if currentTeamRaidGroundDropsDisabled(runtime) {
		return nil, 0, errDungeonDropNotFound
	}
	owner := runtime.DropOwner
	if owner == nil {
		owner = newRuntimeDungeonDropOwner()
	}
	// `dungeonNextObjectKey` 自己取实例锁 ⇒ 必须在 `lockPartyDungeonInstance` **之前**读。
	nextObjectKey := runtime.dungeonNextObjectKey()
	if nextObjectKey == 0 || nextObjectKey > math.MaxUint16 {
		return nil, 0, fmt.Errorf("%w: next=%d", errDungeonPassiveObjectDropKeyRange, nextObjectKey)
	}
	instance := runtime.lockPartyDungeonInstance()
	defer unlockPartyDungeonInstance(instance)
	// 队友先投影过同一间房的同一根柱子 ⇒ 复用他那个 slot/key（同一条行、同一份账本）。
	if existing := owner.mapOwnedPassiveObjectDropByRoom(room, objectIndex); existing != nil {
		runtime.DropOwner = owner
		return existing, existing.SceneSlot, nil
	}
	sceneSlot := owner.nextSceneSlot
	if sceneSlot == 0 || sceneSlot == math.MaxUint16 {
		return nil, 0, fmt.Errorf("%w: next=%d", errDungeonPassiveObjectDropSlotRange, sceneSlot)
	}
	if _, conflict := owner.byObjectKey[nextObjectKey]; conflict {
		return nil, 0, fmt.Errorf("%w: object_key=%d", errDungeonDropObjectConflict, nextObjectKey)
	}
	drop := &runtimeDungeonDrop{
		ObjectKey:             nextObjectKey,
		SceneSlot:             sceneSlot,
		Item:                  item,
		Amount:                amount,
		Room:                  room,
		Status:                runtimeDungeonDropAvailable,
		MapOwnedPassiveObject: true,
		MapOwnedPassiveIndex:  objectIndex,
	}
	if err := owner.registerBatch([]*runtimeDungeonDrop{drop}, sceneSlot+1); err != nil {
		return nil, 0, err
	}
	runtime.DropOwner = owner
	runtime.commitDungeonNextObjectKeyLocked(nextObjectKey + 1)
	return drop, sceneSlot, nil
}
