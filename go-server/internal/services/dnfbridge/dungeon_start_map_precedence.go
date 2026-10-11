package dnfbridge

import (
	"strings"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件把「起始/首领房用哪一份真源解析地图」这条口径归一化成 env 开关，
// 交给 `worldmap.NewResolverWithOptions`。
//
// 现场缺陷（操作者「7 格蓝迪 克雷没有修复」）：格兰迪发电站 **104** 与
// 克雷发电站 **101** 清完起手房不开下一间的门。根因不在清房判定
// （`packet_log.txt` 里六只怪全部 `game-dungeon-monster-death-accepted` +
// `room_cleared=true`），而在**起始房发错了地图**：
//
//   - 真源 PVF 显式声明（`H:\86JP启动器\90CN\地下城与勇士\Script.pvf`，
//     sha256 `DA11CB5C481B921DA4A4FC3BFC73EB6AA51BBC0F83AEE624F5BFFEE6ECA7112E`）：
//     `dungeon/Act9/grandine.dgn` maze 0 `[map specification] `map` 0 0 42001 …`
//     → `map/map.lst` `42001 \`grandine/42001.map\“；该图有
//     `[pathgate recognize range] 0 0 130 140 0 0 0 0`（第 2 扇门可用）。
//     `dungeon/Act9/KohlePowerStation.dgn` maze 0 `… `map` 0 0 42101 …`
//     → `KohlePowerStation/42101.map`，同样有 `0 0 130 140 0 0 0 0`。
//   - 改动前实际下发（线上 `dnf90.out` 的 `game-dungeon-runtime-prepared`）：
//     `dungeon_id=104 room=0:0 map_id=15424 map_path=map/grandine/15424quest(0,0)start.map`、
//     `dungeon_id=101 room=0:0 map_id=18000 map_path=map/KohlePowerStation/18000(0,0)S.map`。
//     这两张图**整段没有** `[pathgate recognize range]` ⇒ 客户端在这个房间里
//     拿不到任何可用门的触发矩形 ⇒ 门永远不开。
//
// 判据与真源见 `internal/modules/dnf/worldmap/resolver.go` 的
// `StartMapPrecedence` / `ownershipRoomCandidates`。
//
// 开关（与 `readHellBlockedSlotsMode` 同一约定：只认一个字面量，默认 = 修复档）：
//
//	`DNFBRIDGE_DUNGEON_START_MAP_PRECEDENCE`
//	  - 未设置/空/其它任何值（默认）= `specification`：PVF `[map specification]` /
//	    `[boss map]` 显式声明的 map 优先，文件名启发式只在显式声明读不到时兜底；
//	  - `ownership` = 改动前口径（启发式排在显式声明之前），一键回退
//	    **不需要重新编译**：写 `ownership` 重启即逐字节回到今天。
//
// 为什么开关挂在 dnfbridge 层：`internal/services/dnfbridge` 是本仓既定的
// "当前客户端兼容层 + env 开关归一化"owner（同族开关 `DNFBRIDGE_HELL_BLOCKED_SLOTS`、
// `DNFBRIDGE_DUNGEON_ENTRY_SELECTION_MODE` 都在这一层读）；`worldmap` 模块只暴露
// 显式的 `ResolverOptions`，模块层自身不读环境变量。
const dungeonStartMapPrecedenceEnv = "DNFBRIDGE_DUNGEON_START_MAP_PRECEDENCE"

// dungeonStartMapPrecedenceValueOwnership 是唯一被承认的回退取值。
const dungeonStartMapPrecedenceValueOwnership = "ownership"

// readDungeonStartMapPrecedence 解析 `DNFBRIDGE_DUNGEON_START_MAP_PRECEDENCE`。
// **只有显式 `ownership` 才回退**；未设置/空/其它任何值（含 `off`/`0`/`legacy`）
// 都取默认档 `specification`，保证"默认 = 修复后的行为"这条承诺不会被一个
// 手滑的别名破坏（与 `readHellBlockedSlotsMode` / `readDungeonEntrySelectionMode`
// 同一约定）。
func readDungeonStartMapPrecedence(value string) worldmap.StartMapPrecedence {
	if strings.EqualFold(strings.TrimSpace(value), dungeonStartMapPrecedenceValueOwnership) {
		return worldmap.StartMapPrecedenceOwnership
	}
	return worldmap.StartMapPrecedenceSpecification
}

// currentDungeonStartMapPrecedence 返回本 Service 这一档的口径（零值 options
// 的测试/探针路径 = specification = 默认档）。
func (s *Service) currentDungeonStartMapPrecedence() worldmap.StartMapPrecedence {
	if s == nil {
		return worldmap.StartMapPrecedenceSpecification
	}
	if s.options.dungeonStartMapPrecedence == worldmap.StartMapPrecedenceOwnership {
		return worldmap.StartMapPrecedenceOwnership
	}
	return worldmap.StartMapPrecedenceSpecification
}

// dungeonResolverOptions 是 `preloadDungeonWorldMap` 构造 resolver 时用的显式配置。
func (s *Service) dungeonResolverOptions() worldmap.ResolverOptions {
	return worldmap.ResolverOptions{StartMapPrecedence: s.currentDungeonStartMapPrecedence()}
}
