package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 走出赛丽亚房间的落点 = **进来时所在那个城镇**里"通往本房间"的门，而不是"房间所挂城镇"
// 的固定外门口。现场与逐字段 PVF 证据见 town_seria_room_door.go 的文件头（报障二）：
//
//	香港 packet_log.txt:403  origin_town_id=40（西海岸）source=current_exe_op36_enter_seria_room
//	香港 packet_log.txt:492  op24 却答 town_id=38 area_id=0（艾尔文防线）(622,196)
//
// 本文件钉四件事：
//  1. 门的判据两种 PVF 声明形式都认：`[town movable area]` 六元组与
//     `[town movable area with animation]` 的 `[area]` + `[move town]`，且保持文档序；
//  2. 落点**随 PVF 变**、随**来源区域**变（证明没有写死坐标）；
//  3. 从西海岸进 ⇒ 出到西海岸；从艾尔文防线进 ⇒ 出到艾尔文防线（两个方向都对）；
//  4. 无进门来源（登录直接落在房间）仍走上一轮口径；`DNFBRIDGE_TOWN_SERIA_EXIT_DOOR`
//     的 `room_town_door` / `prev_village` 两档一键回退。

// fakePVFMapReader 按地图路径喂**真的 PVF 文本**（走 dnfpvf.Parse + worldmap.ParseMap），
// 这样两种门声明形式都按真档语法解析，单测里不用另造一套假数据结构。
func fakePVFMapReader(t *testing.T, texts map[string]string) func(dnftown.Area) (worldmap.Map, bool) {
	t.Helper()
	return func(area dnftown.Area) (worldmap.Map, bool) {
		text, ok := texts[area.MapPath]
		if !ok {
			return worldmap.Map{}, false
		}
		doc, err := dnfpvf.Parse(area.MapPath, text)
		if err != nil {
			t.Fatalf("parse %s: %v", area.MapPath, err)
		}
		return worldmap.ParseMap(area.ID, area.MapPath, doc), true
	}
}

// westCoastMapPVFText 是西海岸街道（town 40 area 0 `WestCoast.map`）的真档结构切片：
// `[town movable area]` 六元组里**没有**通往 38/1 的门，赛丽亚房门是带开门动画的
// `[town movable area with animation]` → `[movable area]` → `[area] 473 127 100 30` +
// `[move town] 38 1`。中间那个 `[animation change area] 134 117 800 500` 是 4 个整数，
// 必须**不能**被当成门矩形。
const westCoastMapPVFText = "[type]\n`[normal]`\n" +
	"[town movable area]\n" +
	"1013 139 99 50 40 2 1469 464 100 50 40 1 10 50 90 440 39 0\n" +
	"[town movable area name]\n" +
	"0 0 0 `` 0 0 0 `码头` 0 0 0 `赫顿玛尔` 0 0 0 ``\n" +
	"[town movable area with animation]\n" +
	"[movable area]\n" +
	"[area]\n" +
	"473 127 100 30\n" +
	"[move town]\n" +
	"38 1\n" +
	"[basic animation]\n" +
	"`Map\\Cataclysm\\Town\\HendonMyre\\Animation\\chn_10th\\SeriaGate\\Seria_Basic.ani`\n" +
	"[animation change area]\n" +
	"134 117 800 500\n" +
	"[draw layer]\n" +
	"`[bottom]`\n" +
	"[/movable area]\n" +
	"[/town movable area with animation]\n" +
	"[name]\n" +
	"`西海岸`\n"

// westCoastNoSeriaGatePVFText 是同一份街道去掉赛丽亚房门之后的版本。
const westCoastNoSeriaGatePVFText = "[type]\n`[normal]`\n" +
	"[town movable area]\n" +
	"1013 139 99 50 40 2 1469 464 100 50 40 1\n" +
	"[name]\n" +
	"`西海岸`\n"

// westCoastPostMapPVFText 是西海岸另一块区域（`WestCoast_post.map`）的同形赛丽亚房门，
// 坐标刻意不同，用来证明"优先来源区域自己的地图"。
const westCoastPostMapPVFText = "[town movable area with animation]\n" +
	"[movable area]\n" +
	"[area]\n" +
	"1000 150 32 78\n" +
	"[move town]\n" +
	"38 1\n" +
	"[/movable area]\n" +
	"[/town movable area with animation]\n" +
	"[name]\n" +
	"`西海岸后街`\n"

