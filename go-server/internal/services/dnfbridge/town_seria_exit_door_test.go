package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 走出赛丽亚房间的落点 = 运行时 PVF 里该房间的**外门口**（门前触发点中心），
// 而不是登录时绑定的 prev-village 原点。现场与逐字段 PVF 证据见
// town_seria_room_door.go 的文件头。
//
// 本文件钉三件事：
//  1. 落点**随 PVF 变**（同一房间、两份坐标不同的地图 ⇒ 两个不同落点，证明没有写死坐标）；
//  2. 房间里问 op1425 时，op24 回答的外门口落点 + 落库位置都用门口坐标；
//  3. 回退开关 `DNFBRIDGE_TOWN_SERIA_EXIT_DOOR=prev_village` 恢复旧口径（原点 = 持久化位置）。

// fakeDoorMapReader 按地图路径喂 `[town movable area]` 六元组（x y w h 城镇 区域）。
func fakeDoorMapReader(t *testing.T, triggers map[string][][6]int64) func(dnftown.Area) (worldmap.Map, bool) {
	t.Helper()
	return func(area dnftown.Area) (worldmap.Map, bool) {
		rows, ok := triggers[area.MapPath]
		if !ok {
			return worldmap.Map{}, false
		}
		parsed := worldmap.Map{Path: area.MapPath, ID: area.ID}
		for _, row := range rows {
			parsed.TownMovableAreas = append(parsed.TownMovableAreas, worldmap.MovableArea{
				X: row[0], Y: row[1], Width: row[2], Height: row[3],
				Params: [2]int64{row[4], row[5]},
			})
		}
		return parsed, true
	}
}

func TestSeriaRoomOuterDoorFollowsRuntimePVFTriggers(t *testing.T) {
	areas := []dnftown.Area{
		{ID: 0, MapPath: "Cataclysm/Town/Elvengard/new_Elvengard.map", Kind: "normal"},
		{ID: 1, MapPath: "Cataclysm/Town/Elvengard/new_seria_room.map", Kind: "gate"},
	}
	// 第一份地图：通往赛丽亚房间的是 500 200 20 40 (38,1) ⇒ 门口中心 (510,220)；
	// 前面那条指向别的区域（38,2）必须被跳过，后面那条指向别的城镇也必须被跳过。
	first := fakeDoorMapReader(t, map[string][][6]int64{
		areas[0].MapPath: {
			{10, 196, 90, 142, 38, 2},
			{500, 200, 20, 40, 38, 1},
			{433, 311, 0, 0, 39, 0},
		},
	})
	door, found, reason := resolveSeriaRoomOuterDoorFromAreas(38, 1, areas, first)
	if !found || reason != "" {
		t.Fatalf("door found=%t reason=%q", found, reason)
	}
	if door.AreaID != 0 || door.PositionX != 510 || door.PositionY != 220 ||
		door.TriggerX != 500 || door.TriggerY != 200 || door.TriggerW != 20 || door.TriggerH != 40 {
		t.Fatalf("door=%+v want area 0 (510,220) from trigger 500 200 20 40", door)
	}

	// 第二份地图：同一房间、另一组触发点 ⇒ 落点必须跟着变（钉子：不许写死坐标）。
	second := fakeDoorMapReader(t, map[string][][6]int64{
		areas[0].MapPath: {{900, 300, 10, 10, 38, 1}},
	})
	other, found, reason := resolveSeriaRoomOuterDoorFromAreas(38, 1, areas, second)
	if !found || reason != "" {
		t.Fatalf("second door found=%t reason=%q", found, reason)
	}
	if other.PositionX != 905 || other.PositionY != 305 {
		t.Fatalf("second door=%+v want (905,305) — the landing must follow the PVF, never a constant", other)
	}

	// 没有任何区域声明通往该房间的门 ⇒ 明确失败，交给调用方回退旧行为。
	none := fakeDoorMapReader(t, map[string][][6]int64{areas[0].MapPath: {{10, 196, 90, 142, 38, 2}}})
	if _, found, reason := resolveSeriaRoomOuterDoorFromAreas(38, 1, areas, none); found ||
		reason != "no_town_area_declares_a_door_to_the_seria_room" {
		t.Fatalf("missing door found=%t reason=%q", found, reason)
	}
}

