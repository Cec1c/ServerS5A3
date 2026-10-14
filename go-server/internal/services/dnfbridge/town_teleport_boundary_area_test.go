package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 真档 town/CentralPark.twn 的形态（Script.pvf sha256 cc517a6e…，见
// internal/modules/dnf/town/principal_area_real_pvf_test.go 的全量扫描）：
//
//	area 0 `Town/CentralPark/Gate_CentralPark.map` `[gate]` 474 234  ← 城镇边界门图
//	area 1 `Town/CentralPark/CentralPark.map`      `[normal]`        ← 主街（操作者实证）
//	area 2 `Town/CentralPark/CentralPark_Waiting.map` `[dungeon gate]` 39
func centralParkBoundaryTownDocument() string {
	return strings.Join([]string{
		"[area]",
		"0 `Town/CentralPark/Gate_CentralPark.map`",
		"`[gate]` 474 234",
		"[/area]",
		"[area]",
		"1 `Town/CentralPark/CentralPark.map`",
		"`[normal]`",
		"[/area]",
		"[area]",
		"2 `Town/CentralPark/CentralPark_Waiting.map`",
		"`[dungeon gate]` 39",
		"[/area]",
		"[name]",
		"`中央公园`",
	}, "\n")
}

// 现场（packet_log.txt，conn_id=game-000028，char_id=3）：
//
//	game-town-teleport-potion-committed town_id=31 area_id=0 position_x=675 position_y=342
//	  map_path=Town/CentralPark/Gate_CentralPark.map landing_source=client_click_point
//
// 之后客户端假死：角色模型消失、能交互 NPC、不能移动。根因是把主街点击点提交给了
// `Gate_*` 边界门图。修好后同一条请求必须落到该城镇主街 area 1 `CentralPark.map`。
func TestHandleTeleportPotionLandsOnMainAreaPastBoundaryGateAtAreaZero(t *testing.T) {
	service, session, repositories := newTeleportPotionTownCatalogTest(t,
		"31 `CentralPark.twn`",
		map[string]string{"town/CentralPark.twn": centralParkBoundaryTownDocument()})

	// 改动前的选择器仍然指向边界门图 —— 这条断言证明回归是真的（没有它，本用例
	// 可能在"旧代码本来就对"的情况下通过）。
	if legacy, found := service.townTeleportPotionLandingArea(31); !found || legacy.ID != 0 {
		t.Fatalf("fixture must reproduce the bug: legacy landing area=(%+v,%t) want area 0", legacy, found)
	}

	if err := service.handleTeleportPotion(session, buildTeleportPotionAreaClickRequest(65, 2600014, 31, 1, 675, 342)); err != nil {
		t.Fatal(err)
	}

	splitTownTransitionAndPostState(
		t,
		session.conn.(*bufferConn).write.Bytes(),
		session,
		29,
		31,
		1,
		675,
		342,
		0,
		3,
		townMoveSkillProjectionBody(t, repositories, "29"),
		false,
	)
	stored, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load stored character found=%t err=%v", found, err)
	}
	if stored.Stats["town_id"] != 31 || stored.Stats["area_id"] != 1 ||
		stored.Stats["pos_x"] != 675 || stored.Stats["pos_y"] != 342 {
		t.Fatalf("teleport potion still landed in the Gate_ boundary map: %+v", stored.Stats)
	}
	inventory, _, err := repositories.Inventory.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Slots["0:65"].Count != 1 {
		t.Fatalf("potion after consume=%+v", inventory.Slots["0:65"])
	}
}

