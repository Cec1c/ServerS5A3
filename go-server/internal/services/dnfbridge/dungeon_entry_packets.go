package dnfbridge

import (
	"errors"
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

var (
	errDungeonEntryModeUnsupported   = errors.New("dnf dungeon entry mode is not supported by the current packet owner")
	errDungeonBossCoordinateRequired = errors.New("dnf dungeon boss coordinate is required")
	errDungeonBossCoordinateRange    = errors.New("dnf dungeon boss coordinate exceeds current EXE range")
	errDungeonMazeIndexRange         = errors.New("dnf dungeon maze index exceeds current EXE range")
	errDungeonRandomizedObjectOwner  = errors.New("dnf dungeon randomized object owner is required")
)

const (
	currentDungeonInfoNotification  uint16 = 28
	currentDungeonStartNotification uint16 = 29
)

type currentDungeonEntryPackets struct {
	DungeonInfo []byte
	StartMap    []byte
	// StartMapSuppressed 为真表示"这张图按厂商口径**不生成**普通 START_MAP(op29) 体"。
	// 此时 `StartMap` 必须为空，房间与 actor 的公告由锦标赛 NOTI 386/387 承担
	// （依据见 `dungeon_tournament_entry.go` 的
	// `currentTournamentMapSuppressesOrdinaryStartMap`）。
	//
	// 发送方**必须**读这个标志：压掉 op29 的同时必须确认替代公告已经发出，
	// 否则就是"少发一段然后当成功"（禁止）。
	StartMapSuppressed bool
	// StartMapSuppressedReason 是具名依据（厂商 file:line），只用于日志与断言。
	StartMapSuppressedReason string
}

// currentDungeonEntryValidationMode 选择 op16 请求体的校验口径。
type currentDungeonEntryValidationMode int

const (
	// currentDungeonEntryStrictValidation 是改动前口径（也是默认档）：
	// `entry_option != 0` 一律归进 errDungeonEntryModeUnsupported。
	currentDungeonEntryStrictValidation currentDungeonEntryValidationMode = iota
	// currentDungeonEntryAllowHellOption 是 `DNFBRIDGE_HELL_BLOCKED_SLOTS=on` 档：
	// op16 body[5:7] 就是厂商的 `HellPartyRequestFlag`/难度标志
	// （`Parsers/Dungeon/SelectDungeonRequest.cs:14 HellPartyRequestFlag => Flag1`），
	// 它不是"当前 EXE 不支持的模式"。放行它，玩家点「挑战深渊派对」才真的进得去副本
	// （操作者清单第 69 条）；其余每一个判据仍然逐条保持 fail-closed。
	currentDungeonEntryAllowHellOption
)

// currentDungeonEntrySelectionModeMode 是 op16 `body[7]`（我方 `SelectionMode`）
// 的**第二条独立校验轴**，由 `DNFBRIDGE_DUNGEON_ENTRY_SELECTION_MODE` 归一化得到。
type currentDungeonEntrySelectionModeMode int

const (
	// currentDungeonEntrySelectionModeStrict 是改动前口径（也是默认档/零值）：
	// `body[7] != 0` 一律归进 errDungeonEntryModeUnsupported。
	currentDungeonEntrySelectionModeStrict currentDungeonEntrySelectionModeMode = iota
	// currentDungeonEntrySelectionModeAllow 是 `DNFBRIDGE_DUNGEON_ENTRY_SELECTION_MODE=allow`
	// 档：`body[7]` 取任意值都不再算"不支持的模式"；**其余每一个判据逐条保持 fail-closed**。
	currentDungeonEntrySelectionModeAllow
)

// ── op16 `body[7]`（SelectionMode）为什么需要一条独立的放行轴 ─────────────────
//
// 现场证据（线上 `/opt/dnf90/logs/` 的 RECV 行，只读；RECV = **13 字节头 + 21 字节体**，
// 日志 `len=34` ⇒ 体的偏移要从行尾倒着取）：
//
//  1. 报文 = `ENUM_CMDPACKET_SELECT_DUNGEON`（msg_id **16** / `0x0010`），21 字节体，
//     由当前 EXE `sub_342EA40` 写出（日志
//     `source=current_exe_sub_342EA40_fixed_21_byte_writer`）。
//  2. **地轨中心（worldmap area 39 / `worldmap/MetroCenter.wdm`）整片区域**的副本都用
//     `body[7] = 1` 发请求，被我们自己的前置校验全部拒掉
//     （`failure_reason_byte=19` = `0x13` = `currentSelectDungeonInvalidSelectionStateReason`
//     = 厂商 `DungeonAdmissionReject.InvalidSelectionState`）：
//     `RECV … msg_id=16 … len=34 hex=01100022000000b6493aec9e00390100000000000100ffff00000000000000000000`
//     体 = `39 01 00 00 | 00 | 00 00 | 01 | 00 | ff ff | 00 ×10`
//     = dungeon **313** / difficulty 0 / entry_option 0 / **body[7]=1** / body[8]=0 / token 0xFFFF。
//     紧随其后的拒绝行：
//     `EVENT kind=game-dungeon-select-blocked … dungeon_id=313 … reason=dungeon_entry_packet_preflight_failed
//     failure_class=entry_mode_unsupported failure_reason_byte=19 reply_sent=true … selection_mode=1 …`。
//     全库实测出现过 mode=1 的副本：**310 / 311 / 312 / 313 / 314**（= area 39「地轨中心」的全部
//     可选副本，含魔界裂缝所在的同一片区域）；`selection_mode` 从未出现 ≥ 2 的值。
//  3. 被我们**接受**的普通请求 `body[7] = 0`，同会话逐字节对照：
//     `RECV … msg_id=16 … len=34 hex=011000220000004d91c4876101780000000200000000ffff00000000000000000000`
//     体 = `78 00 00 00 | 02 | 00 00 | 00 | 00 | ff ff | 00 ×10`
//     = dungeon **120** / difficulty 2 / entry_option 0 / **body[7]=0** / body[8]=0 / token 0xFFFF。
//     ⇒ 两条体**只差 `body[4]`（difficulty 2 vs 0）与 `body[7]`（0 vs 1）**，其余 19 字节完全相同。
//     这就是"313 点了进不去、120 点得进去"的全部线上字节差异。
//
// 4. **厂商不校验这个字节**（`ServerS4A21-完整包-20260923\源码\服务端\Server\DfoServer\`）：
//
//   - `Network\Parsers\Dungeon\SelectDungeonRequest.cs:36-63`：`Parse` 只要求
//     `body.Length >= 9`，把 `body[5]`/`body[6]` 读成 `Flag1`/`Flag2`
//     （`:14 HellPartyRequestFlag => Flag1`、`:15 HellPartyDifficultyFlag => Flag2`），
//     把 **`body[7:9]` 整段**读成 `A21Sentinel`，并把 `body[9:]` 记成
//     `TrailingLength`/`HasNonZeroTrailingBytes` —— **没有任何一处拿这些值去拒绝请求**。
//
//   - 唯一的取值点 `Network\Handlers\Dungeon\DungeonEntryHandler.cs:280-289`
//     在 `ShouldIgnoreClearedAntonFollowerDuplicateSelect` 里比较
//     `Flag1 != 0 || Flag2 != 0 || A21Sentinel != 0xFFFF || TrailingLength != 6 ||
//     HasNonZeroTrailingBytes`，**不满足就 `return false`（= 不忽略这条重复选图），
//     请求照常往下走**，不是拒绝。
//
//   - 该函数的调用链与"安徒恩顺序副本的从属重复选图抑制"有关（同文件
//     `run.Instance?.SequentialDefinition`），与"能不能进图"无关。
//
//   - `Run` 侧只消费 `HellPartyRequestFlag`/`HellPartyDifficultyFlag`
//     （`DungeonEntryHandler.cs:2641-2648`），**从不读 `body[7]`**。
//     ⇒ 厂商对 `body[7] != 0` 是**照常放行**（既不拒绝、也不分支）。
//
// 变更史：我们当初加这条**不是**"见过 mode=1 所以针对它"，而是同一批 21 字节写入器的
// fail-closed 口径顺手写死的。changlog 里唯一被实测证明必须特殊放行的是**训练房 dungeon
// 5000 的 `body[8]=1`** —— 线上体 `881300000000000001ffff00000000000000000000` ⇒
// `[4]=0 [5]=0 [6]=0 [7]=0 [8]=1 [9:11]=FFFF`（见 `runtimeStateSupported` 那段注释）。
// `body[7]` 当时**没有任何实测样本**，就与其余未证明字段一起取了"非 0 即拒"；
// 现在有了 area 39 的实测样本 ⇒ 这条对整片区域是误拒。
//
// 默认档 = `currentDungeonEntrySelectionModeStrict` = **逐字节今天**（照旧拒绝）；
// 一键回退 = 删除该 env（或写成 `off`）。
const dungeonEntrySelectionModeEnv = "DNFBRIDGE_DUNGEON_ENTRY_SELECTION_MODE"

// dungeonEntrySelectionModeValueAllow 是唯一被承认的放行取值。
const dungeonEntrySelectionModeValueAllow = "allow"

// readDungeonEntrySelectionMode 解析 `DNFBRIDGE_DUNGEON_ENTRY_SELECTION_MODE`。
// **只有显式 `allow` 才放行**；未设置/空/其它任何值（含 `on`/`true`/`1`）都取默认档
// `strict`，保证"默认 = 今天逐字节"这条承诺不会被一个手滑的别名破坏
// （与 `readHellBlockedSlotsMode` 同一约定）。
func readDungeonEntrySelectionMode(value string) currentDungeonEntrySelectionModeMode {
	if strings.EqualFold(strings.TrimSpace(value), dungeonEntrySelectionModeValueAllow) {
		return currentDungeonEntrySelectionModeAllow
	}
	return currentDungeonEntrySelectionModeStrict
}

// allowsNonZeroSelectionMode 报告该档位是否放行 `body[7] != 0`。
func (mode currentDungeonEntrySelectionModeMode) allowsNonZeroSelectionMode() bool {
	return mode == currentDungeonEntrySelectionModeAllow
}

func (mode currentDungeonEntrySelectionModeMode) String() string {
	if mode == currentDungeonEntrySelectionModeAllow {
		return dungeonEntrySelectionModeValueAllow
	}
	return "strict"
}

func buildCurrentDungeonEntryPackets(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
) (currentDungeonEntryPackets, error) {
	return buildCurrentDungeonEntryPacketsForModes(
		runtime, scene, currentDungeonEntryStrictValidation, currentDungeonEntrySelectionModeStrict)
}

// buildCurrentDungeonEntryPacketsForModes 是唯一实现：两条**互相独立**的校验轴
// （`mode` = op16 `body[5:7]` 的 `entry_option` 轴，`selectionMode` = op16 `body[7]`
// 的 `SelectionMode` 轴）各自由自己的开关归一化后传进来。
func buildCurrentDungeonEntryPacketsForModes(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	mode currentDungeonEntryValidationMode,
	selectionMode currentDungeonEntrySelectionModeMode,
) (currentDungeonEntryPackets, error) {
	if runtime == nil || runtime.Room == nil || runtime.Session == nil {
		return currentDungeonEntryPackets{}, errDungeonWorldMapUnavailable
	}
	if err := validateCurrentDungeonEntryRequestForModes(runtime.Request, mode, selectionMode); err != nil {
		return currentDungeonEntryPackets{}, err
	}
	if runtime.Dungeon.ID <= 0 || runtime.Dungeon.ID > int64(^uint32(0)) {
		return currentDungeonEntryPackets{}, fmt.Errorf("%w: dungeon=%d", errDungeonStartMapMapIDRange, runtime.Dungeon.ID)
	}
	if runtime.MazeIndex < 0 || runtime.MazeIndex > int(^uint8(0)) {
		return currentDungeonEntryPackets{}, fmt.Errorf("%w: maze=%d", errDungeonMazeIndexRange, runtime.MazeIndex)
	}
	if !runtime.BossSet {
		return currentDungeonEntryPackets{}, errDungeonBossCoordinateRequired
	}
	if runtime.BossCoordinate.X < 0 || runtime.BossCoordinate.X > 0xff || runtime.BossCoordinate.Y < 0 || runtime.BossCoordinate.Y > 0xff {
		return currentDungeonEntryPackets{}, fmt.Errorf("%w: boss=%s", errDungeonBossCoordinateRange, runtime.BossCoordinate)
	}
	if runtime.MazeIndex >= len(runtime.Dungeon.Mazes) {
		return currentDungeonEntryPackets{}, fmt.Errorf("%w: maze=%d count=%d", worldmap.ErrMazeNotFound, runtime.MazeIndex, len(runtime.Dungeon.Mazes))
	}
	if count := len(runtime.Dungeon.Mazes[runtime.MazeIndex].RandomizedObjects); count != 0 {
		if runtime.randomizedObjects == nil || runtime.randomizedObjects.Plan == nil {
			return currentDungeonEntryPackets{}, fmt.Errorf("%w: count=%d", errDungeonRandomizedObjectOwner, count)
		}
		if err := runtime.randomizedObjects.validate(runtime.Dungeon, runtime.MazeIndex); err != nil {
			return currentDungeonEntryPackets{}, fmt.Errorf("%w: %v", errDungeonRandomizedObjectOwner, err)
		}
	}
	// Map [special passive object] child rows such as [item], [trap], [quest],
	// and [hellparty] are script metadata of the parent object, and the parent
	// object itself is **not** a START_MAP row: the client creates it from the
	// MAP (vendor DungeonActorTemplateProjector.cs:254-255) and only the inline
	// [monster] children become actor rows. `planRuntimeDungeonExtendedActors`
	// therefore emits no type-9 parent row by default (see
	// dungeon_special_object_parent_actor.go; `start_map` is the one-key
	// rollback that restores it byte-for-byte). ExtraEntries are a different
	// table of materialized item drops and must not be invented from these
	// metadata rows. Retained rows stay in the room runtime for their later
	// feature owners and never block the typed op28/op29 entry.

	// These values are protocol sentinels, not map fallbacks. The current EXE
	// branches on packetSeed == UINT32_MAX to skip the absent hell-party state.
	// 深渊房间（见 dungeon_hell_room.go 文件头）：厂商 DungeonEntryHandler.cs:1329-1334
	// 只在 run.HellMode 时把 hellPartyRoomX/Y 换成深渊房间坐标，同时
	// hellPartyEnabled=1、value2=0x0B；没开深渊时这三个字段保持本仓今天的值
	// （0xff/0xff、0、0）。HellRoomSet 只可能由 DNFBRIDGE_HELL_ROOM 修复档 +
	// 真深渊请求 + PVF 三条判据全过才会为真，所以 legacy 档逐字节回到改动前。
	// 注意 PacketSeed 仍然照旧发 UINT32_MAX —— 厂商那条分支同样不改它，
	// 本仓也没有新证据支持改它。
	hellRoomX, hellRoomY := byte(0xff), byte(0xff)
	var hellPartyValue uint16
	var dungeonInfoValue2 byte
	if runtime.HellRoomSet {
		hellRoomX = byte(runtime.HellRoom.X)
		hellRoomY = byte(runtime.HellRoom.Y)
		hellPartyValue = 1
		dungeonInfoValue2 = 0x0B
	}
	infoPacket := currentDungeonInfo{
		PairGroups:     runtime.randomizedObjects.minimapGroups(),
		DungeonID:      uint32(runtime.Dungeon.ID),
		Difficulty:     runtime.Request.Difficulty,
		EntryOption:    runtime.Request.EntryOption,
		MazeIndex:      byte(runtime.MazeIndex),
		BossX:          byte(runtime.BossCoordinate.X),
		BossY:          byte(runtime.BossCoordinate.Y),
		HellPartyRoomX: hellRoomX,
		HellPartyRoomY: hellRoomY,
		DungeonValue:   12,
		HellPartyValue: hellPartyValue,
		Value2:         dungeonInfoValue2,
		PacketSeed:     ^uint32(0),
	}
	dungeonInfoBody, err := infoPacket.Build()
	if err != nil {
		return currentDungeonEntryPackets{}, fmt.Errorf("build current dungeon-info body: %w", err)
	}
	// 锦标赛地图：厂商**不组装也不发送**普通 START_MAP（`DungeonMapHandler.cs:778-779`
	// 的 `startMapBody = Array.Empty<byte>(); if (!isBloodAltarMap && !isTournamentMap)`）。
	// 这里必须在校验通过之后、构建之前返回：投到这张图上的锦标赛对手来自
	// `[tournament enemies]`（语法 `PartyCount kind Code Strength Name`，**没有 rank 列**），
	// 在普通 start-map 的 rank→actorType 闸门（`dungeon_start_map.go:326-349`）下
	// 不可表达 —— 旧行为就是在下一行抛 `errDungeonStartMapMonsterRank`，把整次入场
	// 挡在 op28/op29 之前（线上 P8 现场：`failure_class=entry_preflight_unavailable`）。
	//
	// 注意：**闸门本身一个字节都没改**。同一个空 rank 在非锦标赛路径上仍然照样报错
	// （既有 `dungeon_entry_smoke_test.go` / `dungeon_start_map_test.go` 钉的正是那条语义）。
	if reason, suppressed := currentTournamentMapSuppressesOrdinaryStartMap(runtime, scene); suppressed {
		return currentDungeonEntryPackets{
			DungeonInfo:              dungeonInfoBody,
			StartMapSuppressed:       true,
			StartMapSuppressedReason: reason,
		}, nil
	}
	partyMemberIndex := byte(0xff)
	if runtime.partyMemberIndexed {
		partyMemberIndex = runtime.partyMemberIndex
	}
	// 柱子（map-owned 特殊被动对象）的预生成掉落行：只由
	// `Service.prepareCurrentDungeonPassiveObjectDrops`（probe 档）登记过才会有内容；
	// 默认档下 `registration == nil ⇒ nil/false`，这一帧与今天逐字节相同。
	// 见 dungeon_passive_object_drop.go。
	passiveObjectDropEntries, passiveObjectDropVendorWidth := currentDungeonPassiveObjectDropEntries(runtime, scene)
	startPacket, err := currentDungeonStartMapFromRuntime(runtime, scene, currentDungeonStartMapState{
		RidableGroups:         runtime.randomizedObjects.roomGroups(scene.Coordinate),
		Seed:                  runtime.Seed,
		HellPartyMode:         runtime.StartMapHellPartyMode,
		HellPartyFogFlag:      runtime.StartMapHellFogFlag,
		RoomStateValue:        1,
		RoomStateFlag:         currentDungeonStartMapPayloadBuild,
		PartyMemberIndex:      partyMemberIndex,
		ExtraEntries:          passiveObjectDropEntries,
		ExtraEntryVendorWidth: passiveObjectDropVendorWidth,
	})
	if err != nil {
		return currentDungeonEntryPackets{}, fmt.Errorf("plan current start-map body: %w", err)
	}
	startMapBody, err := startPacket.Build()
	if err != nil {
		return currentDungeonEntryPackets{}, fmt.Errorf("build current start-map body: %w", err)
	}
	return currentDungeonEntryPackets{DungeonInfo: dungeonInfoBody, StartMap: startMapBody}, nil
}

func validateCurrentDungeonEntryRequest(request dungeoncmd.SelectDungeonRequest) error {
	return validateCurrentDungeonEntryRequestForModes(
		request, currentDungeonEntryStrictValidation, currentDungeonEntrySelectionModeStrict)
}

// validateCurrentDungeonEntryRequestForModes 是唯一实现：两条轴分别判定，
// 任一不成立就归进 `errDungeonEntryModeUnsupported`（保持改动前的错误身份不变）。
func validateCurrentDungeonEntryRequestForModes(
	request dungeoncmd.SelectDungeonRequest,
	mode currentDungeonEntryValidationMode,
	selectionMode currentDungeonEntrySelectionModeMode,
) error {
	// The current fixed 21-byte writer has now been observed sending a nonzero
	// u32 at offset 16 in an otherwise ordinary solo request. That value is not
	// consumed by either typed op28 or op29 builder, and its current semantics
	// are not proved. Preserve it in the runtime request, but do not reject the
	// normal entry mode or infer leader/quest authority from it.
	runtimeStateSupported := request.RuntimeState == 0
	// Live production traffic proves the current EXE writes runtime state 1
	// only for the training-room request: dungeon 5000, token UINT16_MAX, and
	// every other owned mode field zero. The field is not consumed by the
	// typed op28/op29 builders, so accept only that exact evidence-backed
	// compatibility unit instead of opening state 1 for ordinary dungeons.
	if request.DungeonID == 5000 && request.RuntimeState == 1 && request.RuntimeToken == 0xffff {
		runtimeStateSupported = true
	}
	// `entry_option`（= op16 body[5:7]，厂商 `HellPartyRequestFlag => Flag1` +
	// `HellPartyDifficultyFlag => Flag2`）在 `DNFBRIDGE_HELL_BLOCKED_SLOTS=on` 档
	// 由深渊准入那一层逐槽位判定，不再当作"不支持的模式"。
	entryOptionSupported := request.EntryOption == 0 ||
		mode == currentDungeonEntryAllowHellOption
	// `selection_mode`（= op16 body[7]）在
	// `DNFBRIDGE_DUNGEON_ENTRY_SELECTION_MODE=allow` 档不再算"不支持的模式"；
	// 依据见本文件上方 `readDungeonEntrySelectionMode` 的现场证据块
	// （area 39 的 310/311/312/313/314 全部用它发请求）。
	//
	// 另外：深渊请求标志读取位置修好之后（`dungeonHellRequestOption`，默认档读 body[7]），
	// body[7] 就是厂商 `Flag1 = HellPartyRequestFlag`，它由 `dungeonRequestedHell` 那条轴
	// 统一治理（准入、房间图、op29 两个字节全走它），所以**不再**在这里当成独立的
	// "不支持的模式"。回退档（读 body[5:7]）保持逐字节旧行为。
	selectionModeSupported := request.SelectionMode == 0 ||
		selectionMode.allowsNonZeroSelectionMode() ||
		dungeonHellRequestByteUsesFlag1()
	if !entryOptionSupported || !selectionModeSupported || !runtimeStateSupported ||
		(request.RuntimeToken != 0 && request.RuntimeToken != 0xffff) || request.Reserved != 0 || request.PartyState != 0 ||
		request.SpecialMode != 0 || len(request.OpaqueTail) != 0 {
		return fmt.Errorf("%w: entry_option=%d selection_mode=%d runtime_state=%d runtime_token=%d reserved=%d party_state=%d offset16_unproven=%d special_mode=%d opaque_tail=%d",
			errDungeonEntryModeUnsupported,
			request.EntryOption,
			request.SelectionMode,
			request.RuntimeState,
			request.RuntimeToken,
			request.Reserved,
			request.PartyState,
			request.LeaderObjectKey,
			request.SpecialMode,
			len(request.OpaqueTail),
		)
	}
	return nil
}
