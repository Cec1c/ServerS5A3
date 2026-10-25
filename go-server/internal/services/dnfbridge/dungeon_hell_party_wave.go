package dnfbridge

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件是"深渊房间不出深渊怪物"（操作者清单 batch-4 item 3）的修复：
// `etc/hellparty.etc` 波次表 → 房图 `[hellparty]` 组号 → 深渊房间 START_MAP 的
// **隐藏模板行** + START_MAP 之后的 NOTI 679 + 接受客户端 op235「深渊派对开始」。
//
// ── 缺陷（`_dist/hell-room-two-pillars-diagnosis.md` §2）─────────────────────
//
// 房图 60083 的柱子（special passive object 30528）行内 `[hellparty]` 只是一张
// **索引表**（组号 + 权重 + 波次号），怪物本体在 `etc/hellparty.etc`，而本仓
// **从来没读过这个文件**（全包 `grep wave` = 0 命中）。现场因此只剩房图自带的
// 6 只小怪（71008×3 + 71010×3）—— 正是操作者说的"只有第一波小怪"。
//
// ── 厂商口径（唯一依据）────────────────────────────────────────────────────
//
//  1. 装配波次 `GameWorld\Dungeon.cs:1151-1194 BuildHellPartyWaves`：
//     按 `Order` 升序分组 → 每个 order 内只保留 `HellPartyData.HasEntries(GroupID, difficulty)`
//     的候选 → `:1365-1389 PickHellPartyEntry` 按 `Rate` 加权抽 1 个 →
//     `:1196-1239 BuildHellPartyMonsterInfos` 展开成怪物（`EntityType == 1 ⇒ type 5` + APC 等级，
//     否则 `type 0` + `GetDungeonBasicLv(dungeonId)`）。
//  2. 隐藏模板行 `Network\Handlers\Dungeon\DungeonMapHandler.cs:847-879
//     AppendHellPartyTemplateRows`：把每只波次怪作为**普通 actor 行**追加进同一张表，
//     `TemplateOrder = order`、`PacketIndex = null`（由 running index 决定）、
//     `Flag0 = HellPartyHiddenTemplateFlag (= 1, :16)`、`Flag1 = HellPartyAttachAllWavesSelector
//     (= 0xFF, :17)`、`ExtraState = 0`。行的**线格式就是既有的 21 字节 op29 actor 行**
//     （`DungeonNotificationBuilder.cs:168-186 BuildStartMap` 与
//     `dungeon_start_map.go:179-190 Build()` 逐字段同序），所以本修复**没有发明任何字节**。
//  3. NOTI 679 `DungeonMapHandler.cs:679-688`（opcode `PacketTypesA21.cs:686
//     HELL_PARTY_MONSTER_INFO = 0x02A7`，体 `DungeonNotificationBuilder.cs:120-135
//     BuildHellPartyMonsterInfo` = `int32 count` + count × `(int32 code, int32 level)`，
//     去重按出现顺序、等级取 `Math.Max(1, level)`，entries 由 `:755-778
//     BuildHellPartyMonsterInfoEntries` 生成）在 START_MAP **之后**发。
//  4. 客户端发来的「深渊派对开始」与分解机请求**共用 opcode**
//     `PacketTypesA21.cs:1466 REQUEST_DISJOINT_ITEM = 0x00EB /* 235 */`，
//     由 `Network\Protocol\GameProtocolHandler.cs:970-978 HandleSharedDisjointOrHellParty`
//     按"是否在分解机里"分派；`DungeonMapHandler.cs:944-963 HandleHellPartyStart`
//     只记状态（`HellPartyPhase: WaitingStart → Started`）**不回包**。
//
// ── 开关（三个，默认都是修复档；回退值一律字面量 `legacy`）──────────────────
//
// DNFBRIDGE_HELL_PARTY_WAVE_ROWS
//
//	hidden（默认，= 修复）：深渊房间的 START_MAP 追加波次隐藏模板行。
//	legacy（一键回退）     ：一个波次行都不发（逐字节回到改动前）。
//
// DNFBRIDGE_HELL_PARTY_MONSTER_INFO
//
//	noti（默认，= 修复） ：START_MAP 之后补一条 NOTI 679。
//	legacy（一键回退）   ：不发 679（逐字节回到改动前）。
//
// DNFBRIDGE_HELL_PARTY_START_CMD
//
//	accept（默认，= 修复）：op235 在深渊房间里按「深渊派对开始」收下（不回包）。
//	legacy（一键回退）    ：完全不接管 op235（逐字节回到改动前：照旧走分解机失败回包）。
//
// 三者合起来的取值网格（也是测试要钉的两侧）：
//
//	WAVE_ROWS=legacy & MONSTER_INFO=legacy ⇒ 逐字节今天（连随机都不抽）
//	WAVE_ROWS=legacy & MONSTER_INFO=noti   ⇒ 只发 679、不发隐藏行（诊断档 B-2）
//	WAVE_ROWS=hidden & MONSTER_INFO=legacy ⇒ 只发隐藏行
//	WAVE_ROWS=hidden & MONSTER_INFO=noti   ⇒ 修复档（默认）
//
// 波次解析（含随机抽组）只在两个开关里至少有一个是修复档时发生，因此**两个都是 legacy
// 时不消耗任何随机数**，回退档逐字节、逐随机序列等于今天。
//
// ── 本轮边界（诚实口径）────────────────────────────────────────────────────
//
// 波次怪以**扩展 actor**（`runtimeDungeonActorSpecialMonster`）投影，**不**进
// `scene.Monsters` / `BlockingHostiles`：清房/开门语义与改动前完全一致，死亡上报走既有的
// `Room.RetireSpecialMonsterActor`（`dungeon_monster_runtime.go:638-683`，与
// `[special passive object] [monster]` 子模板同一条已证路径：收下 op39、回 op38、
// 不产生经验/掉落/清房影响）。厂商口径里这些行 `IsBlocking = true` 且计入房间怪数
// （`DungeonMapHandler.cs:1229` / `:907-914 CountServerTrackedMonsters`），把它们升格成
// 真正的房间敌对怪属于**下一轮**，需要一次实机确认 + 经验/掉落口径决策。
const (
	hellPartyWaveRowsEnv     = "DNFBRIDGE_HELL_PARTY_WAVE_ROWS"
	hellPartyMonsterInfoEnv  = "DNFBRIDGE_HELL_PARTY_MONSTER_INFO"
	hellPartyStartCommandEnv = "DNFBRIDGE_HELL_PARTY_START_CMD"
)