// 落点与区域必须成对：请求不带点击点（8 字节旧体）而主街区域自己不声明落点时，
// 不能拿边界图的 `[gate]` 坐标去主街上落地（那是另一张图的坐标），也不能发明坐标 ——
// 逐字节回退到改动前那一对（边界门图 + 它自己的门坐标），并在事件里记 reason=。
func TestHandleTeleportPotionWithoutClickPointKeepsLegacyBoundaryPair(t *testing.T) {
	service, session, repositories := newTeleportPotionTownCatalogTest(t,
		"31 `CentralPark.twn`",
		map[string]string{"town/CentralPark.twn": centralParkBoundaryTownDocument()})

	character, _, err := repositories.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	decision, failure := service.currentTeleportPotionLandingDecision(
		currentTeleportPotionRequest{TownID: 31, ItemCode: 2600014}, character)
	if failure != "" {
		t.Fatalf("no click point must still commit the legacy pair, got failure=%s", failure)
	}
	if decision.Area.ID != 0 || decision.Area.MapPath != "Town/CentralPark/Gate_CentralPark.map" ||
		decision.X != 474 || decision.Y != 234 || decision.PositionSource != "target_area_gate" {
		t.Fatalf("legacy boundary pair=%+v want area 0 Gate_CentralPark.map at (474,234)", decision)
	}
	if decision.AreaSource != currentTeleportPotionAreaSourceLegacyKept ||
		decision.Reason != "main_area_declares_no_landing_position_and_request_has_no_click_point" {
		t.Fatalf("rollback decision must be attributable: source=%q reason=%q", decision.AreaSource, decision.Reason)
	}

	if err := service.handleTeleportPotion(session, buildTeleportPotionRequest(65, 2600014, 31)); err != nil {
		t.Fatal(err)
	}
	splitTownTransitionAndPostState(
		t,
		session.conn.(*bufferConn).write.Bytes(),
		session,
		29,
		31,
		0,
		474,
		234,
		0,
		3,
		townMoveSkillProjectionBody(t, repositories, "29"),
		false,
	)
	stored, _, err := repositories.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Stats["town_id"] != 31 || stored.Stats["area_id"] != 0 ||
		stored.Stats["pos_x"] != 474 || stored.Stats["pos_y"] != 234 {
		t.Fatalf("no-click rollback pair changed: %+v", stored.Stats)
	}
}