// elvengardMapPVFText 是艾尔文防线街道（town 38 area 0 `new_Elvengard.map`）的真档结构切片：
// 通往赛丽亚房间 38/1 的门是**六元组** `622 196 0 0 38 1`（同一节里前面那条指向副本门口 38/2）。
const elvengardMapPVFText = "[town movable area]\n" +
	"10 196 90 142 38 2 622 196 0 0 38 1 1363 351 88 19 38 3\n" +
	"[town movable area name]\n" +
	"0 0 0 `` 0 34 0 `赫顿玛尔`\n" +
	"[name]\n" +
	"`艾尔文防线`\n"

// 两种门声明形式都要按**文档序**读出来，且带开门动画那一节里的
// `[animation change area]`（同为 4 个整数）不许被误当成门矩形。
func TestCurrentSeriaRoomDoorTriggersCoverBothPVFSectionFormsInDocumentOrder(t *testing.T) {
	doc, err := dnfpvf.Parse("WestCoast.map", westCoastMapPVFText)
	if err != nil {
		t.Fatalf("parse WestCoast.map: %v", err)
	}
	parsed := worldmap.ParseMap(0, "WestCoast.map", doc)
	got := currentSeriaRoomDoorTriggersInMapOrder(parsed)
	want := []currentSeriaRoomDoorTrigger{
		{X: 1013, Y: 139, Width: 99, Height: 50, TargetTownID: 40, TargetAreaID: 2, Section: "town_movable_area"},
		{X: 1469, Y: 464, Width: 100, Height: 50, TargetTownID: 40, TargetAreaID: 1, Section: "town_movable_area"},
		{X: 10, Y: 50, Width: 90, Height: 440, TargetTownID: 39, TargetAreaID: 0, Section: "town_movable_area"},
		{X: 473, Y: 127, Width: 100, Height: 30, TargetTownID: 38, TargetAreaID: 1, Section: "town_movable_area_with_animation"},
	}
	if len(got) != len(want) {
		t.Fatalf("triggers=%+v want %d entries", got, len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("trigger[%d]=%+v want %+v", index, got[index], want[index])
		}
	}
}

// 来源城镇里的门落点必须跟着 PVF 走（两份坐标不同的 PVF ⇒ 两个不同落点），
// 且来源城镇没有这条门时明确失败、由调用方回退。
func TestSeriaRoomReturnDoorFollowsTheEntryTownPVF(t *testing.T) {
	areas := []dnftown.Area{
		{ID: 0, MapPath: "WestCoast.map"},
		{ID: 1, MapPath: "WestCoast_post.map"},
	}
	readMap := fakePVFMapReader(t, map[string]string{"WestCoast.map": westCoastMapPVFText})
	door, found, reason := resolveSeriaRoomDoorFromAreas(38, 1, 40, 0, areas, readMap)
	if !found || reason != "" {
		t.Fatalf("entry-town door found=%t reason=%q", found, reason)
	}
	if door.TownID != 40 || door.AreaID != 0 || door.PositionX != 523 || door.PositionY != 142 ||
		door.TriggerX != 473 || door.TriggerY != 127 || door.TriggerW != 100 || door.TriggerH != 30 ||
		door.TriggerSection != "town_movable_area_with_animation" {
		t.Fatalf("entry-town door=%+v want 40/0 (523,142) from `[area] 473 127 100 30` + `[move town] 38 1`", door)
	}

	// 第二份 PVF：同一来源城镇、另一组坐标 ⇒ 落点必须跟着变（钉子：不许写死坐标）。
	moved := fakePVFMapReader(t, map[string]string{
		"WestCoast.map": strings.Replace(westCoastMapPVFText, "473 127 100 30", "900 300 10 10", 1),
	})
	other, found, reason := resolveSeriaRoomDoorFromAreas(38, 1, 40, 0, areas, moved)
	if !found || reason != "" {
		t.Fatalf("moved door found=%t reason=%q", found, reason)
	}
	if other.PositionX != 905 || other.PositionY != 305 {
		t.Fatalf("moved door=%+v want (905,305) — the landing must follow the PVF, never a constant", other)
	}

	// 来源城镇里没有任何通往本房间的门 ⇒ 明确失败（调用方据此退回"进门时站的那一点"）。
	none := fakePVFMapReader(t, map[string]string{"WestCoast.map": westCoastNoSeriaGatePVFText})
	if _, found, reason := resolveSeriaRoomDoorFromAreas(38, 1, 40, 0, areas, none); found ||
		reason != "no_town_area_declares_a_door_to_the_seria_room" {
		t.Fatalf("missing entry-town door found=%t reason=%q", found, reason)
	}
}

