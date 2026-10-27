package dnfbridge

import (
	"fmt"
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件是"深渊房间（hell party room）"在 op28 DUNGEON_INFO / op29 START_MAP
// 上的最小投影。改动前这两条包对深渊是**恒定的缺席投影**：
//
//	dungeon_entry_packets.go  HellPartyRoomX = 0xff, HellPartyRoomY = 0xff
//	dungeon_entry_packets.go  HellPartyValue = 0（零值）
//	dungeon_entry_packets.go  Value2        = 0（零值）
//	dungeon_entry_packets.go  op29 HellPartyMode = 0（零值）、HellPartyFogFlag = 0
//
// 现场症状（操作者清单第 ⑧/⑤ 条）：已经做完 `[hell quest]` 的角色可以进深渊
// （准入那一层已由 dungeon_hell_party_entry.go 修好），但**进图后看不到深渊房间**。
//
// ── 厂商口径（唯一依据，`a21zigai\ServerS4A21\Server\DfoServer`）─────────────
//
//  1. 深渊房间的坐标不是算出来的，是 PVF 直接声明的：每个 `[maze]` 的
//     `[seal door map index]`（深渊房间那张地图）+ `[seal door pos]`（该房间在
//     迷宫里的坐标）。厂商 `GameWorld\Dungeon.cs:904-1029 FindHellMapRoom` /
//     `TryBuildSealDoorHellPartyRoom`：
//
//     hellMapId <= 0 || position.Length < 2                     → 拒
//     FindNormalMapIdForRoom(maze, x, y) <= 0（(x,y) 不是真房间）→ 拒
//     !IsHellPartyMap(hellMapId)（该图没有柱子）                 → 拒
//
//     这三条判据本文件逐条保留（见 resolveDungeonHellRoom）。
//
// 2) op28 的下发点在厂商 `Network\Handlers\Dungeon\DungeonEntryHandler.cs:1323-1335`：
//
//			hellPartyRoomX:   run.HellMode ? run.HellMapX : (byte)0xFF,
//			hellPartyRoomY:   run.HellMode ? run.HellMapY : (byte)0xFF,
//			hellPartyEnabled: run.HellMode ? (ushort)1 : (ushort)0,
//			value2:           run.HellMode ? (byte)0x0B : (byte)0,
//
//		   本仓 `currentDungeonInfo` 与厂商
//		   `Network\Builders\Dungeon\DungeonNotificationBuilder.cs:33-85 BuildDungeonInfo`
//		   逐字段同名同序，只是 90CN EXE 在 `+5` 多一个 u16（本仓 `EntryOption`），
//		   其后所有字段整体偏移 +2：厂商 hellPartyRoomX/Y `+8/+9` ⇒ 本仓 `+10/+11`；
//		   厂商 hellPartyEnabled(ushort) `+12`（紧跟可变 minimap 组之后）⇒ 本仓
//		   `HellPartyValue` `+14`；厂商 value1 缺省 `0x000C` ⇒ 本仓 `DungeonValue = 12`
//		   （现场常量，逐字节一致）；厂商 value2 ⇒ 本仓 `Value2`。**因此只搬这三个
//		   字段的取值，不搬厂商的行偏移。**
//
//	 3. op29 START_MAP 的 offset 7 是"标准副本模式标记"
//	    （`Network\Handlers\Dungeon\DungeonMapHandler.cs:509-513`）：
//
//	    // A21 START_MAP offset 7 is the standard-dungeon mode marker.
//	    // Normal rooms use 2 even when abyss mode is disabled; zero is
//	    // not the A21 standard value and moves the minimap projection.
//	    var hellPartyMapMode = run.HellMode ? run.HellPartyMode : (byte)2;
//	    var startMapFogFlag  = run.HellMode ? (byte)1 : (byte)0;
//
//	    本仓 `currentDungeonStartMap` 的前缀与厂商 `BuildStartMap`
//	    (`DungeonNotificationBuilder.cs:138-161`) 逐字节同序（X, Y, layered, seed u32,
//	    HellPartyMode, UnknownAfterHellPartyMode, roomStateValue u32, roomStateFlag,
//	    map u32…），所以 offset 7 就是本仓 `HellPartyMode`，**两个客户端同布局**
//	    （op29 不存在 op28 那种 `+2` 差异）。
//
//	 4. 厂商 `Game\Dungeon\DungeonEntryAdmissionApplicationService.cs:381-385
//	    ResolveHellPartyMode`：`requestFlag != 0 ? requestFlag : 随机`；随机来自
//	    `HellPartyData.ResolveManualHellPartyMode`（`GameWorld\HellPartyData.cs:56-73`），
//	    读 PVF `[difficulty]` 的 A/B 权重，**权重不可用时厂商写死回退 1**
//	    （`:68-69 if (total <= 0) return 1;`）。本文件只承担这个回退值，
//	    权重随机的完整口径不在本次改动范围（见 resolveDungeonHellPartyMode 注释）。
//
// ── 开关 ──────────────────────────────────────────────────────────────────
//
// DNFBRIDGE_HELL_ROOM
//
//	vendor（默认，= 修复档）：按上面 2)/3) 下发深渊房间坐标、hellPartyEnabled、
//	                         value2=0x0B，并把 op29 offset 7 摆到标准值 2。
//	legacy（一键回退）      ：逐字节回到改动前（0xff/0xff、0、0、offset 7 = 0）。
//
// 默认 = 修复档（本仓既定纪律：默认即修复行为，回退档必须显式写 legacy）。
const dungeonHellRoomEnv = "DNFBRIDGE_HELL_ROOM"

// dungeonHellRoomValueLegacy 是唯一被承认的回退取值。
const dungeonHellRoomValueLegacy = "legacy"

type dungeonHellRoomMode int

const (
	// dungeonHellRoomVendor 是默认档，也是修复档（零值 = 修复）。
	dungeonHellRoomVendor dungeonHellRoomMode = iota
	// dungeonHellRoomLegacy 是改动前的缺席投影，一键回退用。
	dungeonHellRoomLegacy
)

// readDungeonHellRoomMode 解析 DNFBRIDGE_HELL_ROOM。
// **只有显式 `legacy` 才回退**；未设置/空/其它任何值都取默认档（修复），
// 与 readHellBlockedSlotsMode / readDungeonEntrySelectionMode 相反方向的同一约定：
// 那两条的零值是"改动前"，本条零值是"修复"，所以判据取 `== legacy` 而不是 `== 修复值`。
func readDungeonHellRoomMode(value string) dungeonHellRoomMode {
	if strings.EqualFold(strings.TrimSpace(value), dungeonHellRoomValueLegacy) {
		return dungeonHellRoomLegacy
	}
	return dungeonHellRoomVendor
}

func (mode dungeonHellRoomMode) String() string {
	if mode == dungeonHellRoomLegacy {
		return dungeonHellRoomValueLegacy
	}
	return "vendor"
}

func (mode dungeonHellRoomMode) enabled() bool { return mode == dungeonHellRoomVendor }

// dungeonHellRoom 是一次进图里冻结下来的深渊房间，对应厂商
// `DungeonData.HellPartyRoomInfo` 里被 op28/op29 用到的三个字段
// （`GameWorld\Dungeon.cs:136-146`）。只在进图那一刻算一次，之后只读。
type dungeonHellRoom struct {
	// MapID 是深渊房间该用的地图（PVF `[seal door map index]`，例如地轨中心
	// time_plaza.dgn 的 60083）。柱子（`[special passive object]` + `[hellparty]`）
	// 就长在这张图上。
	MapID int64
	// X/Y 是该房间在迷宫里的坐标（PVF `[seal door pos]`，例如 `3 0`）。
	X int
	Y int
}

// resolveDungeonHellRoom 逐条对应厂商 `Dungeon.cs:904-1029 FindHellMapRoom`
// 的 ordinary 分支 + `TryBuildSealDoorHellPartyRoom` 的三条硬判据。
//
// 本次**不**实现季节门（`[season seal door map index]`）：本仓 worldmap 根本没解析
// 该 section（`parse_dungeon.go:297-302` 只读 appear rate / map index / pos），
// 而厂商那条分支还要 `HellPartyData.IsSeasonHellPartyEnabled()`，属于"活动路线"，
// 不在操作者清单第 ⑤ 条范围内。缺字段时厂商自己也会落到 ordinary 分支
// （`Dungeon.cs:945-953`），因此这里的行为与厂商在"无季节门"数据上的行为一致。
func resolveDungeonHellRoom(maze *worldmap.Maze, table *worldmap.Table) (dungeonHellRoom, bool) {
	room, found, _ := resolveDungeonHellRoomRead(maze, table)
	return room, found
}
func resolveDungeonHellRoomRead(maze *worldmap.Maze, table *worldmap.Table) (dungeonHellRoom, bool, error) {
	if maze == nil {
		return dungeonHellRoom{}, false, nil
	}
	hellMapID := maze.SealDoorMapIndex
	if !hellMapID.Set || hellMapID.Value <= 0 {
		return dungeonHellRoom{}, false, nil
	}
	if len(maze.SealDoorPosition) < 2 {
		return dungeonHellRoom{}, false, nil
	}
	x := int(maze.SealDoorPosition[0])
	y := int(maze.SealDoorPosition[1])
	if !mazeHasRoomAt(maze, x, y) {
		return dungeonHellRoom{}, false, nil
	}
	// 厂商 `IsHellPartyMap`：那张图上必须真有柱子，否则宁可不下发坐标
	// （`Dungeon.cs:995-1004`：`normalMapId <= 0 || !hasHellParty` 一律拒）。
	hasPillar, readErr := hellPartyMapCarriesPillarRead(table, hellMapID.Value)
	if readErr != nil {
		return dungeonHellRoom{}, false, readErr
	}
	if !hasPillar {
		return dungeonHellRoom{}, false, nil
	}
	return dungeonHellRoom{MapID: hellMapID.Value, X: x, Y: y}, true, nil
}

// mazeHasRoomAt 是厂商 `FindNormalMapIdForRoom(maze, x, y) <= 0` 判据的投影：
// 该坐标必须真的是这个迷宫里的一个房间（且该房间有地图）。
func mazeHasRoomAt(maze *worldmap.Maze, x, y int) bool {
	for _, spec := range maze.MapSpecifications {
		if int(spec.Coordinate.X) == x && int(spec.Coordinate.Y) == y && len(spec.MapIDs) > 0 && spec.MapIDs[0] > 0 {
			return true
		}
	}
	return false
}

// hellPartyMapCarriesPillar 是厂商 `Dungeon.cs:1031-1048 IsHellPartyMap`：
// 该 map 至少有一个带 `[hellparty]` 子行的 `[special passive object]`（就是柱子）。
// 本仓 worldmap 已经解析好这层元数据（`parse_map.go:197 attachHellParty`、
// `types.go:99-108 HellPartyEntry`），这里只读不改。
func hellPartyMapCarriesPillar(table *worldmap.Table, mapID int64) bool {
	found, _ := hellPartyMapCarriesPillarRead(table, mapID)
	return found
}
func hellPartyMapCarriesPillarRead(table *worldmap.Table, mapID int64) (bool, error) {
	if table == nil {
		return false, nil
	}
	value, ok, err := table.FindMapWithError(mapID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	for _, object := range value.SpecialPassiveObjects {
		if len(object.HellParty) != 0 {
			return true, nil
		}
	}
	return false, nil
}

// resolveDungeonHellPartyMode 对应厂商 `ResolveHellPartyMode`
// (`DungeonEntryAdmissionApplicationService.cs:381-385`)：
// `requestFlag != 0 ? requestFlag : ResolveManualHellPartyMode()`。
//
// 本仓 op16 的 `EntryOption` 是 u16，低字节 = 厂商 `HellPartyRequestFlag`（body[5]），
// 高字节 = 厂商 `HellPartyDifficultyFlag`（body[6]），所以"请求里的难度"就是高字节。
//
// 高字节为 0（玩家没点名难度）时，厂商走 `ResolveManualHellPartyMode` 的
// PVF `[difficulty]` A/B 权重随机，权重不可用时**写死回退 1**
// （`HellPartyData.cs:65-72`）。本函数只实现那条回退值：读权重需要
// `hellparty` 脚本目录（`HellPartyData.ReadHellPartyScript`），
// 不在本次改动范围 —— 也就是说"没点名难度"的现场固定拿 A 档，
// 而不是按权重随机。这是**已知偏差**，已记入 changlog，取随机需要另立一条改动。
func resolveDungeonHellPartyMode(entryOption uint16) byte {
	if difficulty := byte(entryOption >> 8); difficulty != 0 {
		return difficulty
	}
	return 1
}

// dungeonStartMapStandardHellPartyMode 是厂商写死的"标准副本模式标记"
// （`DungeonMapHandler.cs:509-513`）：普通房间即使没开深渊也用 2。
const dungeonStartMapStandardHellPartyMode byte = 2

// dungeonHellRequestByteEnv 选择"深渊请求标志读哪一字节"。
//
//	""/"flag1"（默认，= 修复档）：低字节取 body[7]（厂商 `Flag1` = `HellPartyRequestFlag`），
//	    高字节取 body[8]（厂商 `Flag2` = 深渊难度）。
//	"entry_option"（回退档）：原样用 body[5:7]（改动前口径）。
//
// 为什么默认必须换（现场取证，见 `_dist/hell-pillar-diagnosis.md`）：真实 90CN 客户端
// **245/245 次** op16 的 body[5:7] 恒为 `00 00`，而 A21 的 `0xFFFF` 哨兵出现在 body[9:11]
// （A21 在 [7:9]）—— 因为本客户端在 [5:7] 多插了一个 u16，真实标志整体后移 2 字节。
// 读 EntryOption 的后果是 `dungeonRequestedHell` 在线上**永远 false** ⇒ 深渊房间图从不生效
// ⇒ 时间广场没有柱子。语义旁证：body[8]=1 只出现在训练房（dungeon 5000），而厂商定义
// `PracticeMode => HellPartyRequestFlag == 0 && Flag2 != 0`；body[7]=1 只出现在
// 地轨中心（area 39）的 310/313 等图上，正是"挑战深渊派对"。
const dungeonHellRequestByteEnv = "DNFBRIDGE_HELL_REQUEST_BYTE"

// dungeonHellRequestByteEntryOptionValue 是回退档取值。
const dungeonHellRequestByteEntryOptionValue = "entry_option"

// dungeonHellRequestByteUsesFlag1 报告当前是否处于修复档（默认）。
func dungeonHellRequestByteUsesFlag1() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv(dungeonHellRequestByteEnv)), dungeonHellRequestByteEntryOptionValue)
}