// 扫描型用例：同一张目录下逐个城镇核对"改动前后的选择"。
//   - area 0 不是 `Gate_*` ⇒ 与改动前完全相同（区域、坐标、来源都要一致）；
//   - area 0 是 `Gate_*`（15/53 个真档城镇的形态）⇒ 改取主城区并提交点击点；
//   - area 0 带 `[gate]` 但文件名不是 `Gate_*`（真档 town 33/103 的村图形态）⇒ 不动。
func TestTeleportPotionLandingDecisionScanMatchesLegacyExceptBoundaryGateTowns(t *testing.T) {
	service, _, repositories := newTeleportPotionTownCatalogTest(t,
		"31 `CentralPark.twn` 38 `new_Elvengard.twn` 39 `new_HendonMyre.twn` 33 `ReturneeCare.twn`",
		map[string]string{
			"town/CentralPark.twn": centralParkBoundaryTownDocument(),
			"town/new_Elvengard.twn": "[area]\n0 `Cataclysm/Town/Elvengard/new_Elvengard.map` `[normal]`\n[/area]\n" +
				"[area]\n1 `Cataclysm/Town/Elvengard/new_seria_room.map` `[gate]` 450 234\n[/area]\n[name]\n`Elvengard`",
			"town/new_HendonMyre.twn": "[area]\n0 `Cataclysm/Town/HendonMyre/new_Hendon_Main.map` `[dungeon gate]` 102\n[/area]\n" +
				"[area]\n2 `Cataclysm/Town/HendonMyre/new_backstreet_1.map` `[normal]`\n[/area]\n[name]\n`HendonMyre`",
			"town/ReturneeCare.twn": "[area]\n0 `Town/Returnee/ReaturneeTown0.map` `[gate]` 594 234\n[/area]\n[name]\n`ReturneeCare`",
		})
	character, _, err := repositories.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	request := currentTeleportPotionRequest{
		PotionSlotIndex: 65, ItemCode: 2600014, TownID: 31,
		ClickX: 675, ClickY: 342, HasClickPoint: true,
	}
	type want struct {
		areaID      int64
		mapPath     string
		areaSource  string
		posX, posY  int64
		posSource   string
		description string
	}
	cases := []struct {
		townID byte
		want   want
	}{
		{
			townID: 31,
			want: want{
				areaID: 1, mapPath: "Town/CentralPark/CentralPark.map",
				areaSource: currentTeleportPotionAreaSourceAfterGate,
				posX:       675, posY: 342, posSource: "client_click_point",
				description: "area 0 是 Gate_ 边界门图 ⇒ 落到主街并提交点击点",
			},
		},
		{
			townID: 38,
			want: want{
				areaID: 0, mapPath: "Cataclysm/Town/Elvengard/new_Elvengard.map",
				areaSource: currentTeleportPotionAreaSourceDocumentOrder,
				posX:       675, posY: 342, posSource: "client_click_point",
				description: "area 0 本来就是主街 ⇒ 与改动前逐字节一致",
			},
		},
		{
			townID: 39,
			want: want{
				areaID: 0, mapPath: "Cataclysm/Town/HendonMyre/new_Hendon_Main.map",
				areaSource: currentTeleportPotionAreaSourceDocumentOrder,
				posX:       675, posY: 342, posSource: "client_click_point",
				description: "area 0 只带 [dungeon gate] ⇒ 仍是主街，不动",
			},
		},
		{
			townID: 33,
			want: want{
				areaID: 0, mapPath: "Town/Returnee/ReaturneeTown0.map",
				areaSource: currentTeleportPotionAreaSourceDocumentOrder,
				posX:       675, posY: 342, posSource: "client_click_point",
				description: "带 [gate] 但不是 Gate_ 命名的村图 ⇒ 不动",
			},
		},
	}
	for _, item := range cases {
		request.TownID = item.townID
		decision, failure := service.currentTeleportPotionLandingDecision(request, character)
		if failure != "" {
			t.Fatalf("town %d (%s): unexpected failure=%s", item.townID, item.want.description, failure)
		}
		if decision.Area.ID != item.want.areaID || decision.Area.MapPath != item.want.mapPath ||
			decision.AreaSource != item.want.areaSource ||
			decision.X != item.want.posX || decision.Y != item.want.posY ||
			decision.PositionSource != item.want.posSource {
			t.Fatalf("town %d (%s): decision=%+v want area=%d %s source=%s pos=(%d,%d) %s",
				item.townID, item.want.description, decision, item.want.areaID, item.want.mapPath,
				item.want.areaSource, item.want.posX, item.want.posY, item.want.posSource)
		}
		if item.want.description == "" {
			t.Fatalf("town %d: fixture is missing a description", item.townID)
		}
		if decision.Reason != "" {
			t.Fatalf("town %d (%s): default-mode decision must not carry reason=%q", item.townID, item.want.description, decision.Reason)
		}
	}

	// 逐字节回退基线：非 Gate_ 城镇上，改动前的选择器与默认档决定的区域完全一致。
	for _, townID := range []int64{38, 39, 33} {
		legacy, found := service.townTeleportPotionLandingArea(townID)
		if !found {
			t.Fatalf("town %d has no legacy landing area", townID)
		}
		request.TownID = byte(townID)
		decision, _ := service.currentTeleportPotionLandingDecision(request, character)
		if decision.Area.ID != legacy.ID || decision.Area.MapPath != legacy.MapPath {
			t.Fatalf("town %d: default mode moved a town that must not move: legacy=%+v decision=%+v", townID, legacy, decision.Area)
		}
	}
}