// 同一来源城镇里多块区域都声明了通往本房间的门时，**来源区域自己那张地图**优先：
// 从西海岸后街（area 1）走门进房，出来要落在后街那扇门，而不是主街那扇。
func TestSeriaRoomReturnDoorPrefersTheEntryAreaItself(t *testing.T) {
	areas := []dnftown.Area{
		{ID: 0, MapPath: "WestCoast.map"},
		{ID: 1, MapPath: "WestCoast_post.map"},
	}
	readMap := fakePVFMapReader(t, map[string]string{
		"WestCoast.map":      westCoastMapPVFText,
		"WestCoast_post.map": westCoastPostMapPVFText,
	})
	fromPost, found, reason := resolveSeriaRoomDoorFromAreas(38, 1, 40, 1, areas, readMap)
	if !found || reason != "" {
		t.Fatalf("area 1 door found=%t reason=%q", found, reason)
	}
	if fromPost.AreaID != 1 || fromPost.PositionX != 1016 || fromPost.PositionY != 189 {
		t.Fatalf("entry area 1 door=%+v want area 1 (1016,189)", fromPost)
	}
	fromMain, found, reason := resolveSeriaRoomDoorFromAreas(38, 1, 40, 0, areas, readMap)
	if !found || reason != "" {
		t.Fatalf("area 0 door found=%t reason=%q", found, reason)
	}
	if fromMain.AreaID != 0 || fromMain.PositionX != 523 || fromMain.PositionY != 142 {
		t.Fatalf("entry area 0 door=%+v want area 0 (523,142)", fromMain)
	}
}

// seriaRoomEntryTownService 把 (服务, 会话, 仓库) 准备好，并让角色站在 entryTown/entryArea。
func seriaRoomEntryTownService(
	t *testing.T,
	entryTown, entryArea, entryPosX, entryPosY int64,
) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	service, session, repositories := newTownMoveTest(t)
	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Stats["town_id"] = entryTown
	character.Stats["area_id"] = entryArea
	character.Stats["pos_x"] = entryPosX
	character.Stats["pos_y"] = entryPosY
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	session.townPositionSnapshot = currentTownPositionSnapshot{
		CharacterID:   29,
		TownID:        byte(entryTown),
		AreaID:        byte(entryArea),
		PositionX:     uint16(entryPosX),
		PositionY:     uint16(entryPosY),
		MovementCode:  5,
		PositionValid: true,
	}
	service.seriaRoomDoorMapReader = fakePVFMapReader(t, map[string]string{
		"WestCoast.map":      westCoastMapPVFText,
		"WestCoast_post.map": westCoastPostMapPVFText,
		"new_Elvengard.map":  elvengardMapPVFText,
	})
	return service, session, repositories
}

