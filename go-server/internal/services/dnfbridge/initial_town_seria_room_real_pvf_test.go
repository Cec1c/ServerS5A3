package dnfbridge

import (
	"context"
	"os"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本用例用**真实运行时 Script.pvf** 钉住"每个城镇的赛丽亚房间"这条判据的事实，
// 免得下一轮又靠猜：登录落点的判据只有 town 文档的 `[area]` 地图路径
// （townMapPathLooksLikeSeria，见 town_seria_room.go），而实测结论是——
//
//   - town 38 `艾尔文防线`（town/new_Elvengard.twn）area 1 =
//     `Cataclysm/Town/Elvengard/new_seria_room.map`，`[gate]` 450 234 ← 唯一的城镇赛丽亚房间；
//   - town 39/40/41/42/43（赫顿玛尔/西海岸/阿法利亚营地/暗黑城/风暴航线）以及
//     44/45/46 的 `[area]` 全是街道、后巷、酒馆或 `[dungeon gate]`，没有 seria 房间。
//
// 于是三级落点在真实数据上的表现是：持久化 town_id = 38 的角色落在 38/1（第一级）；
// 39..46 的角色**默认**落回 38/1 的赛丽亚房间（第三级，第二级被门控关闭，见下）；
// 只有显式 `DNFBRIDGE_LOGIN_PERSISTED_TOWN=keep` 时才停在该城镇自己的持久化精确落点
// （第二级，不跨城镇），见 TestRealScriptPVFCharacterListLoginDefaultsToSeriaRoom。
// 将来若某份 PVF 给这些城镇补了赛丽亚房间，本用例会失败，提醒重新核对落点口径。
//
// 运行：DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf 路径> go test ./internal/services/dnfbridge -run RealScriptPVF
func TestRealScriptPVFTownSeriaRoomCatalog(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the real town Seria-room catalog")
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

	room, found := service.currentTownSeriaRoomArea(newCharacterInitialTownID)
	if !found || room.ID != newCharacterInitialAreaID ||
		room.MapPath != "Cataclysm/Town/Elvengard/new_seria_room.map" ||
		room.Gate == nil || room.Gate.X != newCharacterInitialPosX || room.Gate.Y != newCharacterInitialPosY {
		t.Fatalf("real runtime PVF town 38 Seria room=%+v found=%t", room, found)
	}
	location, ok, reason := service.currentTownSeriaRoomLoginLocation(newCharacterInitialTownID)
	if !ok || reason != "" || location.MapPath != room.MapPath ||
		location.PositionX != newCharacterInitialPosX || location.PositionY != newCharacterInitialPosY ||
		location.Branch != currentTownLoginBranchTownSeriaRoom {
		t.Fatalf("real runtime PVF town 38 login location=%+v ok=%t reason=%q", location, ok, reason)
	}

	for _, townID := range []int64{39, 40, 41, 42, 43, 44, 45, 46} {
		town, found := table.Find(townID)
		if !found || len(town.Areas) == 0 {
			t.Fatalf("real runtime PVF town %d is missing", townID)
		}
		if room, found := service.currentTownSeriaRoomArea(townID); found {
			t.Fatalf(
				"real runtime PVF town %d now declares a Seria room %+v; the three-tier login rule must be re-checked",
				townID, room)
		}
		if _, ok, reason := service.currentTownSeriaRoomLoginLocation(townID); ok ||
			!strings.Contains(reason, "has_no_seria_room_in_runtime_pvf") {
			t.Fatalf("real runtime PVF town %d login location ok=%t reason=%q", townID, ok, reason)
		}
		// 第二级必须停在该城镇自己的一个区域里（不跨城镇）：该城镇的每个 `[area]`
		// 都能作为持久化落点被运行时 PVF 校验接受。
		first := town.Areas[0]
		persisted := dnfrepo.CharacterRecord{
			CharacterID: "29",
			Stats: map[string]int64{
				"town_id":    townID,
				"area_id":    first.ID,
				"pos_x":      70,
				"pos_y":      80,
				"direction":  5,
				"area_state": 3,
			},
		}
		kept, ok, reason := service.currentPersistedTownLoginLocation(29, persisted)
		if !ok || reason != "" || int64(kept.TownID) != townID ||
			kept.AreaID != byte(first.ID) || kept.MapPath != first.MapPath ||
			kept.Branch != currentTownLoginBranchPersistedPosition {
			t.Fatalf("real runtime PVF town %d persisted tier location=%+v ok=%t reason=%q", townID, kept, ok, reason)
		}
	}

	// 第三级回落落点必须在真实 PVF 里存在，否则登录路径会直接失败。
	fallback, err := service.currentInitialTownSeriaLoginLocation()
	if err != nil {
		t.Fatalf("real runtime PVF 38/1 fallback location: %v", err)
	}
	if fallback.TownID != newCharacterInitialTownID || fallback.AreaID != newCharacterInitialAreaID ||
		fallback.Source != currentTownLoginLocationSeriaGateSource ||
		fallback.Branch != currentTownLoginBranchElvengardSeria ||
		!townMapPathLooksLikeSeria(fallback.MapPath) {
		t.Fatalf("real runtime PVF fallback location=%+v", fallback)
	}
}

// 真实运行时 PVF 上的登录落点默认口径（操作者 2026-09-26 报障
// 「选择角色重新进入游戏会在上次下线的地方出现」）：
//
//	持久化 town 39（赫顿玛尔，真档没有赛丽亚房间）+ 持久化街道坐标 600/250
//	  → 默认落 38/1 的赛丽亚房间 gate（450/234），branch=elvengard_seria_fallback；
//	  → 只有 DNFBRIDGE_LOGIN_PERSISTED_TOWN=keep 时才停在 39/<area0> @600/250。
func TestRealScriptPVFCharacterListLoginDefaultsToSeriaRoom(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the real login landing rule")
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

	town, found := table.Find(39)
	if !found || len(town.Areas) == 0 {
		t.Fatal("real runtime PVF town 39 (Hendon Myre) is missing")
	}
	first := town.Areas[0]
	persisted := dnfrepo.CharacterRecord{
		CharacterID: "29",
		Stats: map[string]int64{
			"town_id":    39,
			"area_id":    first.ID,
			"pos_x":      600,
			"pos_y":      250,
			"direction":  5,
			"area_state": 3,
		},
	}

	location, persistedTownID, fallbackReason, _, err := service.currentCharacterListLoginLocation(29, persisted)
	if err != nil {
		t.Fatalf("default login location: %v", err)
	}
	if persistedTownID != 39 {
		t.Fatalf("default login persisted_town_id=%d, want 39", persistedTownID)
	}
	if location.TownID != newCharacterInitialTownID || location.AreaID != newCharacterInitialAreaID ||
		location.PositionX != newCharacterInitialPosX || location.PositionY != newCharacterInitialPosY ||
		location.Branch != currentTownLoginBranchElvengardSeria ||
		location.Source != currentTownLoginLocationSeriaGateSource {
		t.Fatalf("default login location=%+v, want the 38/1 Seria room", location)
	}
	if want := "persisted_town_position_gated_by_default_env_" + currentLoginPersistedTownEnv; fallbackReason != want {
		t.Fatalf("default login fallback_reason=%q, want %q", fallbackReason, want)
	}

	t.Setenv(currentLoginPersistedTownEnv, currentLoginPersistedTownKeepValue)
	location, _, fallbackReason, _, err = service.currentCharacterListLoginLocation(29, persisted)
	if err != nil {
		t.Fatalf("gated login location: %v", err)
	}
	if location.TownID != 39 || location.AreaID != byte(first.ID) ||
		location.PositionX != 600 || location.PositionY != 250 ||
		location.Branch != currentTownLoginBranchPersistedPosition ||
		location.Source != currentTownLoginLocationPersistedSource {
		t.Fatalf("gated login location=%+v, want the persisted town-39 position", location)
	}
	if fallbackReason != "persisted_position" {
		t.Fatalf("gated login fallback_reason=%q, want persisted_position", fallbackReason)
	}
}