// 只有一张 `Gate_` 边界图、没有任何非边界区域的城镇（真档里不存在）：解析不出主城区 ⇒
// 逐字节回退到改动前的选择，并在事件里记 reason=。这里同时钉住"回退时仍然提交点击点"
// 这一条改动前行为（不是我们发明的行为）。
func TestTeleportPotionLandingDecisionFallsBackToLegacyWhenTownHasOnlyBoundaryArea(t *testing.T) {
	service, _, repositories := newTeleportPotionTownCatalogTest(t,
		"77 `gate_only.twn`",
		map[string]string{
			"town/gate_only.twn": "[area]\n0 `Town/Example/Gate_Example.map` `[gate]` 10 20\n[/area]\n[name]\n`Example`",
		})
	character, _, err := repositories.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	request := currentTeleportPotionRequest{
		PotionSlotIndex: 65, ItemCode: 2600014, TownID: 77,
		ClickX: 700, ClickY: 300, HasClickPoint: true,
	}
	legacy, found := service.townTeleportPotionLandingArea(77)
	if !found || legacy.ID != 0 {
		t.Fatalf("fixture legacy landing area=(%+v,%t)", legacy, found)
	}
	decision, failure := service.currentTeleportPotionLandingDecision(request, character)
	if failure != "" {
		t.Fatalf("boundary-only town must keep the legacy commit, got failure=%s", failure)
	}
	if decision.Area.ID != legacy.ID || decision.Area.MapPath != legacy.MapPath ||
		decision.X != request.ClickX || decision.Y != request.ClickY {
		t.Fatalf("rollback is not byte-identical to the legacy choice: legacy=%+v decision=%+v", legacy, decision)
	}
	if decision.AreaSource != currentTeleportPotionAreaSourceDocumentOrder {
		// area 0 既是边界图也是唯一区域 ⇒ FindPrincipalArea 原样返回它，
		// 区域来源是"文档顺序第一个区域"，而不是"回退"。回退分支留给
		// 连 area 0 都解析不出来的档（见下一条断言）。
		t.Fatalf("areaSource=%q", decision.AreaSource)
	}
}

// 目标城镇在目录里根本不存在（连 area 0 都没有）时，失败口径与改动前一字不变。
func TestTeleportPotionLandingDecisionUnknownTownKeepsLegacyFailure(t *testing.T) {
	service, session, _ := newTeleportPotionTest(t)
	if err := service.handleTeleportPotion(session, buildTeleportPotionRequest(65, 2600014, 99)); err != nil {
		t.Fatal(err)
	}
	assertTeleportPotionFailure(t, session)
}

// 事件字段：默认档成功落地要留下 landing_area_source=（否则线上无法按档核对判据），
// 回退档必须留下 reason=。
func TestTeleportPotionCommittedEventCarriesLandingAreaSourceAndReason(t *testing.T) {
	service, session, repositories := newTeleportPotionTownCatalogTest(t,
		"31 `CentralPark.twn`",
		map[string]string{"town/CentralPark.twn": centralParkBoundaryTownDocument()})
	logPath := filepath.Join(t.TempDir(), "packet_log.txt")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service.packetLog = logger

	if err := service.handleTeleportPotion(session, buildTeleportPotionAreaClickRequest(65, 2600014, 31, 1, 675, 342)); err != nil {
		t.Fatal(err)
	}
	// 把角色挪回 town 38 area 1，再打一条不带点击点的请求，触发回退档。
	character, _, err := repositories.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	character = dnfrepo.CloneCharacter(character)
	character.Stats["town_id"] = 38
	character.Stats["area_id"] = 1
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	session.conn.(*bufferConn).write.Reset()
	if err := service.handleTeleportPotion(session, buildTeleportPotionRequest(65, 2600014, 31)); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"kind=game-town-teleport-potion-committed",
		"town_id=31 area_id=1 position_x=675 position_y=342 map_path=Town/CentralPark/CentralPark.map",
		"landing_source=client_click_point",
		"landing_area_source=client_requested_area",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("committed event line missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "reason=main_area_declares_no_landing_position_and_request_has_no_click_point") {
		t.Fatalf("rollback commit must carry reason=:\n%s", text)
	}
	if strings.Contains(text, "reason=main_area_declares_no_landing_position_and_request_has_no_click_point") &&
		!strings.Contains(text, "area_id=0 position_x=474 position_y=234 map_path=Town/CentralPark/Gate_CentralPark.map") {
		t.Fatalf("rollback commit must keep the legacy boundary pair:\n%s", text)
	}
}