// 三个开关的唯一回退取值。
const (
	hellPartyWaveRowsLegacyValue     = "legacy"
	hellPartyMonsterInfoLegacyValue  = "legacy"
	hellPartyStartCommandLegacyValue = "legacy"
)

// 厂商常量（file:line 见文件头）。
const (
	// hellPartyHiddenTemplateFlag = `DungeonMapHandler.cs:16 HellPartyHiddenTemplateFlag`。
	hellPartyHiddenTemplateFlag byte = 1
	// hellPartyAttachAllWavesSelector = `DungeonMapHandler.cs:17 HellPartyAttachAllWavesSelector`。
	hellPartyAttachAllWavesSelector byte = 0xFF
	// a21NotiHellPartyMonsterInfo = `PacketTypesA21.cs:686 HELL_PARTY_MONSTER_INFO = 0x02A7`。
	a21NotiHellPartyMonsterInfo uint16 = 679
	// a21CmdHellPartyStart = `PacketTypesA21.cs:1466 REQUEST_DISJOINT_ITEM = 0x00EB`（= 235）。
	a21CmdHellPartyStart uint16 = uint16(dnfenum.CmdPacketRequestDisjointItem)
)

var (
	errDungeonHellPartyWaveLevel     = errors.New("dnf dungeon hell party wave monster level is invalid")
	errDungeonHellPartyWaveCode      = errors.New("dnf dungeon hell party wave monster code is invalid")
	errDungeonHellPartyMapUnresolved = errors.New("dnf dungeon hell party room map is unresolved")
)

type hellPartyWaveRowsMode int

const (
	// hellPartyWaveRowsHidden 是默认档，也是修复档（零值 = 修复）。
	hellPartyWaveRowsHidden hellPartyWaveRowsMode = iota
	// hellPartyWaveRowsLegacy 是改动前的"不发行"档。
	hellPartyWaveRowsLegacy
)

type hellPartyMonsterInfoMode int