// 从**西海岸**走进赛丽亚房间，出门必须回西海岸（操作者报障的那条路径）。
func TestHandlePrevVillageReturnsFromSeriaRoomToTheEntryTown(t *testing.T) {
	service, session, repositories := seriaRoomEntryTownService(t, 40, 0, 1500, 500)
	// 走门进房：op36 目标 = 赛丽亚房间 38/1，来源城镇 = 40（西海岸）。
	if err := service.handleTownSetUserArea(
		session,
		buildTownSeriaRoomPortalRequest(38, 1, 433, 311, 5, 40),
	); err != nil {
		t.Fatal(err)
	}
	entry := session.townSeriaRoomEntrySnapshot
	if entry.CharacterID != 29 || entry.TownID != 40 || entry.AreaID != 0 ||
		entry.PositionX != 1500 || entry.PositionY != 500 || !entry.PositionValid {
		t.Fatalf("seria-room entry origin=%+v want 29 @ 40/0 (1500,500)", entry)
	}
	connection := session.conn.(*bufferConn)
	connection.write.Reset()

	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketPrevVillage),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if packet.Header.MsgID != currentSceneTransitionMsgID || len(rest) != 0 {
		t.Fatalf("prev-village header=%+v trailing=%x", packet.Header, rest)
	}
	wantBody, err := buildCurrentSceneTransitionBody(40, 0, []currentSceneTransitionRow{{
		ObjectOrResourceKey: 29,
		Value1:              523,
		Value2:              142,
		Value3:              5,
		Value4:              3,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packet.Body, wantBody) {
		t.Fatalf("prev-village body=%x want West Coast door 40/0 (523,142) body=%x", packet.Body, wantBody)
	}
	stored, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found || stored.Stats["town_id"] != 40 || stored.Stats["area_id"] != 0 ||
		stored.Stats["pos_x"] != 523 || stored.Stats["pos_y"] != 142 {
		t.Fatalf("stored location found=%t err=%v stats=%+v want 40/0 (523,142)", found, err, stored.Stats)
	}
	// 进门来源出房即消费：下一次进房必须重新绑定，陈旧来源不许复活。
	if session.townSeriaRoomEntrySnapshot.PositionValid {
		t.Fatalf("seria-room entry origin was not consumed: %+v", session.townSeriaRoomEntrySnapshot)
	}
}

// 从**艾尔文防线**走进赛丽亚房间，出门回艾尔文防线（与上一轮口径逐字节一致，防止反向回归）。
func TestHandlePrevVillageReturnsFromSeriaRoomToElvengardWhenEnteredThere(t *testing.T) {
	service, session, repositories := seriaRoomEntryTownService(t, 38, 0, 700, 200)
	if err := service.handleTownSetUserArea(
		session,
		buildTownSeriaRoomPortalRequest(38, 1, 873, 233, 5, 38),
	); err != nil {
		t.Fatal(err)
	}
	entry := session.townSeriaRoomEntrySnapshot
	if entry.CharacterID != 29 || entry.TownID != 38 || entry.AreaID != 0 ||
		entry.PositionX != 700 || entry.PositionY != 200 || !entry.PositionValid {
		t.Fatalf("seria-room entry origin=%+v want 29 @ 38/0 (700,200)", entry)
	}
	connection := session.conn.(*bufferConn)
	connection.write.Reset()

	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketPrevVillage),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	wantBody, err := buildCurrentSceneTransitionBody(38, 0, []currentSceneTransitionRow{{
		ObjectOrResourceKey: 29,
		Value1:              622,
		Value2:              196,
		Value3:              5,
		Value4:              3,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packet.Body, wantBody) {
		t.Fatalf("prev-village body=%x want Elvengard door 38/0 (622,196) body=%x", packet.Body, wantBody)
	}
	stored, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found || stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 0 ||
		stored.Stats["pos_x"] != 622 || stored.Stats["pos_y"] != 196 {
		t.Fatalf("stored location found=%t err=%v stats=%+v want 38/0 (622,196)", found, err, stored.Stats)
	}
}

// 来源城镇里没有"通往本房间"的门 ⇒ 退回玩家进门时站的那一点（仍然是来源城镇，绝不落到
// 房间所挂的那个城镇）。
func TestHandlePrevVillageFallsBackToTheEntryPositionInsideTheEntryTown(t *testing.T) {
	service, session, repositories := seriaRoomEntryTownService(t, 40, 0, 1500, 500)
	// 把西海岸那张地图换成"没有赛丽亚房门"的版本。
	service.seriaRoomDoorMapReader = fakePVFMapReader(t, map[string]string{
		"WestCoast.map":     westCoastNoSeriaGatePVFText,
		"new_Elvengard.map": elvengardMapPVFText,
	})
	if err := service.handleTownSetUserArea(
		session,
		buildTownSeriaRoomPortalRequest(38, 1, 433, 311, 5, 40),
	); err != nil {
		t.Fatal(err)
	}
	connection := session.conn.(*bufferConn)
	connection.write.Reset()

	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketPrevVillage),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	wantBody, err := buildCurrentSceneTransitionBody(40, 0, []currentSceneTransitionRow{{
		ObjectOrResourceKey: 29,
		Value1:              1500,
		Value2:              500,
		Value3:              5,
		Value4:              3,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packet.Body, wantBody) {
		t.Fatalf("prev-village body=%x want entry position 40/0 (1500,500) body=%x", packet.Body, wantBody)
	}
	stored, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found || stored.Stats["town_id"] != 40 || stored.Stats["area_id"] != 0 ||
		stored.Stats["pos_x"] != 1500 || stored.Stats["pos_y"] != 500 {
		t.Fatalf("stored location found=%t err=%v stats=%+v want 40/0 (1500,500)", found, err, stored.Stats)
	}
}

// 一键回退档 `room_town_door`：即便来源城镇已知（西海岸），也回到上一轮口径
// （房间所挂城镇 38 的固定外门口 38/0 (622,196)）。
func TestHandlePrevVillageSeriaExitRoomTownDoorSwitchIgnoresTheEntryTown(t *testing.T) {
	t.Setenv(currentTownSeriaExitDoorEnv, "room_town_door")
	service, session, repositories := seriaRoomEntryTownService(t, 40, 0, 1500, 500)
	if err := service.handleTownSetUserArea(
		session,
		buildTownSeriaRoomPortalRequest(38, 1, 433, 311, 5, 40),
	); err != nil {
		t.Fatal(err)
	}
	connection := session.conn.(*bufferConn)
	connection.write.Reset()

	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketPrevVillage),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	wantBody, err := buildCurrentSceneTransitionBody(38, 0, []currentSceneTransitionRow{{
		ObjectOrResourceKey: 29,
		Value1:              622,
		Value2:              196,
		Value3:              5,
		Value4:              3,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packet.Body, wantBody) {
		t.Fatalf("room_town_door rollback body=%x want room-town door 38/0 (622,196) body=%x", packet.Body, wantBody)
	}
	stored, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found || stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 0 ||
		stored.Stats["pos_x"] != 622 || stored.Stats["pos_y"] != 196 {
		t.Fatalf("room_town_door rollback location found=%t err=%v stats=%+v want 38/0 (622,196)", found, err, stored.Stats)
	}
}

// 真档钉子：运行时 Script.pvf 里
//   - 从西海岸 40/0 走进 38/1 ⇒ 门口 = WestCoast.map 的 `[town movable area with animation]`
//     `[area] 473 127 100 30` + `[move town] 38 1` 中心 (523,142)，城镇/区域必须是 40/0；
//   - 从艾尔文防线 38/0 走进 38/1 ⇒ 门口 = `622 196 0 0 38 1` 中心 (622,196)（上一轮口径一致）；
//   - 来源未知（登录直接落在房间）⇒ 与 `currentSeriaRoomOuterDoorLocation` 逐字段相同。
//
// 运行：DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf 路径> go test ./internal/services/dnfbridge -run RealScriptPVFSeriaRoomReturnDoor
func TestRealScriptPVFSeriaRoomReturnDoorFollowsTheEntryTown(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the real Seria-room return door")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatalf("load real town catalog: %v", err)
	}
	service := &Service{townCatalog: table, options: options{pvfPath: pvfPath}}

	// 从西海岸（town 40 area 0 `WestCoast.map`）进房。
	west, found, reason := service.currentSeriaRoomReturnDoorLocation(38, 1, 40, 0)
	if !found || reason != "" {
		t.Fatalf("real PVF West-Coast entry door found=%t reason=%q", found, reason)
	}
	if west.TownID != 40 || west.AreaID != 0 ||
		!strings.Contains(strings.ToLower(west.MapPath), "westcoast.map") ||
		west.TriggerX != 473 || west.TriggerY != 127 || west.TriggerW != 100 || west.TriggerH != 30 ||
		west.PositionX != 523 || west.PositionY != 142 ||
		west.TriggerSection != "town_movable_area_with_animation" {
		t.Fatalf("real PVF West-Coast entry door=%+v want 40/0 (523,142) from `[area] 473 127 100 30` + `[move town] 38 1`", west)
	}

	// 从艾尔文防线（town 38 area 0 `new_Elvengard.map`）进房：与上一轮口径同一坐标。
	elvengard, found, reason := service.currentSeriaRoomReturnDoorLocation(38, 1, 38, 0)
	if !found || reason != "" {
		t.Fatalf("real PVF Elvengard entry door found=%t reason=%q", found, reason)
	}
	if elvengard.TownID != 38 || elvengard.AreaID != 0 ||
		elvengard.TriggerX != 622 || elvengard.TriggerY != 196 ||
		elvengard.TriggerW != 0 || elvengard.TriggerH != 0 ||
		elvengard.PositionX != 622 || elvengard.PositionY != 196 ||
		elvengard.TriggerSection != "town_movable_area" {
		t.Fatalf("real PVF Elvengard entry door=%+v want 38/0 (622,196) from `622 196 0 0 38 1`", elvengard)
	}

	// 来源未知：必须与上一轮口径逐字段相同（登录直接落在房间那条路径不许变）。
	unknown, found, reason := service.currentSeriaRoomOuterDoorLocation(38, 1)
	if !found || reason != "" {
		t.Fatalf("real PVF outer door found=%t reason=%q", found, reason)
	}
	if unknown != elvengard {
		t.Fatalf("unknown-entry door=%+v want the room-town outer door %+v", unknown, elvengard)
	}

	// 缓存：同一 (房间, 来源) 第二次必须给出同一结果。
	again, found, _ := service.currentSeriaRoomReturnDoorLocation(38, 1, 40, 0)
	if !found || again != west {
		t.Fatalf("cached West-Coast door=%+v found=%t want %+v", again, found, west)
	}
}
