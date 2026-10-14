package dnfbridge

import (
	"context"
	"encoding/hex"
	"os"
	"testing"

	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 现场 op237 原始请求体（线上 packet_log 的 RECV type=237 行逐字，只读抓取）：
// body = i16 potionSlot, i32 itemCode, u8, u8 town, u8 **requestedArea**, u16 x, u16 y。
// wantArea is the selected map record ID carried by the current EXE.
// Coordinates may overlap across maps and must not change that selection.
var currentTeleportPotionRequestedAreaCases = []struct {
	name     string
	bodyHex  string
	town     int64
	wantArea int64
	x, y     uint16
	change   string
	at       string
}{
	{"elvengard_death_valley_19_23_08", "06004eac2700002605e5017e01", 38, 5, 485, 382, "changed", "2026-10-14T11:23:08Z"},
	{"elvengard_death_valley_19_22_38", "06004eac270000260564027401", 38, 5, 612, 372, "changed", "2026-10-14T11:22:38Z"},
	{"elvengard_death_valley_19_22_24", "06004eac270000260590018801", 38, 5, 400, 392, "changed", "2026-10-14T11:22:24Z"},
	{"elvengard_death_valley_10_47_05", "06004eac27000026051d029201", 38, 5, 541, 402, "changed", "2026-10-14T10:47:05Z"},
	{"elvengard_main_19_22_08", "06004eac27000026007805f900", 38, 0, 1400, 249, "unchanged", "2026-10-14T11:22:08Z"},
	{"shonan_workshop_area_1_15_53_48", "48004eac2700000b013204d200", 11, 1, 1074, 210, "changed", "2026-10-13T15:53:48Z"},
	{"centralpark_area_2_08_16_32", "06004eac2700001f02f6004001", 31, 2, 246, 320, "changed", "2026-10-14T08:16:32Z"},
	{"slough_area_2_08_18_58", "06004eac2700000e0250013901", 14, 2, 336, 313, "changed", "2026-10-14T08:18:58Z"},
	{"stormpass_area_1_15_33_09", "42004eac2700002b015101c101", 43, 1, 337, 449, "changed", "2026-10-13T15:33:09Z"},
	{"jelva_area_1_15_33_35", "42004eac27000016012c077e01", 22, 1, 1836, 382, "unchanged", "2026-10-13T15:33:35Z"},
}

// TestRealScriptPVFTeleportPotionRequestedAreaLanding 用**真实 handleTeleportPotion** 喂
// 现场原始 body，核对：提交区域 / 落点 / 持久化 / 药水恰好扣 1 / op24 切图包里的区域与落点
// 与落库一致。基线（5fc6807，忽略 body[8]）在 change=changed 的 8 条上提交错误区域 ⇒ 红。
func TestRealScriptPVFTeleportPotionRequestedAreaLanding(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatalf("load real town catalog: %v", err)
	}
	for _, sample := range currentTeleportPotionRequestedAreaCases {
		t.Run(sample.name, func(t *testing.T) {
			service, session, repositories := newTeleportPotionTest(t)
			service.townCatalog = table
			// 生产路径上 currentPVFItemCatalog 已经加载过同一份运行时档案；测试里直接注入。
			service.initialEquipmentArchive = archive
			body, decodeErr := hex.DecodeString(sample.bodyHex)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if err := service.handleTeleportPotion(session, body); err != nil {
				t.Fatalf("handleTeleportPotion error = %v", err)
			}
			// 1) 持久化：城镇/区域/坐标
			stored, found, loadErr := repositories.Character.Load(context.Background(), "29")
			if loadErr != nil || !found {
				t.Fatalf("load stored character found=%t err=%v", found, loadErr)
			}
			if stored.Stats["town_id"] != sample.town || stored.Stats["area_id"] != sample.wantArea ||
				stored.Stats["pos_x"] != int64(sample.x) || stored.Stats["pos_y"] != int64(sample.y) {
				t.Fatalf("committed location = %+v, want town=%d area=%d pos=(%d,%d) (%s, %s)",
					stored.Stats, sample.town, sample.wantArea, sample.x, sample.y, sample.at, sample.change)
			}
			// 2) 药水恰好扣 1（harness 初始 2 个）
			inventory, _, invErr := repositories.Inventory.Load(context.Background(), "29")
			if invErr != nil {
				t.Fatal(invErr)
			}
			if got := inventory.Slots["0:65"].Count; got != 1 {
				t.Fatalf("teleport potion count = %d, want 1 (2 consumed by exactly one use)", got)
			}
			// 3) op24 切图链：区域/落点与落库一致（splitTownTransitionAndPostState 会逐包核对）
			splitTownTransitionAndPostState(
				t,
				session.conn.(*bufferConn).write.Bytes(),
				session,
				29,
				byte(sample.town),
				byte(sample.wantArea),
				sample.x,
				sample.y,
				0,
				3,
				townMoveSkillProjectionBody(t, repositories, "29"),
				false,
			)
			area, ok := service.townArea(sample.town, sample.wantArea)
			if !ok || area.MapPath == "" {
				t.Fatalf("committed area %d:%d missing from the real catalog", sample.town, sample.wantArea)
			}
			t.Logf("%s (%s): committed town=%d area=%d map=%s click=(%d,%d)",
				sample.at, sample.change, sample.town, sample.wantArea, area.MapPath, sample.x, sample.y)
		})
	}
}
