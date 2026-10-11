package dnfbridge

import (
	"context"
	"testing"
)

// 现场报障「角色登录后进入赛丽亚房间就卡死」这条入口的回归用例。
//
// 先钉住事实：**每个已完成角色的登录落点本来就是赛丽亚房间**（runtime PVF 的
// town 38 / area 1 = `new_seria_room.map`，gate 450/234），所以"角色持久化位置 = 赛丽亚房间"
// 不是特殊分支，而是唯一分支；它必须能正常走完整条登录入城流程，而该房间在客户端是
// **每个角色一份**的私有房间。本用例断言三件事：
//  1. 落点解析/入城流程不阻塞（不是"位置与地图不匹配"的拒绝路径）；
//  2. 登录路径按 Instance=本人 登记私有房间的存在，共享区域枚举看不到本人；
//  3. 别人进同一 (town,area) 落在自己的实例里，与本人互不可见；共享城镇落点维持
//     既有"过图(op36)才登记"的行为。
func TestCharacterListLoginIntoPrivateSeriaRoomKeepsOwnInstance(t *testing.T) {
	service, session, repositories := newTownMoveTest(t)
	service.onlinePlayers = newOnlinePlayerManager()

	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	// 角色持久化位置就在赛丽亚房间（这正是报障角色的状态）。
	character.Stats["town_id"] = newCharacterInitialTownID
	character.Stats["area_id"] = newCharacterInitialAreaID
	character.Stats["pos_x"] = newCharacterInitialPosX
	character.Stats["pos_y"] = newCharacterInitialPosY
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}

	updated, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
		context.Background(),
		session,
		29,
		character,
	)
	if err != nil {
		t.Fatalf("persisted Seria-room login location must not block the login route: %v", err)
	}
	if townID != newCharacterInitialTownID || areaID != newCharacterInitialAreaID ||
		!townMapPathLooksLikeSeria(mapPath) {
		t.Fatalf("login transition town=%d area=%d map=%q, want the per-character private Seria room", townID, areaID, mapPath)
	}
	if row.Value1 != newCharacterInitialPosX || row.Value2 != newCharacterInitialPosY {
		t.Fatalf("login transition row=%+v, want the runtime PVF gate coordinates", row)
	}

	// 登录路径必须把本人登记进"自己的实例"，而不是留在区域表之外。
	if !service.registerInitialTownPrivateAreaPresence(
		session,
		29,
		updated,
		townID,
		areaID,
		mapPath,
		row,
		"regression_test_login_initial_town_route",
	) {
		t.Fatalf("login into the private Seria room must register presence keyed to the owner character")
	}
	info, ok := service.onlinePlayers.PlayerForCharacter(29)
	if !ok || info.Instance != 29 || info.TownID != townID || info.AreaID != areaID {
		t.Fatalf("registered presence = %+v ok=%t, want instance keyed to character 29 in %d/%d", info, ok, townID, areaID)
	}

	// 共享区域枚举（Instance 固定 0）不能看到私房里的本人——按本会话自己的频道查。
	if players := service.onlinePlayers.GetAreaPlayers(townID, areaID, currentTownPresenceChannelID(session)); len(players) != 0 {
		t.Fatalf("shared-area lookup must not expose the private room, got %d players", len(players))
	}

	// 另一个角色进同一 (town,area)：各自实例，谁也看不到谁。
	peerInstance := townAreaInstanceForMapPath(mapPath, 2)
	if peerInstance == 0 || peerInstance == 29 {
		t.Fatalf("peer instance for the same room = %d, want the peer's own character id", peerInstance)
	}
	if others := service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 2,
		TownID:      townID,
		AreaID:      areaID,
		Instance:    peerInstance,
	}); len(others) != 0 {
		t.Fatalf("a second character must not see the private-room owner, got %d peers", len(others))
	}
	// 本人重登（同角色/新连接重新登记）仍只在自己的实例里。
	if others := service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 29,
		TownID:      townID,
		AreaID:      areaID,
		Instance:    29,
	}); len(others) != 0 {
		t.Fatalf("owner re-login must not see peers, got %d", len(others))
	}
	// 共享城镇落点在这条路径上**不**登记：保持"过图 op36 才登记"的既有线上时序。
	if service.registerInitialTownPrivateAreaPresence(
		session,
		29,
		updated,
		38,
		0,
		"Cataclysm/Town/Elvengard/new_Elvengard.map",
		row,
		"regression_test_login_initial_town_route",
	) {
		t.Fatalf("shared town areas must keep registering on the client op36 transition only")
	}
}