const (
	// hellPartyMonsterInfoNoti 是默认档，也是修复档。
	hellPartyMonsterInfoNoti hellPartyMonsterInfoMode = iota
	// hellPartyMonsterInfoLegacy 是改动前的"不发 679"档。
	hellPartyMonsterInfoLegacy
)

type hellPartyStartCommandMode int

const (
	// hellPartyStartCommandAccept 是默认档，也是修复档。
	hellPartyStartCommandAccept hellPartyStartCommandMode = iota
	// hellPartyStartCommandLegacy 是改动前的"不接管 op235"档。
	hellPartyStartCommandLegacy
)

func readDungeonHellPartyWaveRowsMode(value string) hellPartyWaveRowsMode {
	if strings.EqualFold(strings.TrimSpace(value), hellPartyWaveRowsLegacyValue) {
		return hellPartyWaveRowsLegacy
	}
	return hellPartyWaveRowsHidden
}

func readDungeonHellPartyMonsterInfoMode(value string) hellPartyMonsterInfoMode {
	if strings.EqualFold(strings.TrimSpace(value), hellPartyMonsterInfoLegacyValue) {
		return hellPartyMonsterInfoLegacy
	}
	return hellPartyMonsterInfoNoti
}

func readDungeonHellPartyStartCommandMode(value string) hellPartyStartCommandMode {
	if strings.EqualFold(strings.TrimSpace(value), hellPartyStartCommandLegacyValue) {
		return hellPartyStartCommandLegacy
	}
	return hellPartyStartCommandAccept
}

func (mode hellPartyWaveRowsMode) String() string {
	if mode == hellPartyWaveRowsLegacy {
		return hellPartyWaveRowsLegacyValue
	}
	return "hidden"
}

func (mode hellPartyMonsterInfoMode) String() string {
	if mode == hellPartyMonsterInfoLegacy {
		return hellPartyMonsterInfoLegacyValue
	}
	return "noti"
}

func (mode hellPartyStartCommandMode) String() string {
	if mode == hellPartyStartCommandLegacy {
		return hellPartyStartCommandLegacyValue
	}
	return "accept"
}

// emitsHiddenRows 报告该档位是否把波次怪发成 START_MAP 隐藏模板行。
func (mode hellPartyWaveRowsMode) emitsHiddenRows() bool { return mode == hellPartyWaveRowsHidden }

// emitsMonsterInfo 报告该档位是否在 START_MAP 之后补 NOTI 679。
func (mode hellPartyMonsterInfoMode) emitsMonsterInfo() bool { return mode == hellPartyMonsterInfoNoti }

// accepts 报告该档位是否接管客户端 op235「深渊派对开始」。
func (mode hellPartyStartCommandMode) accepts() bool { return mode == hellPartyStartCommandAccept }

// 运行时读取（每次用到时读一次 env；纯函数在三个 read* 里）。
func hellPartyWaveRowsModeFromEnv() hellPartyWaveRowsMode {
	return readDungeonHellPartyWaveRowsMode(os.Getenv(hellPartyWaveRowsEnv))
}

func hellPartyMonsterInfoModeFromEnv() hellPartyMonsterInfoMode {
	return readDungeonHellPartyMonsterInfoMode(os.Getenv(hellPartyMonsterInfoEnv))
}

func hellPartyStartCommandModeFromEnv() hellPartyStartCommandMode {
	return readDungeonHellPartyStartCommandMode(os.Getenv(hellPartyStartCommandEnv))
}

// dungeonHellPartyMonster 是一只已冻结等级/类型的波次怪。
type dungeonHellPartyMonster struct {
	Code  int64
	Type  byte
	Level byte
}

// dungeonHellPartyWave 是一个 order 上抽中的组及展开后的怪物（厂商 `HellPartyWaveInfo`）。
type dungeonHellPartyWave struct {
	Order    int64
	GroupID  int64
	Monsters []dungeonHellPartyMonster
}

// dungeonHellPartyWavesNeeded 报告当前开关组合是否需要解析波次（= 至少要有人消费它）。
func dungeonHellPartyWavesNeeded() bool {
	return hellPartyWaveRowsModeFromEnv().emitsHiddenRows() || hellPartyMonsterInfoModeFromEnv().emitsMonsterInfo()
}