// dungeonHellRequestOption 把请求按当前档位归一化成既有的 u16 编码：
// 低字节 = 是否请求深渊派对，高字节 = 深渊难度。下游三个判据
// （`dungeonRequestedHell` / `resolveDungeonHellPartyMode` / `dungeonStartMapHellState`）
// 因此**一行都不用改**，档位只在这一个入口切换。
func dungeonHellRequestOption(request dungeoncmd.SelectDungeonRequest) uint16 {
	if !dungeonHellRequestByteUsesFlag1() {
		return request.EntryOption
	}
	return uint16(request.HellPartyRequestFlag) | uint16(request.HellPartyDifficultyFlag)<<8
}

// dungeonRequestedHell 报告这条 op16 是不是深渊派对请求。
// 判据用**低字节**：厂商 `manualHellParty = req.HellPartyRequestFlag != 0`
// （`DungeonEntryHandler.cs:825-831`）。低字节的来源由 `dungeonHellRequestOption` 决定：
// 修复档下它就是 body[7]，回退档下是 body[5]（线上恒为 0）。
func dungeonRequestedHell(entryOption uint16) bool { return byte(entryOption) != 0 }

// dungeonStartMapHellState 给出 op29 START_MAP offset 7（HellPartyMode）与
// 之后的 fog 字节（本仓 `HellPartyFogFlag`，厂商 `startMapFogFlag`）。
// 逐字节对应 `DungeonMapHandler.cs:512-513` 两行。
// resolveDungeonHellRoomForEntry 是"这条 op16 要不要深渊房间"的唯一判据入口。
// 只有修复档 + 真深渊请求（低字节 = `HellPartyRequestFlag`）才会去读 PVF。
func (s *Service) resolveDungeonHellRoomForEntry(mode dungeonHellRoomMode, option uint16, dungeon worldmap.Dungeon, maze int, table *worldmap.Table) (dungeonHellRoom, bool) {
	room, found, _ := s.resolveDungeonHellRoomForEntryRead(mode, option, dungeon, maze, table)
	return room, found
}
func (s *Service) resolveDungeonHellRoomForEntryRead(
	mode dungeonHellRoomMode,
	entryOption uint16,
	dungeon worldmap.Dungeon,
	mazeIndex int,
	table *worldmap.Table,
) (dungeonHellRoom, bool, error) {
	if !mode.enabled() || !dungeonRequestedHell(entryOption) {
		return dungeonHellRoom{}, false, nil
	}
	return resolveDungeonHellRoomRead(mazeForHellRoom(dungeon, mazeIndex), table)
}

