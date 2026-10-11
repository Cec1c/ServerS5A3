package dnfbridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 现场缺口（2026-09-27 双客户端自动化 p2）：
//
//	三级落点的第二级"沿用持久化落点"（DNFBRIDGE_LOGIN_PERSISTED_TOWN=keep）把角色
//	**直接放进共享城镇区域**（实测 town 39 / area 0 / new_Hendon_Main.map，客户端认为
//	自己已经就位、不会再发 op36），而登录路径当时只在落点是"每角色一份"的私房时登记
//	在线存在感 ⇒ 服务端区域表里永远没有这个人 ⇒ `PeerInSameArea` 恒假 ⇒
//	邀请/查看信息/选择器 fan-out/整队进副本全部被静默吞掉
//	（客户端表现正是操作者报的"组不上队 / 互相看不到 / 点了没反应"）。
//
// 本用例钉住三件事：
//  1. 私房落点不由这条路由负责（仍归 registerInitialTownPrivateAreaPresence）；
//  2. 开关 off 时逐字节回到改动前行为（不登记）；
//  3. 默认档下共享落点必须登记进**共享桶**（Instance=0），同区另一名角色能看到他，
//     并且 packet_log 里留下带本路由 source 的 `game-town-presence-published`。
func TestInitialTownSharedAreaLandingPublishesPresence(t *testing.T) {
	fixture := newExpertJobStoreProjectionFixture(t)
	logPath := filepath.Join(t.TempDir(), "packet_log.txt")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.packetLog = logger

	fixture.peer.selectedCharacterID = 17
	character := dnfrepo.CharacterRecord{
		CharacterID: "17",
		AccountID:   "dnf:2",
		Name:        "peer",
		Job:         "1",
		Level:       2,
		Stats: map[string]int64{
			"town_id": 39, "area_id": 0, "pos_x": 600, "pos_y": 250,
			"direction": 5, "area_state": 3,
		},
	}
	row := currentSceneTransitionRow{Value1: 600, Value2: 250, Value3: 5, Value4: 3}
	const sharedMapPath = "Cataclysm/Town/HendonMyre/new_Hendon_Main.map"
	const privateMapPath = "Cataclysm/Town/Elvengard/new_seria_room.map"

	// 1) 私房落点：本条路由不接管。
	if fixture.service.registerInitialTownSharedAreaPresence(
		fixture.peer, 17, character, 38, 1, privateMapPath, row, initialTownSharedAreaPresenceSource,
	) {
		t.Fatal("private Seria-room landings stay owned by registerInitialTownPrivateAreaPresence")
	}

	// 2) 开关 off：回到改动前行为（共享落点不登记，等客户端 op36）。
	t.Setenv(initialTownSharedAreaPresenceEnv, "off")
	if fixture.service.registerInitialTownSharedAreaPresence(
		fixture.peer, 17, character, 39, 0, sharedMapPath, row, initialTownSharedAreaPresenceSource,
	) {
		t.Fatal("env off must keep the pre-fix behavior for shared-area landings")
	}
	if info, ok := fixture.service.onlinePlayers.PlayerForCharacter(17); ok {
		t.Fatalf("env off must not register presence, got %+v", info)
	}

	// 3) 默认档：共享落点必须登记进共享桶，且同区玩家互相可见。
	t.Setenv(initialTownSharedAreaPresenceEnv, "on")
	if !fixture.service.registerInitialTownSharedAreaPresence(
		fixture.peer, 17, character, 39, 0, sharedMapPath, row, initialTownSharedAreaPresenceSource,
	) {
		t.Fatal("a shared-area login landing must publish presence")
	}
	info, ok := fixture.service.onlinePlayers.PlayerForCharacter(17)
	if !ok || info.TownID != 39 || info.AreaID != 0 || info.Instance != 0 {
		t.Fatalf("registered presence = %+v ok=%t, want town 39 / area 0 / shared instance 0", info, ok)
	}
	if info.PositionX != 600 || info.PositionY != 250 || info.Direction != 5 || info.AreaState != 3 {
		t.Fatalf("registered presence position = (%d,%d) dir=%d state=%d, want the login landing (600,250) 5/3",
			info.PositionX, info.PositionY, info.Direction, info.AreaState)
	}
	players := fixture.service.onlinePlayers.GetAreaPlayers(39, 0, currentTownPresenceChannelID(fixture.peer))
	if len(players) != 1 || players[0].CharacterID != 17 {
		t.Fatalf("shared-area lookup = %+v, want exactly character 17", players)
	}
	others := fixture.service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 29,
		AccountID:   "dnf:1",
		Name:        "owner",
		TownID:      39,
		AreaID:      0,
		Instance:    0,
		Session:     fixture.owner,
	})
	coPresent := false
	for _, player := range others {
		if player.CharacterID == 17 {
			coPresent = true
		}
	}
	if !coPresent {
		t.Fatalf("a co-present character must see 17 (PeerInSameArea gate), got %+v", others)
	}

	// 先把日志落盘再断言（断言的 t.Fatal 会跳过后续语句，不能把 Close 留到断言之后）。
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "game-town-presence-published") ||
		!strings.Contains(text, initialTownSharedAreaPresenceSource) {
		t.Fatalf("packet log must carry game-town-presence-published source=%s", initialTownSharedAreaPresenceSource)
	}
}