// resolveDungeonHellPartyWaves 对应厂商 `Dungeon.cs:1151-1194 BuildHellPartyWaves` + `:1196-1239`。
//
// 表不可用（`etc/hellparty.etc` 读不到/解析失败）时返回空表：这是**厂商自己的行为**
// （`HellPartyData.cs:141-149 ReadHellPartyScript` 读不到就返回空内容 ⇒ 没有波次），
// 预载会另发一条具名事件把这条降级写进日志。
func (s *Service) resolveDungeonHellPartyWaves(
	table *worldmap.Table,
	catalog *pvfDungeonHellPartyCatalog,
	aiCatalog *pvfDungeonAICharacterCatalog,
	hellRoomMapID int64,
	basis worldmap.OptionalInt,
	difficulty byte,
) ([]dungeonHellPartyWave, error) {
	if table == nil {
		return nil, errDungeonWorldMapUnavailable
	}
	mapValue, found := table.FindMap(hellRoomMapID)
	if !found {
		return nil, fmt.Errorf("%w: map=%d", errDungeonHellPartyMapUnresolved, hellRoomMapID)
	}
	entriesByOrder := make(map[int64][]worldmap.HellPartyEntry)
	orders := make([]int64, 0, 2)
	for _, object := range mapValue.SpecialPassiveObjects {
		for _, entry := range object.HellParty {
			if _, exists := entriesByOrder[entry.Order]; !exists {
				orders = append(orders, entry.Order)
			}
			entriesByOrder[entry.Order] = append(entriesByOrder[entry.Order], entry)
		}
	}
	if len(orders) == 0 {
		return nil, nil
	}
	if catalog == nil {
		return nil, nil
	}
	// 厂商用 `SortedDictionary<int, …>` ⇒ order 升序。
	sort.Slice(orders, func(i, j int) bool { return orders[i] < orders[j] })
	result := make([]dungeonHellPartyWave, 0, len(orders))
	for _, order := range orders {
		candidates := make([]worldmap.HellPartyEntry, 0, len(entriesByOrder[order]))
		for _, entry := range entriesByOrder[order] {
			if catalog.HasEntries(entry.GroupID, difficulty) {
				candidates = append(candidates, entry)
			}
		}
		selected, ok, err := s.pickDungeonHellPartyEntry(candidates)
		if err != nil {
			return nil, fmt.Errorf("pick hell party entry order=%d: %w", order, err)
		}
		if !ok {
			continue
		}
		monsters, err := s.buildDungeonHellPartyMonsterInfos(catalog, aiCatalog, selected.GroupID, basis, difficulty)
		if err != nil {
			return nil, fmt.Errorf("build hell party monsters order=%d group=%d: %w", order, selected.GroupID, err)
		}
		if len(monsters) == 0 {
			continue
		}
		result = append(result, dungeonHellPartyWave{Order: order, GroupID: selected.GroupID, Monsters: monsters})
	}
	return result, nil
}

// pickDungeonHellPartyEntry 逐行对应厂商 `Dungeon.cs:1365-1389 PickHellPartyEntry`
// （按 `Rate` 加权抽 1 个；总权重 <= 0 时取第一个；空表返回 false = 厂商的 null）。
func (s *Service) pickDungeonHellPartyEntry(candidates []worldmap.HellPartyEntry) (worldmap.HellPartyEntry, bool, error) {
	if len(candidates) == 0 {
		return worldmap.HellPartyEntry{}, false, nil
	}
	total := 0
	for _, entry := range candidates {
		if entry.Rate > 0 {
			total += int(entry.Rate)
		}
	}
	if total <= 0 {
		return candidates[0], true, nil
	}
	roll, err := s.chooseDungeonIndex(total)
	if err != nil {
		return worldmap.HellPartyEntry{}, false, err
	}
	for _, entry := range candidates {
		if entry.Rate <= 0 {
			continue
		}
		if roll < int(entry.Rate) {
			return entry, true, nil
		}
		roll -= int(entry.Rate)
	}
	return candidates[0], true, nil
}