// handlePrevVillage 在赛丽亚房间里必须回答"外门口"，并把角色落库到门口。
func TestHandlePrevVillageFromSeriaRoomLandsOnPVFOuterDoor(t *testing.T) {
	service, session, repositories := newTownMoveTest(t)
	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	// 角色就在赛丽亚房间（38/1），持久化 prev-village 是**副本门口**（38/2）——这正是现场。
	character.Stats["town_id"] = newCharacterInitialTownID
	character.Stats["area_id"] = newCharacterInitialAreaID
	character.Stats["pos_x"] = newCharacterInitialPosX
	character.Stats["pos_y"] = newCharacterInitialPosY
	character.Stats["direction"] = newCharacterInitialDirection
	character.Stats[currentTownPrevVillageTownStat] = 38
	character.Stats[currentTownPrevVillageAreaStat] = 2
	character.Stats[currentTownPrevVillagePosXStat] = 1085
	character.Stats[currentTownPrevVillagePosYStat] = 249
	character.Stats[currentTownPrevVillageDirectionStat] = 5
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	service.seriaRoomDoorMapReader = fakeDoorMapReader(t, map[string][][6]int64{
		"new_Elvengard.map": {{622, 196, 0, 0, 38, 1}},
	})

	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketPrevVillage),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	if packet.Header.MsgID != currentSceneTransitionMsgID || len(rest) != 0 {
		t.Fatalf("prev-village header=%+v trailing=%x", packet.Header, rest)
	}
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
		t.Fatalf("prev-village body=%x want exterior door %x (town 38 area 0 at the PVF trigger center)",
			packet.Body, wantBody)
	}
	stored, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found || stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 0 ||
		stored.Stats["pos_x"] != 622 || stored.Stats["pos_y"] != 196 {
		t.Fatalf("stored location found=%t err=%v stats=%+v want 38/0 (622,196)", found, err, stored.Stats)
	}
}

// 一键回退：开关写 prev_village 时逐字节恢复旧口径（沿用登录/进城时绑定的原点）。
func TestHandlePrevVillageSeriaExitDoorSwitchFallsBackToOrigin(t *testing.T) {
	t.Setenv(currentTownSeriaExitDoorEnv, "prev_village")
	service, session, repositories := newTownMoveTest(t)
	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Stats["town_id"] = newCharacterInitialTownID
	character.Stats["area_id"] = newCharacterInitialAreaID
	character.Stats["pos_x"] = newCharacterInitialPosX
	character.Stats["pos_y"] = newCharacterInitialPosY
	character.Stats["direction"] = newCharacterInitialDirection
	character.Stats[currentTownPrevVillageTownStat] = 40
	character.Stats[currentTownPrevVillageAreaStat] = 0
	character.Stats[currentTownPrevVillagePosXStat] = 1500
	character.Stats[currentTownPrevVillagePosYStat] = 500
	character.Stats[currentTownPrevVillageDirectionStat] = 5
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	// 即便门口解析得出来，回退档也一次都不许用。
	service.seriaRoomDoorMapReader = fakeDoorMapReader(t, map[string][][6]int64{
		"new_Elvengard.map": {{622, 196, 0, 0, 38, 1}},
	})

	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketPrevVillage),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
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
		t.Fatalf("rollback body=%x want legacy origin body=%x", packet.Body, wantBody)
	}
}

// 真档钉子：运行时 Script.pvf 的 38/1（`new_seria_room.map`）外门口必须落在
// 38/0 `new_Elvengard.map` 的 `622 196 0 0 38 1` 触发点中心 (622,196)；
// 且它**不是**副本门口（38/2 `new_D_Elvengard_L.map`）。
// 运行：DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf 路径> go test ./internal/services/dnfbridge -run RealScriptPVFSeriaRoomOuterDoor
func TestRealScriptPVFSeriaRoomOuterDoor(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the real Seria-room exterior door")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatalf("load real town catalog: %v", err)
	}
	// Service 必须能自己读到同一份真档（门口判据要读城镇区域的地图文档）。
	service := &Service{townCatalog: table, options: options{pvfPath: pvfPath}}
	door, found, reason := service.resolveCurrentSeriaRoomOuterDoorLocation(
		int64(newCharacterInitialTownID), int64(newCharacterInitialAreaID))
	if !found || reason != "" {
		t.Fatalf("real PVF Seria-room exterior door found=%t reason=%q", found, reason)
	}
	if door.AreaID != 0 || door.MapPath != "Cataclysm/Town/Elvengard/new_Elvengard.map" ||
		door.TriggerX != 622 || door.TriggerY != 196 || door.TriggerW != 0 || door.TriggerH != 0 ||
		door.PositionX != 622 || door.PositionY != 196 {
		t.Fatalf("real PVF Seria-room exterior door=%+v want town 38 area 0 (622,196) from `622 196 0 0 38 1`", door)
	}
	// 副本门口（38/2）的 `[dungeon gate] 2` 是另一回事：门口不能等于副本门口。
	dungeonGate, ok := table.FindArea(int64(newCharacterInitialTownID), 2)
	if !ok || dungeonGate.Kind != "dungeon_gate" {
		t.Fatalf("real PVF town 38 area 2=%+v found=%t want the dungeon gate area", dungeonGate, ok)
	}
	if door.AreaID == 2 {
		t.Fatalf("Seria-room exterior door resolved to the dungeon gate area: %+v", door)
	}
	// 缓存：第二次调用必须给出同一结果（同一 (城镇,区域) 只解析一次）。
	again, found, _ := service.currentSeriaRoomOuterDoorLocation(
		int64(newCharacterInitialTownID), int64(newCharacterInitialAreaID))
	if !found || again != door {
		t.Fatalf("cached door=%+v found=%t want %+v", again, found, door)
	}
}
