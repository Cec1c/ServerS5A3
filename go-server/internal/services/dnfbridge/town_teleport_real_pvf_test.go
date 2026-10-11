package dnfbridge

import (
	"context"
	"os"
	"testing"

	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真档 Script.pvf 上钉住瞬间移动药剂(2600014)的落点判据。
//
// 运行：
//
//	DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf 路径> go test ./internal/services/dnfbridge -run RealScriptPVFTeleportPotion
//
// 现场证据（2026-09-25 香港封包日志，op237 = ENUM_CMDPACKET_TELEPORT，13 字节 body）：
//
//	packet_log.20260925-212540.txt:1739  town=43 x=1461 y=893  （请求前在 39/2 new_backstreet_1.map）
//	packet_log.20260925-212540.txt:1537  town=38 x=579  y=204  （请求前在 38/1 new_seria_room.map）
//	packet_log.20260925-212540.txt:1989  town=38 x=834  y=293  （请求前在 38/1 new_seria_room.map）
//	packet_log.20260925-202024.txt:2021  town=39 x=1990 y=297
//	packet_log.20260925-192600.txt:2762  town=39 x=2420 y=304
//
// 请求前所在区域的现场坐标带（同一批日志里所有 set-user-area-committed 的 x/y 范围）：
//
//	new_backstreet_1.map  x∈[197,639]   y∈[178,353]
//	new_seria_room.map    x∈[433,873]   y∈[233,311]
//
// 上一版把落点固定成目标城镇的 PVF `[gate]` 区（FindGateArea），于是：
//  1. 目标城镇根本没有 `[gate]` 区时直接回 {00, 23} —— 操作者看到客户端弹
//     「对方正忙，无法对其使用该功能。」（现场 town 43/11/12 三次命中）；
//  2. 有 `[gate]` 区时落到那个门坐标（town 38 → 赛丽亚房间 450 234），与点击点无关。
func TestRealScriptPVFTeleportPotionLandingArea(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the real town teleport-potion landing catalog")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatalf("load real town catalog: %v", err)
	}
	service := &Service{townCatalog: table}

	// 1) 症状二的根因：这些真档城镇只声明 [normal]/[dungeon gate] 区，FindGateArea 找不到，
	//    上一版就在这里硬失败。它们的目标区域必须是 area 0（该地区的主城区）。
	gateLessTowns := map[int64]string{
		11: "Town/Shonan/Shonan_Gymnasium.map",
		12: "Town/TimeGateArea/TimeGateAreaWR.map",
		43: "Cataclysm/Town/StormPass/StormPass.map",
	}
	for townID, wantMap := range gateLessTowns {
		if _, found := service.townGateArea(townID); found {
			t.Fatalf("real PVF town %d now declares a [gate] area; the old hard failure no longer reproduces, re-check the evidence", townID)
		}
		area, found := service.townTeleportPotionLandingArea(townID)
		if !found || area.ID != 0 || area.MapPath != wantMap {
			t.Fatalf("real PVF town %d landing area=%+v found=%t want area=0 map=%q", townID, area, found, wantMap)
		}
	}

	// 2) 症状一的判据：目标区域是**目标地区的主城区**，不是当前所在区域、也不是门区。
	mainMaps := map[int64]string{
		38: "Cataclysm/Town/Elvengard/new_Elvengard.map",
		39: "Cataclysm/Town/HendonMyre/new_Hendon_Main.map",
		40: "Cataclysm/Town/west_coast/WestCoast.map",
	}
	for townID, wantMap := range mainMaps {
		area, found := service.townTeleportPotionLandingArea(townID)
		if !found || area.ID != 0 || area.MapPath != wantMap {
			t.Fatalf("real PVF town %d landing area=%+v found=%t want area=0 map=%q", townID, area, found, wantMap)
		}
	}
	if room, found := service.townGateArea(38); !found || room.ID != 1 || !townMapPathLooksLikeSeria(room.MapPath) {
		t.Fatalf("real PVF town 38 gate area=%+v found=%t: the old rule landed inside the Seria room", room, found)
	}

	// 3) 点击点坐标只与"目标地区主城区"自洽，与"请求前所在区域"不自洽 —— 这正是
	//    「落点必须用请求里的点击点 + 目标地区主城区」这两条判据的现场依据。
	type liveSample struct {
		name           string
		townID         int64
		clickX, clickY int64
		currentMap     string
		currentBand    [4]int64 // xMin xMax yMin yMax
		insideCurrent  bool
	}
	samples := []liveSample{
		{
			name: "town43_from_backstreet1", townID: 43, clickX: 1461, clickY: 893,
			currentMap:  "Cataclysm/Town/HendonMyre/new_backstreet_1.map",
			currentBand: [4]int64{197, 639, 178, 353}, insideCurrent: false,
		},
		{
			name: "town38_from_seria_room_x579", townID: 38, clickX: 579, clickY: 204,
			currentMap:  "Cataclysm/Town/Elvengard/new_seria_room.map",
			currentBand: [4]int64{433, 873, 233, 311}, insideCurrent: false,
		},
		{
			name: "town38_from_seria_room_x743", townID: 38, clickX: 743, clickY: 223,
			currentMap:  "Cataclysm/Town/Elvengard/new_seria_room.map",
			currentBand: [4]int64{433, 873, 233, 311}, insideCurrent: false,
		},
	}
	for _, sample := range samples {
		inside := sample.clickX >= sample.currentBand[0] && sample.clickX <= sample.currentBand[1] &&
			sample.clickY >= sample.currentBand[2] && sample.clickY <= sample.currentBand[3]
		if inside != sample.insideCurrent {
			t.Fatalf("live sample %s: click (%d,%d) inside %s band %v = %t, want %t",
				sample.name, sample.clickX, sample.clickY, sample.currentMap, sample.currentBand, inside, sample.insideCurrent)
		}
		area, found := service.townTeleportPotionLandingArea(sample.townID)
		if !found {
			t.Fatalf("live sample %s: real PVF town %d has no landing area", sample.name, sample.townID)
		}
		if townMapPathLooksLikeSeria(area.MapPath) || area.MapPath == sample.currentMap {
			t.Fatalf("live sample %s: landing map %q must be the destination region's main map, not the requester's current map %q",
				sample.name, area.MapPath, sample.currentMap)
		}
	}
}