// buildDungeonHellPartyMonsterInfos 逐行对应厂商 `Dungeon.cs:1196-1239 BuildHellPartyMonsterInfos`。
func (s *Service) buildDungeonHellPartyMonsterInfos(
	catalog *pvfDungeonHellPartyCatalog,
	aiCatalog *pvfDungeonAICharacterCatalog,
	groupID int64,
	basis worldmap.OptionalInt,
	difficulty byte,
) ([]dungeonHellPartyMonster, error) {
	entries := catalog.Entries(groupID, difficulty)
	if len(entries) == 0 {
		return nil, nil
	}
	result := make([]dungeonHellPartyMonster, 0, len(entries))
	for _, entry := range entries {
		if entry.Code <= 0 || entry.Code > int64(^uint32(0)) {
			return nil, fmt.Errorf("%w: group=%d code=%d", errDungeonHellPartyWaveCode, groupID, entry.Code)
		}
		monsterType := byte(0)
		var level byte
		var err error
		if entry.EntityType == 1 {
			// 厂商：APC 先读 `AICharacter.lst` 等级，读不到**回落到副本基准等级**
			// （`Dungeon.cs:1210-1214`，并把回落写进 `[Dungeon] HellParty APC … fallback` 日志）。
			monsterType = 5
			if aiCatalog != nil {
				definition, found, findErr := aiCatalog.Find(entry.Code)
				if findErr != nil {
					return nil, fmt.Errorf("resolve hell party APC code=%d: %w", entry.Code, findErr)
				}
				if found && definition.Level > 0 {
					level = definition.Level
				}
			}
		}
		if level == 0 {
			level, err = dungeonHellPartyBasisLevel(basis, entry.Code)
			if err != nil {
				return nil, err
			}
		}
		result = append(result, dungeonHellPartyMonster{Code: entry.Code, Type: monsterType, Level: level})
	}
	return result, nil
}

// dungeonHellPartyBasisLevel 是厂商 `GetDungeonBasicLv(dungeonId)` 在本仓的投影
// （`worldmap.Dungeon.Metadata.BasisLevel` = dgn `[basis level]`）。算不出来就**硬失败**，
// 绝不编一个等级。
func dungeonHellPartyBasisLevel(basis worldmap.OptionalInt, code int64) (byte, error) {
	if !basis.Set || basis.Value <= 0 || basis.Value > 0xff {
		return 0, fmt.Errorf("%w: code=%d basis=%d basis_set=%t", errDungeonHellPartyWaveLevel, code, basis.Value, basis.Set)
	}
	return byte(basis.Value), nil
}

// appendCurrentHellPartyTemplateRows 把波次怪作为**隐藏模板行**追加进房间的扩展 actor 计划，
// 逐行对应厂商 `DungeonMapHandler.cs:847-879 AppendHellPartyTemplateRows`。
//
// `PacketIndex` 用厂商的 running index 口径（`DungeonNotificationBuilder.cs:166-174`：
// `monster.PacketIndex ?? (isApc ? apcIndex++ : normalIndex++)`）—— 这里把它投影成
// "先数已经占用了普通/APC 序号的既有行（房图怪 + 已规划的扩展 actor），隐藏行接着往后排"。
//
// 波次怪本轮**不进** `scene.Monsters`：它们没有 HostileReference，只作为扩展 actor
// 被公告（`AnnounceAllActors` 会把它们推进到 announced），死亡上报走
// `RetireSpecialMonsterActor`。见文件头"本轮边界"。
func (s *Service) appendCurrentHellPartyTemplateRows(
	waves []dungeonHellPartyWave,
	scene worldmap.DungeonRoomScene,
	monsterCatalog *pvfDungeonMonsterCatalog,
	plan *runtimeDungeonExtendedActorPlan,
) error {
	if plan == nil || !hellPartyWaveRowsModeFromEnv().emitsHiddenRows() || len(waves) == 0 {
		return nil
	}
	normalIndex, apcIndex := currentDungeonHellPartyPacketIndexCounters(scene, plan)
	for _, wave := range waves {
		order := wave.Order
		if order < 0 || order > int64(^uint16(0)) {
			order = 0
		}
		for _, monster := range wave.Monsters {
			// EntityType 1 is projected as Type 5 by the wave builder. Its
			// definition belongs to AICharacter.lst, not monster.lst. Keep
			// missing definitions fail-closed before committing the room move.
			if monster.Type == 5 {
				aiCatalog, err := s.dungeonAICharacterCatalog()
				if err != nil {
					return err
				}
				if _, found, err := aiCatalog.Find(monster.Code); err != nil {
					return fmt.Errorf("resolve hell party wave APC code=%d: %w", monster.Code, err)
				} else if !found {
					return fmt.Errorf("%w: hell party wave APC code=%d", errDungeonAICharacterDefinitionMiss, monster.Code)
				}
			} else {
				if monsterCatalog == nil {
					return errDungeonMonsterCatalogUnavailable
				}
				if _, found, err := monsterCatalog.Find(monster.Code); err != nil {
					return fmt.Errorf("resolve hell party wave monster code=%d: %w", monster.Code, err)
				} else if !found {
					return fmt.Errorf("%w: hell party wave monster code=%d", errDungeonMonsterDefinitionMiss, monster.Code)
				}
			}
			packetIndex := uint32(normalIndex)
			if monster.Type >= 5 {
				packetIndex = uint32(apcIndex)
				apcIndex++
			} else {
				normalIndex++
			}
			actor, err := newRuntimeDungeonExtendedActor(
				runtimeDungeonActorSpecialMonster,
				plan.NextObjectKey,
				monster.Code,
				currentDungeonStartMapActor{
					TemplateOrder: uint16(order),
					PacketIndex:   packetIndex,
					Level:         monster.Level,
					Type:          monster.Type,
					Flag0:         hellPartyHiddenTemplateFlag,
					Flag1:         hellPartyAttachAllWavesSelector,
				},
			)
			if err != nil {
				return err
			}
			actor.HellPartyOrder = int(order)
			actor.HellPartyGroupID = wave.GroupID
			plan.Actors = append(plan.Actors, actor)
			plan.NextObjectKey++
		}
	}
	if len(scene.Monsters)+len(plan.Actors) > 0xff {
		return fmt.Errorf("%w: normal=%d extended=%d", errDungeonStartMapActorCount, len(scene.Monsters), len(plan.Actors))
	}
	return nil
}

