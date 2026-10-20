package dnfbridge

import (
	"os"
	"strings"
)

// 本文件是"深渊房间两根柱子"（操作者清单 batch-4 item 3）的开关。
//
// ── 缺陷与依据（`_dist/hell-room-two-pillars-diagnosis.md` §1）─────────────────
//
// map 60083（`map/metrocenter/time_plaza/hell_time_plaza.map`）的
// `[special passive object]` 段**只声明了一行** 30528（柱子
// `Monster/CosmoFiend/SealedGateOfHellDungeon4.obj`），我们解析出来也**只有 1 个**
// 对象（`parse_map.go:415-424`）。但我们的 op29 里除了该对象行内 `[monster]` 模板行，
// 还额外发了一条 **type-9「父对象」actor 行**（`planRuntimeDungeonExtendedActors`）。
//
// 厂商口径（`GameWorld\DungeonActorTemplateProjector.cs:254-255` 原文）：
//
//	// The client creates parent objects from the MAP. START_MAP only
//	// supplies the inline templates those objects can activate.
//	for (var objectIndex = 0; objectIndex < mapFile.SpecialPassiveObjects.Count; objectIndex++) {
//	    var obj = mapFile.SpecialPassiveObjects[objectIndex];
//	    if (obj?.Spawns == null || obj.Spawns.Count == 0) continue;      // ← 只对"有 spawn 的对象"发行
//	    for (var spawnIndex = 0; spawnIndex < obj.Spawns.Count; spawnIndex++) {
//	        var spawn = obj.Spawns[spawnIndex];
//	        if (spawn.Code <= 0 || !string.Equals(spawn.Kind, "[monster]", …)) continue;  // ← 只发 [monster] 模板
//	        actors.Add(new Dungeon.MonsterSumInfo { Code = spawn.Code, Type = 0, Level = …,
//	            TemplateOrder = (ushort)objectIndex, PacketIndex = spawnIndex,
//	            Flag0 = 1, Flag1 = (byte)objectIndex, SourceSpecialPassiveObjectIndex = objectIndex });
//	    }
//	}
//
// 即厂商**从不**为父对象自身发行 START_MAP 行；旁证 `Game\Dungeon\DungeonInstance.cs:186`
// `SpecialPassiveObjectActorType = 9`（type 9 是**死亡事实**里的 actor 类型）与
// 同文件 `:392` 注释 `// MAP-owned actors have no START_MAP wire sequence.`（柱子作为
// map-owned actor 被服务端按 ObjectCode 记账，没有 START_MAP 线上序号）。
//
// 现场取证：柱子在整库只出现 2 次（各 1 份），`actor_count=7` = 6 只小怪 + 1 根柱子；
// 客户端自己会从 map 造 1 根 ⇒ 我们那条多余的 type-9 行就是**第二根柱子**。
//
// ── 开关 ──────────────────────────────────────────────────────────────────
//
// DNFBRIDGE_SPECIAL_OBJECT_PARENT_ACTOR
//
//	map_owned（默认，= 修复档）：special passive object **不再**产生 type-9
//	                           「父对象」actor 行（父对象归 map / 客户端自建）。
//	                           行内 `[monster]` 模板行（Type=0, Flag0=1, Flag1=objectIndex）
//	                           一个字段都不动，与厂商 `DungeonActorTemplateProjector.cs:279-298` 一致。
//	start_map（一键回退）      ：逐字节回到改动前（每个 >0 的 special passive object
//	                           仍多发一条 `PacketIndex=objectIndex, Level=0, Type=9` 的行）。
//
// 默认 = 修复档（本仓既定纪律：默认即修复行为，回退档必须显式写值），判据取
// `== start_map` 而不是 `== 修复值`，与 `readDungeonHellRoomMode` 同一约定。
//
// 只影响**线上行表**：`plan.Actors` 里那条 actor 被整条跳过，`SpecialObjectCount` /
// `RetainedSpecialSpawns` / `NextObjectKey` 之外的记账保持不变。副作用（已核）：
// 该对象之后的扩展 actor objectKey / NextObjectKey 整体前移 1（柱子行不再占用 key），
// `start_map` 档逐字节等于今天。
const specialObjectParentActorEnv = "DNFBRIDGE_SPECIAL_OBJECT_PARENT_ACTOR"

// specialObjectParentActorStartMapValue 是唯一被承认的回退取值。
const specialObjectParentActorStartMapValue = "start_map"

type specialObjectParentActorMode int

const (
	// specialObjectParentActorMapOwned 是默认档，也是修复档（零值 = 修复）。
	specialObjectParentActorMapOwned specialObjectParentActorMode = iota
	// specialObjectParentActorStartMap 是改动前的"我们自己发父对象行"档。
	specialObjectParentActorStartMap
)

// readSpecialObjectParentActorMode 解析 DNFBRIDGE_SPECIAL_OBJECT_PARENT_ACTOR。
// **只有显式 `start_map` 才回退**；未设置/空/其它任何值都取默认档（修复）。
func readSpecialObjectParentActorMode(value string) specialObjectParentActorMode {
	if strings.EqualFold(strings.TrimSpace(value), specialObjectParentActorStartMapValue) {
		return specialObjectParentActorStartMap
	}
	return specialObjectParentActorMapOwned
}

// specialObjectParentActorModeFromEnv 在**运行时**读开关（每次规划 actor 表时读一次）。
func specialObjectParentActorModeFromEnv() specialObjectParentActorMode {
	return readSpecialObjectParentActorMode(os.Getenv(specialObjectParentActorEnv))
}

func (mode specialObjectParentActorMode) String() string {
	if mode == specialObjectParentActorStartMap {
		return specialObjectParentActorStartMapValue
	}
	return "map_owned"
}

// emitsParentActorRow 报告该档位是否仍然把父对象本身发成一条 type-9 actor 行。
func (mode specialObjectParentActorMode) emitsParentActorRow() bool {
	return mode == specialObjectParentActorStartMap
}
