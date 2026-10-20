package dnfbridge

import (
	"testing"
)

// 现场报障「其他玩家可以进入别人的赛丽亚房间」：赛丽亚的房间在客户端是每个角色一份的
// 私有房间，而服务端原先只用 (TownID, AreaID) 做区域键 —— 第二个角色进去后落进同一个桶，
// 于是互相可见/受影响。这里钉住三件事：
//  1. 同一个赛丽亚房间区域、两个角色各自进（Instance=各自角色）→ 互不可见；
//  2. 同一角色的重复进入（重连/重新过图）仍正常，且能看到自己区域里的其他……只可能没有别人；
//  3. 普通城镇区域（Instance=0）行为完全不变 —— 同屏玩家照旧互相可见。
func TestSeriaRoomAreaIsPerCharacterPrivate(t *testing.T) {
	manager := newOnlinePlayerManager()

	first := manager.EnterArea(&onlinePlayerInfo{CharacterID: 3, TownID: 38, AreaID: 1, Instance: 3})
	if len(first) != 0 {
		t.Fatalf("first entrant must see nobody, got %d", len(first))
	}
	second := manager.EnterArea(&onlinePlayerInfo{CharacterID: 2, TownID: 38, AreaID: 1, Instance: 2})
	if len(second) != 0 {
		t.Fatalf("a second character entered somebody else's private room and saw %d players", len(second))
	}

	// 共享区域枚举里不该出现任何人（私有房间不能通过共享区域查询访问）。
	// 第三个参数是频道：这些夹具没有会话，频道按 0（未绑定）处理。
	if players := manager.GetAreaPlayers(38, 1, 0); len(players) != 0 {
		t.Fatalf("shared-area lookup must not expose private-room occupants, got %d", len(players))
	}

	// 同一角色重复进入仍应在自己的实例里（重连/重过图）。
	if others := manager.EnterArea(&onlinePlayerInfo{CharacterID: 3, TownID: 38, AreaID: 1, Instance: 3}); len(others) != 0 {
		t.Fatalf("re-entering own private room must not see others, got %d", len(others))
	}
}

func TestSharedTownAreaStillBroadcastsBetweenPlayers(t *testing.T) {
	manager := newOnlinePlayerManager()

	if others := manager.EnterArea(&onlinePlayerInfo{CharacterID: 3, TownID: 38, AreaID: 0}); len(others) != 0 {
		t.Fatalf("first town entrant must see nobody, got %d", len(others))
	}
	others := manager.EnterArea(&onlinePlayerInfo{CharacterID: 2, TownID: 38, AreaID: 0})
	if len(others) != 1 || others[0].CharacterID != 3 {
		t.Fatalf("shared town area must keep co-presence, got %+v", others)
	}
	if players := manager.GetAreaPlayers(38, 0, 0); len(players) != 2 {
		t.Fatalf("shared-area lookup = %d players, want 2", len(players))
	}
	// 频道是分桶键的一部分：频道 0（未绑定）以外的任何频道都看不到这两个人。
	if players := manager.GetAreaPlayers(38, 0, 10); len(players) != 0 {
		t.Fatalf("channel-scoped lookup leaked %d players into another channel", len(players))
	}
}

// 私有实例键的判定直接复用工程已有的赛丽亚房间识别，且只对该地图生效。
func TestTownAreaInstanceForMapPathOnlyMarksSeriaRoom(t *testing.T) {
	seriaRoom := "Cataclysm/Town/Elvengard/new_seria_room.map"
	if got := townAreaInstanceForMapPath(seriaRoom, 3); got != 3 {
		t.Fatalf("seria room instance = %d, want the character id 3", got)
	}
	for _, shared := range []string{
		"Cataclysm/Town/Elvengard/new_Elvengard.map",
		"Cataclysm/Town/Elvengard/new_D_Elvengard_L.map",
	} {
		if got := townAreaInstanceForMapPath(shared, 3); got != 0 {
			t.Fatalf("%s must stay a shared town area, got instance %d", shared, got)
		}
	}
	if got := townAreaInstanceForMapPath(seriaRoom, 0); got != 0 {
		t.Fatalf("unknown character must not create a private instance, got %d", got)
	}
}