// currentDungeonHellPartyPacketIndexCounters 数出"已经用掉的普通序号/APC 序号"。
func currentDungeonHellPartyPacketIndexCounters(
	scene worldmap.DungeonRoomScene,
	plan *runtimeDungeonExtendedActorPlan,
) (int, int) {
	normalIndex, apcIndex := 0, 0
	for _, monster := range scene.Monsters {
		actorType, err := currentDungeonMonsterActorType(monster)
		if err != nil {
			// 类型算不出来的行走 op29 会硬失败（`currentDungeonStartMapActors`），
			// 这里只做序号记账，按普通序号计。
			normalIndex++
			continue
		}
		if actorType >= 5 {
			apcIndex++
			continue
		}
		normalIndex++
	}
	for _, actor := range plan.Actors {
		if actor.Packet.Type >= 5 {
			apcIndex++
			continue
		}
		normalIndex++
	}
	return normalIndex, apcIndex
}

// buildCurrentHellPartyMonsterInfoEntries 对应厂商 `DungeonMapHandler.cs:755-778
// BuildHellPartyMonsterInfoEntries`：按出现顺序去重 code，等级取 `Math.Max(1, level)`。
func buildCurrentHellPartyMonsterInfoEntries(waves []dungeonHellPartyWave) []dungeonHellPartyMonster {
	seen := make(map[int64]struct{}, len(waves))
	result := make([]dungeonHellPartyMonster, 0, len(waves))
	for _, wave := range waves {
		for _, monster := range wave.Monsters {
			if monster.Code <= 0 {
				continue
			}
			if _, exists := seen[monster.Code]; exists {
				continue
			}
			seen[monster.Code] = struct{}{}
			level := monster.Level
			if level < 1 {
				level = 1
			}
			result = append(result, dungeonHellPartyMonster{Code: monster.Code, Type: monster.Type, Level: level})
		}
	}
	return result
}

// buildCurrentHellPartyMonsterInfoBody 对应厂商 `DungeonNotificationBuilder.cs:120-135
// BuildHellPartyMonsterInfo`：`int32 count` + count × `(int32 code, int32 level)`。
func buildCurrentHellPartyMonsterInfoBody(waves []dungeonHellPartyWave) []byte {
	entries := buildCurrentHellPartyMonsterInfoEntries(waves)
	if len(entries) == 0 {
		return nil
	}
	var writer packetWriter
	writer.writeInt32(len(entries))
	for _, entry := range entries {
		writer.writeInt32(int(entry.Code))
		writer.writeInt32(int(entry.Level))
	}
	return writer.bytes()
}