// applyDungeonHellRoomMap 把深渊房间那一间的房图换成 `[seal door map index]` 那张图。
//
// 位置有讲究：必须在 `BuildDungeonLayout` **之后**、`NewDungeonRun` **之前**，
// 与锦标赛 `prepareTournamentDungeonRun`（`dungeon_tournament_entry.go:285`）同一处边界。
//
// 只换房图就够：房间场景完全从房图派生（`dungeon_session.go:717-731` 的
// `dungeonRoomScene` 直接取 `Monsters` / `PassiveObjects` / `SpecialPassiveObjects`），
// 柱子（`map/metrocenter/time_plaza/hell_time_plaza.map` 上的
// `[special passive object] 30528`）会作为 type-9 actor 进入 op29。
//
// 坐标不是这个迷宫的房间、或那张图不在真档 `map/map.lst` 里 ⇒ **硬失败**，
// 绝不凭空造一间屋子、也不悄悄退回普通图。
func applyDungeonHellRoomMap(
	topology *worldmap.DungeonTopology,
	table *worldmap.Table,
	room dungeonHellRoom,
) error {
	if table == nil {
		return errDungeonWorldMapUnavailable
	}
	mapValue, found, readErr := table.FindMapWithError(room.MapID)
	if readErr != nil {
		return readErr
	}
	if !found {
		return fmt.Errorf("%w: hell_room_map=%d", worldmap.ErrMapUnresolved, room.MapID)
	}
	if err := topology.OverrideRoomMap(
		worldmap.RoomCoordinate{X: int64(room.X), Y: int64(room.Y)},
		worldmap.ResolvedMap{Map: mapValue, Source: worldmap.ResolutionHellSealDoor},
	); err != nil {
		return fmt.Errorf("override hell room map: %w", err)
	}
	return nil
}

// hellRoomMode 是 DNFBRIDGE_HELL_ROOM 的归一化取值（见本文件头"开关"一节）。
func (s *Service) hellRoomMode() dungeonHellRoomMode { return s.options.dungeonHellRoom }

// mazeForHellRoom 安全取迷宫：越界一律 nil（resolveDungeonHellRoom 对 nil 返回 false，
// 于是 op28/op29 逐字节回到改动前，而不是发一个编出来的坐标）。
func mazeForHellRoom(dungeon worldmap.Dungeon, mazeIndex int) *worldmap.Maze {
	if mazeIndex < 0 || mazeIndex >= len(dungeon.Mazes) {
		return nil
	}
	return &dungeon.Mazes[mazeIndex]
}

func dungeonStartMapHellState(mode dungeonHellRoomMode, entryOption uint16) (byte, byte) {
	if !mode.enabled() {
		// legacy：逐字节回到改动前（两个字节都恒为 0）。
		return 0, 0
	}
	if !dungeonRequestedHell(entryOption) {
		return dungeonStartMapStandardHellPartyMode, 0
	}
	return resolveDungeonHellPartyMode(entryOption), 1
}