// 回退开关 DNFBRIDGE_TELEPORT_POTION_LANDING=gate 必须逐字节等于改动前：区域 = PVF
// `[gate]` 区（这里就是 area 0 边界门图）、坐标 = 门坐标、忽略点击点。
func TestTeleportPotionLandingDecisionGateSwitchKeepsLegacyBoundaryLanding(t *testing.T) {
	t.Setenv(currentTeleportPotionLandingEnv, currentTeleportPotionLandingLegacy)
	service, session, repositories := newTeleportPotionTownCatalogTest(t,
		"31 `CentralPark.twn`",
		map[string]string{"town/CentralPark.twn": centralParkBoundaryTownDocument()})
	character, _, err := repositories.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	decision, failure := service.currentTeleportPotionLandingDecision(
		currentTeleportPotionRequest{TownID: 31, ItemCode: 2600014, ClickX: 675, ClickY: 342, HasClickPoint: true},
		character)
	if failure != "" {
		t.Fatalf("gate switch must commit the legacy target, got failure=%s", failure)
	}
	if decision.Area.ID != 0 || decision.Area.MapPath != "Town/CentralPark/Gate_CentralPark.map" ||
		decision.X != 474 || decision.Y != 234 || decision.PositionSource != "pvf_gate_legacy" ||
		decision.AreaSource != currentTeleportPotionAreaSourceLegacyGate {
		t.Fatalf("gate switch decision=%+v want the legacy [gate] area at (474,234)", decision)
	}

	if err := service.handleTeleportPotion(session, buildTeleportPotionAreaClickRequest(65, 2600014, 31, 1, 675, 342)); err != nil {
		t.Fatal(err)
	}
	splitTownTransitionAndPostState(
		t,
		session.conn.(*bufferConn).write.Bytes(),
		session,
		29,
		31,
		0,
		474,
		234,
		0,
		3,
		townMoveSkillProjectionBody(t, repositories, "29"),
		false,
	)
	stored, _, err := repositories.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Stats["town_id"] != 31 || stored.Stats["area_id"] != 0 ||
		stored.Stats["pos_x"] != 474 || stored.Stats["pos_y"] != 234 {
		t.Fatalf("gate-switch rollback changed the committed location: %+v", stored.Stats)
	}
}

// dnftown 的解析结果与 dnfbridge 的判据必须同一套目录：这里只核对两者读的是同一张表，
// 防止有人日后在 bridge 里新造第二套城镇目录。
func TestTeleportPotionPrincipalAreaComesFromTheSharedTownTable(t *testing.T) {
	service, _, _ := newTeleportPotionTownCatalogTest(t,
		"31 `CentralPark.twn`",
		map[string]string{"town/CentralPark.twn": centralParkBoundaryTownDocument()})
	area, found := service.townTeleportPotionPrincipalArea(31)
	if !found || area.ID != 1 || area.MapPath != "Town/CentralPark/CentralPark.map" {
		t.Fatalf("principal area=(%+v,%t)", area, found)
	}
	if service.townTeleportPotionBoundarySignals(31) != "gate_map_name+pvf_gate_marker" {
		t.Fatalf("boundary signals=%q", service.townTeleportPotionBoundarySignals(31))
	}
	table := service.townCatalog
	direct, found := table.FindPrincipalArea(31)
	if !found || direct.ID != area.ID || direct.MapPath != area.MapPath {
		t.Fatalf("bridge resolver diverged from the shared town table: %+v vs %+v", area, direct)
	}
	if bytes.Contains([]byte(area.MapPath), []byte("Gate_")) {
		t.Fatal("principal area must never be a Gate_ boundary map")
	}
}
