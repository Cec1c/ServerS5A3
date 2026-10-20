package town

import (
	"context"
	"os"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真档 Script.pvf 全量扫描：钉住"城镇主图"判据的取证数字。
//
// 运行（真档 = 线上 /opt/dnf90/data/dnf/Script.pvf 的同哈希副本，
// sha256 cc517a6e2b7c77a4ba0f30a1869a3ab234e281fe6251e35d60f0f04a82aacee1）：
//
//	DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf> go test -count=1 ./internal/modules/dnf/town \
//	  -run RealScriptPVFPrincipalArea
//
// 现场（packet_log.txt，conn_id=game-000028/000033，char_id=3）：
//
//	game-town-teleport-potion-committed town_id=31 area_id=0 map_path=Town/CentralPark/Gate_CentralPark.map
//	game-town-teleport-potion-committed town_id=30 area_id=0 map_path=Town/MetroCenter/Gate_MetroCenter.map
//
// 而同一份日志里中央公园的主街是 area 1 `Town/CentralPark/CentralPark.map`
// （game-town-set-user-area-transition-send town_id=31 area_id=1）。
// ⇒ "area 0 就是主城区"这个硬编码假设对它们不成立。
func TestRealScriptPVFPrincipalAreaScan(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the principal-area rule against the real town catalog")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	table, err := Load(context.Background(), archive, Options{})
	if err != nil {
		t.Fatalf("load real town catalog: %v", err)
	}
	towns := table.Towns()
	if len(towns) != 53 {
		t.Fatalf("real town catalog changed: towns=%d want 53 (re-measure before touching this pin)", len(towns))
	}

	// 1) 中央公园 / 魔界营地：area 0 是 Gate_* 边界门图，主城区在别的区域。
	centralPark, ok := table.Find(31)
	if !ok || centralPark.Name != "中央公园" {
		t.Fatalf("town 31=(%+v,%t)", centralPark, ok)
	}
	if area, _ := table.FindArea(31, 0); area.MapPath != "Town/CentralPark/Gate_CentralPark.map" ||
		area.Gate == nil || area.Gate.X != 474 || area.Gate.Y != 234 ||
		area.BoundarySignals() != "gate_map_name+pvf_gate_marker" {
		t.Fatalf("town 31 area 0=(%+v) want the Gate_ boundary map", area)
	}
	centralParkMain, found := table.FindPrincipalArea(31)
	if !found || centralParkMain.ID != 1 || centralParkMain.MapPath != "Town/CentralPark/CentralPark.map" ||
		centralParkMain.IsBoundaryArea() {
		t.Fatalf("town 31 principal=(%+v,%t) want area 1 Town/CentralPark/CentralPark.map", centralParkMain, found)
	}
	metro, ok := table.Find(30)
	if !ok || metro.Name != "魔界营地" {
		t.Fatalf("town 30=(%+v,%t)", metro, ok)
	}
	if area, _ := table.FindArea(30, 0); area.MapPath != "Town/MetroCenter/Gate_MetroCenter.map" || !area.IsBoundaryArea() {
		t.Fatalf("town 30 area 0=(%+v) want the Gate_ boundary map", area)
	}
	metroMain, found := table.FindPrincipalArea(30)
	if !found || metroMain.ID != 1 || metroMain.MapPath != "Town/MetroCenter/Metro_Time.map" {
		t.Fatalf("town 30 principal=(%+v,%t) want area 1 Town/MetroCenter/Metro_Time.map", metroMain, found)
	}

	// 2) 全档扫描：area 0 的文件名以 `Gate_` 开头（城镇边界门图）的城镇有 15 个（真档实测），
	//    这 15 个必须全部解析到"非边界图的主城区"。
	var boundaryAtZero []int64
	for _, town := range towns {
		zero, hasZero := table.FindArea(town.ID, 0)
		if !hasZero || !zero.IsBoundaryArea() {
			continue
		}
		boundaryAtZero = append(boundaryAtZero, town.ID)
		if !zero.HasPVFGateMarker() {
			t.Fatalf("town %d (%s): area 0 %s is a Gate_ boundary map but declares no `[gate]` entry coordinate; re-check the naming-convention evidence",
				town.ID, town.Name, zero.MapPath)
		}
		principal, found := table.FindPrincipalArea(town.ID)
		if !found {
			t.Fatalf("town %d (%s): area 0 %s is a boundary map but no principal area was found",
				town.ID, town.Name, zero.MapPath)
		}
		if principal.IsBoundaryArea() || strings.TrimSpace(principal.MapPath) == "" {
			t.Fatalf("town %d (%s): principal area=(%+v) is still a boundary map", town.ID, town.Name, principal)
		}
		if principal.ID == 0 {
			t.Fatalf("town %d (%s): boundary area 0 must not stay the landing area", town.ID, town.Name)
		}
	}
	wantBoundaryAtZero := []int64{1, 2, 3, 4, 5, 10, 17, 19, 20, 21, 22, 30, 31, 32, 98}
	if len(boundaryAtZero) != len(wantBoundaryAtZero) {
		t.Fatalf("real archive changed: towns with a Gate_ boundary map at area 0 = %v (want %v)", boundaryAtZero, wantBoundaryAtZero)
	}
	for idx, want := range wantBoundaryAtZero {
		if boundaryAtZero[idx] != want {
			t.Fatalf("real archive changed: towns with a Gate_ boundary map at area 0 = %v (want %v)", boundaryAtZero, wantBoundaryAtZero)
		}
	}

	// 3) 其余 38 个城镇一字不改：主城区仍然是 area 0（与改动前的选择逐字节一致）。
	//    其中 3 个城镇（15 时空之门 / 33 生化禁区 / 103 梅林的魔法研究所）的 area 0
	//    带 PVF `[gate]` 入口坐标，但**不是** `Gate_*` 边界图 —— 真档里它们就是城镇主图
	//    （town 33/103 的 area 0 是 `[minimap rect]` + 入口坐标 594 234 的村图，
	//    与主城 town/180201_Wildforest.twn area 0 同形），所以必须留在 area 0。
	markerOnlyAtZero := 0
	for _, town := range towns {
		zero, hasZero := table.FindArea(town.ID, 0)
		if !hasZero || zero.IsBoundaryArea() {
			continue
		}
		if zero.HasPVFGateMarker() {
			markerOnlyAtZero++
		}
		principal, found := table.FindPrincipalArea(town.ID)
		if !found || principal.ID != 0 || principal.MapPath != zero.MapPath {
			t.Fatalf("town %d (%s): principal=(%+v) changed away from area 0=%s", town.ID, town.Name, principal, zero.MapPath)
		}
	}
	if markerOnlyAtZero != 3 {
		t.Fatalf("real archive changed: non-Gate_ towns carrying a [gate] marker at area 0 = %d want 3 (towns 15/33/103)", markerOnlyAtZero)
	}

	// 4) 现场那次成功的药水落点（town 9 鲁夫特悬空海港）确认不受影响。
	luft, found := table.FindPrincipalArea(9)
	if !found || luft.ID != 0 || luft.MapPath != "Town/LuftHafen/LuftHafent_Market.map" {
		t.Fatalf("town 9 principal=(%+v,%t) want area 0 Town/LuftHafen/LuftHafent_Market.map", luft, found)
	}
}