// currentHellPartyMonsterInfoWaves 选 NOTI 679 的内容源。
//
// **优先房间上真正发出去的那几行**：队伍级共享实例下同一个房间可能由队友先建
// （`partyDungeonSharedRoom`），那时线上只有**他**抽中的组，本会话冻结的波次可能不同；
// 679 必须与同一条 START_MAP 里给出的行一致，所以从房间 actor 表反推波次。
// 房间里没有波次行时回落到本会话冻结的波次 —— 那正是
// `DNFBRIDGE_HELL_PARTY_WAVE_ROWS=legacy` + `..._MONSTER_INFO=noti` 这条诊断档
// （只发 679、不发隐藏行）。
func currentHellPartyMonsterInfoWaves(
	runtime *runtimeDungeonState,
	room *runtimeDungeonRoom,
) ([]dungeonHellPartyWave, string) {
	if room != nil {
		snapshot := room.Snapshot()
		waves := make([]dungeonHellPartyWave, 0, 2)
		indexByOrder := make(map[int]int, 2)
		for _, actor := range snapshot.ExtendedActors {
			// 判据与厂商 `BuildHellPartyGroupRemaining` 一致：`HellPartyGroupId > 0`。
			if actor.HellPartyGroupID <= 0 {
				continue
			}
			index, ok := indexByOrder[actor.HellPartyOrder]
			if !ok {
				index = len(waves)
				indexByOrder[actor.HellPartyOrder] = index
				waves = append(waves, dungeonHellPartyWave{
					Order:   int64(actor.HellPartyOrder),
					GroupID: actor.HellPartyGroupID,
				})
			}
			waves[index].Monsters = append(waves[index].Monsters, dungeonHellPartyMonster{
				Code:  int64(actor.Packet.Code),
				Type:  actor.Packet.Type,
				Level: actor.Packet.Level,
			})
		}
		if len(waves) != 0 {
			return waves, "room_extended_actor_rows"
		}
	}
	if runtime == nil || len(runtime.HellPartyWaves) == 0 {
		return nil, ""
	}
	return runtime.HellPartyWaves, "session_frozen_waves"
}

// sendCurrentHellPartyMonsterInfo 在深渊房间的 START_MAP **之后**补 NOTI 679。
//
// 位置与厂商一致（`DungeonMapHandler.cs:658-688`：先 START_MAP、再若干投影，最后 679），
// 只对自己就是深渊房间的那一次 START_MAP 发（`scene.Map.Map.ID == runtime.HellRoom.MapID`）。
// `room` 必须是**这一次 START_MAP 对应**的房间对象（移动路径传 `plan.TargetRoom`，
// 进场路径传 `runtime.Room`）：679 的 entries 就是从它上面的波次行反推的。
func (s *Service) sendCurrentHellPartyMonsterInfo(
	session *gameSession,
	runtime *runtimeDungeonState,
	room *runtimeDungeonRoom,
	scene worldmap.DungeonRoomScene,
	source string,
) error {
	if s == nil || session == nil || runtime == nil {
		return nil
	}
	if !hellPartyMonsterInfoModeFromEnv().emitsMonsterInfo() {
		return nil
	}
	if !currentHellPartySceneIsHellRoom(runtime, scene) {
		return nil
	}
	waves, entriesSource := currentHellPartyMonsterInfoWaves(runtime, room)
	if len(waves) == 0 {
		return nil
	}
	body := buildCurrentHellPartyMonsterInfoBody(waves)
	if body == nil {
		return nil
	}
	s.logGameEvent(session, "game-dungeon-hell-party-monster-info-send",
		"source", source,
		"entries_source", entriesSource,
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"msg_id", a21NotiHellPartyMonsterInfo,
		"classification", 0,
		"plain_body_len", len(body),
		"entries", len(body)/8,
		"waves", len(waves),
		"frozen_waves", len(runtime.HellPartyWaves),
		"difficulty", runtime.HellPartyDifficulty,
		"switch", hellPartyMonsterInfoModeFromEnv().String(),
		"response", "vendor_DungeonMapHandler_cs_679_HELL_PARTY_MONSTER_INFO_after_START_MAP")
	return s.sendGameUpperRawClass(session, a21NotiHellPartyMonsterInfo, body, 0)
}
