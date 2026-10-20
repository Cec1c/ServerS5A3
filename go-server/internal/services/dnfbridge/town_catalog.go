package dnfbridge

import (
	"context"
	"fmt"
	"strings"

	dnftown "longheng.io/server/internal/modules/dnf/town"
)

func (s *Service) preloadTownCatalog(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return fmt.Errorf("preload town pvf: %w", err)
	}
	table, err := dnftown.Load(ctx, archive, dnftown.Options{})
	if err != nil {
		return fmt.Errorf("preload town catalog: %w", err)
	}
	s.townCatalogMu.Lock()
	s.townCatalog = table
	s.townCatalogMu.Unlock()
	snapshot := table.Snapshot()
	s.logPacketEvent("dnf-town-catalog-loaded", "towns", snapshot.Towns, "areas", snapshot.Areas)
	return nil
}

func (s *Service) townArea(townID, areaID int64) (dnftown.Area, bool) {
	if s == nil {
		return dnftown.Area{}, false
	}
	s.townCatalogMu.RLock()
	table := s.townCatalog
	s.townCatalogMu.RUnlock()
	if table == nil {
		return dnftown.Area{}, false
	}
	return table.FindArea(townID, areaID)
}

func (s *Service) townGateArea(townID int64) (dnftown.Area, bool) {
	if s == nil {
		return dnftown.Area{}, false
	}
	s.townCatalogMu.RLock()
	table := s.townCatalog
	s.townCatalogMu.RUnlock()
	if table == nil {
		return dnftown.Area{}, false
	}
	return table.FindGateArea(townID)
}

// townTeleportPotionLandingArea 是**改动前**的落地区域选择（保留下来做逐字节回退基线）。
//
// 判据来自现场封包：客户端请求里带的点击点坐标与**目标地区的主城区**自洽，
// 与"当前所在区域"或"PVF [gate] 区"都不自洽：
//
//   - town=43 x=1461 y=893：使用者当时在 town 39 area 2 (new_backstreet_1.map，现场
//     坐标带 x∈[55,639] y∈[178,353])，y=893 不可能是当前地图的点；而 town 43 的
//     area 0 是 `Cataclysm/Town/StormPass/StormPass.map`（现场 x∈[55,1229] y∈[417,891]）。
//   - town=38 x=579 y=204 / x=743 y=223：使用者当时在 town 38 area 1
//     (new_seria_room.map，现场 y∈[233,311]，门坐标 450 234)，点击点落在房间可行走带
//     **之上**；而 town 38 的 area 0 是 new_Elvengard.map（现场 x∈[55,2319] y∈[138,360]）。
//   - town=39 x=1990 y=297 / x=2420 y=304：area 0 是 new_Hendon_Main.map
//     （现场 x∈[55,3547] y∈[130,270]），x 落在城镇主街上。
//
// 真档 town 11/12(素喃/时空之门) 与 town 43 只声明 `[normal]`/`[dungeon gate]` 区、
// 没有 `[gate]` 区 —— 上一版用 FindGateArea 在这里硬失败，操作者看到的就是
// 「对方正忙，无法对其使用该功能。」。所以这里逐级回退，只要该城镇有任何一个带
// 地图的区域就一定能落地，绝不因为缺 `[gate]` 而失败。
//
// 2026-09-27 现场复验补正：这条链把 area 0 当成"主城区"是**硬编码假设**，对
// town 30/31 这类"area 0 = 城镇边界门图"的城镇是错的（真档 53 个城镇里 15 个如此）。
// 默认档不再直接用它，只在"主城区解析不出来"时逐字节回退到这里；回退基线的
// 语义与顺序一字未改，见 townTeleportPotionLandingDecision。
func (s *Service) townTeleportPotionLandingArea(townID int64) (dnftown.Area, bool) {
	areas, found := s.townAreas(townID)
	if !found {
		return dnftown.Area{}, false
	}
	for _, area := range areas {
		if area.ID == 0 && strings.TrimSpace(area.MapPath) != "" {
			return area, true
		}
	}
	for _, area := range areas {
		if area.Kind == "normal" && strings.TrimSpace(area.MapPath) != "" {
			return area, true
		}
	}
	for _, area := range areas {
		if strings.TrimSpace(area.MapPath) != "" {
			return area, true
		}
	}
	return s.townGateArea(townID)
}

// townTeleportPotionPrincipalArea 按地图/区域语义解析目标地区的**城镇主图**，
// 复用 town 模块的 Table.FindPrincipalArea（判据与真档统计写在 types.go 的注释里）：
// area 0 不是边界门图时就是 area 0（改动前的结果），是边界门图时取文档顺序里
// 第一个非边界图区域。解析不出来（城镇没有非边界图）时返回 found=false，
// 调用方据此逐字节回退到 townTeleportPotionLandingArea。
func (s *Service) townTeleportPotionPrincipalArea(townID int64) (dnftown.Area, bool) {
	if s == nil {
		return dnftown.Area{}, false
	}
	s.townCatalogMu.RLock()
	table := s.townCatalog
	s.townCatalogMu.RUnlock()
	if table == nil {
		return dnftown.Area{}, false
	}
	area, found := table.FindPrincipalArea(townID)
	if !found || strings.TrimSpace(area.MapPath) == "" {
		return dnftown.Area{}, false
	}
	return area, true
}

// townTeleportPotionBoundarySignals 返回"area 0 被判成城镇边界图"时命中的信号，
// 供事件日志取证（pvf_gate_marker / gate_map_name）。
func (s *Service) townTeleportPotionBoundarySignals(townID int64) string {
	area, found := s.townArea(townID, 0)
	if !found {
		return ""
	}
	return area.BoundarySignals()
}

// townFirstDeclaredPosition 返回该城镇任意一个区域声明过的落点，按文档顺序优先取
// `[gate]` 门坐标，再取 `[normal] <x> <y>` 的默认位置。它只用于"请求不带点击坐标"
// 的旧体/回城类传送的兜底落点，避免因为没有点击点而无法落地。
func (s *Service) townFirstDeclaredPosition(townID int64) (int64, int64, bool) {
	areas, found := s.townAreas(townID)
	if !found {
		return 0, 0, false
	}
	for _, area := range areas {
		if area.Gate != nil {
			return area.Gate.X, area.Gate.Y, true
		}
	}
	for _, area := range areas {
		if area.DefaultPosition != nil {
			return area.DefaultPosition.X, area.DefaultPosition.Y, true
		}
	}
	return 0, 0, false
}

// townAreas returns every area the runtime PVF declares for one town, in
// document order. Callers that need "the town's Seria room" must scan the whole
// town instead of guessing an area id: the runtime PVF keeps that room at
// different area ids per town (town 38 declares it as area 1).
func (s *Service) townAreas(townID int64) ([]dnftown.Area, bool) {
	if s == nil {
		return nil, false
	}
	s.townCatalogMu.RLock()
	table := s.townCatalog
	s.townCatalogMu.RUnlock()
	if table == nil {
		return nil, false
	}
	town, found := table.Find(townID)
	if !found || len(town.Areas) == 0 {
		return nil, false
	}
	return town.Areas, true
}
