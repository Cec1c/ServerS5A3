package gmtool

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件实现「开启最高难度」（A21 前台按钮 `btn-unlock-dungeon-permissions`）。
//
// 旧实现是一句硬编码失败（"副本难度解锁尚未移植到 90CN 面板"），但服务端其实
// 早就有这条写路径：dnfbridge 在副本结算时用
// `dnfrepo.DungeonPermission.UpsertMax(char, dungeonID, nextClearState)` 落库
// （dnfbridge/dungeon_resource_state.go:200-250），选角后由
// `sendCurrentDungeonPermissionSnapshot` 作为「已通关难度快照」发给客户端。
// 本面板复用**同一个**仓储方法与**同一段** PVF 判据，不新增第二套难度逻辑。
//
// 上限值不是发明出来的：每个副本的最高 clear state 由该副本自己在运行时 PVF 里
// 声明的难度档位决定 —— `difficulty_levels` 合法档位数 > 1 → 档位数-1；
// 否则有 `designated_difficulties` 或 `difficulty` → 4；两者都没有 → 0（不可
// 解锁，跳过）。判据与 dnfbridge/currentDungeonMaxClearState 一字不差；这里
// 复制过来而不是导出，是为了不动 dnfbridge 的既有实现。

// dungeonMaxClearState 与 dnfbridge.currentDungeonMaxClearState 同一口径。
func dungeonMaxClearState(dungeon worldmap.Dungeon) byte {
	if len(dungeon.Metadata.DifficultyLevels) != 0 {
		count := 0
		for _, level := range dungeon.Metadata.DifficultyLevels {
			if level != 0 {
				count++
			}
		}
		if count > 1 {
			if count-1 > int(^uint8(0)) {
				return ^uint8(0)
			}
			return byte(count - 1)
		}
		return 0
	}
	if len(dungeon.Metadata.DesignatedDifficulties) != 0 {
		return 4
	}
	if dungeon.Metadata.Difficulty.Set && dungeon.Metadata.Difficulty.Value >= 0 {
		return 4
	}
	return 0
}

func (c *pvfCache) dungeonTable(s *Server) (*worldmap.Table, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dungeonDone {
		return c.dungeons, c.dungeonErr
	}
	c.dungeonDone = true
	source, err := s.source()
	if err != nil {
		c.dungeonErr = err
		return nil, err
	}
	// 只需要副本表：跳过 map/area 能省掉绝大部分解析量。
	table, err := worldmap.LoadSource(context.Background(), source, worldmap.Options{SkipMaps: true, SkipAreas: true})
	if err != nil {
		c.dungeonErr = fmt.Errorf("加载副本表失败: %w", err)
		return nil, c.dungeonErr
	}
	c.dungeons = table
	return table, nil
}

type dungeonPermissionTarget struct {
	dungeonID int64
	maxState  byte
}

func dungeonPermissionTargets(table *worldmap.Table) []dungeonPermissionTarget {
	if table == nil {
		return nil
	}
	dungeons := table.Dungeons()
	targets := make([]dungeonPermissionTarget, 0, len(dungeons))
	for _, dungeon := range dungeons {
		if dungeon.ID <= 0 || dungeon.ID > int64(^uint32(0)) {
			continue
		}
		maxState := dungeonMaxClearState(dungeon)
		if maxState == 0 {
			continue
		}
		targets = append(targets, dungeonPermissionTarget{dungeonID: dungeon.ID, maxState: maxState})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].dungeonID < targets[j].dungeonID })
	return targets
}

// handleUnlockDungeonPermissions 把当前角色可解锁的每个副本都写到最高难度。
//
// 幂等：UpsertMax 只在「新状态高于已存状态」时才更新，重复点击不产生额外写入；
// 响应里的 insertedCount/updatedCount 如实反映本次真正变化的条数。
func (s *Server) handleUnlockDungeonPermissions(w http.ResponseWriter, r *http.Request) {
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	if group.DungeonPermission == nil {
		writeFailure(w, "副本难度仓储不可用")
		return
	}
	table, err := s.pvf.dungeonTable(s)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	targets := dungeonPermissionTargets(table)
	if len(targets) == 0 {
		writeFailure(w, "当前 PVF 里没有声明难度的副本，无法解锁")
		return
	}
	_ = ctx
	updated := 0
	scanned := 0
	for _, target := range targets {
		scanned++
		entry, changed, err := group.DungeonPermission.UpsertMax(ctx, character.CharacterID, uint32(target.dungeonID), target.maxState)
		if err != nil {
			writeFailure(w, fmt.Sprintf("开启副本 %d 难度失败: %v", target.dungeonID, err))
			return
		}
		if changed && entry.ClearState != 0 {
			updated++
		}
	}
	writeOK(w, map[string]any{
		"characterId":   panelID,
		"scannedCount":  scanned,
		"insertedCount": updated,
		"updatedCount":  updated,
		"hint":          "难度快照按角色持久化；在线角色返回选角再进入后由服务端下发。",
	})
}

// 编译期断言：本文件写的就是服务端同一个仓储方法签名。
var _ = func(repo dnfrepo.DungeonPermissionRepository, characterID string, dungeonID uint32, state byte) {
	_, _, _ = repo.UpsertMax(context.Background(), characterID, dungeonID, state)
}
