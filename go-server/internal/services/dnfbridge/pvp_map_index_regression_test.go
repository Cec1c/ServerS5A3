package dnfbridge

import (
	"encoding/binary"
	"testing"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
)

// TestPVPResolveRoomMapIndexNeverLeavesRandomRow 锁定「建房时不能把 0 号随机行写进房间」。
//
// PVF `etc/pvpmapbasicdata.etc` 的 `pvp map order` 里索引 0 固定是 `50000` 随机行。
// 玩家自己选了具体地图时（非 0）必须原样保留，否则会覆盖玩家的选择；
// 选「随机」或自动匹配（都是 0）时必须在建房阶段就抽成真实索引。
func TestPVPResolveRoomMapIndexNeverLeavesRandomRow(t *testing.T) {
	service := pvpChannelInfoTestService(t)
	service.pvpMapOrder = []int64{50000, 50042, 50041}
	for i := 0; i < 64; i++ {
		if got := service.resolveCurrentPVPRoomMapIndexLocked(0); got < 1 || got > 2 {
			t.Fatalf("随机选择解出了 %d：必须落在 1..%d，0 号是随机行、客户端不会预加载地图", got, 2)
		}
	}
	for requested := int16(1); requested <= 2; requested++ {
		if got := service.resolveCurrentPVPRoomMapIndexLocked(requested); got != requested {
			t.Fatalf("玩家选的图 %d 被改成了 %d", requested, got)
		}
	}
}

// TestPVPMatchingRoomInfoCarriesResolvedMapIndex 锁定「op41 与 op45 必须报同一张真实地图」。
//
// 现场（2026-10-31）：自动匹配进决斗房间看不到对方、技能也打不到。
// 旧实现建房时 `request.mapIndex` 恒 0 →
// op41（房间信息）报 0、op45（开始决斗）才随机报真实索引（实测 4 / 3）。
// 客户端拿到 0 不预加载决斗地图，要等 op45 才加载，而场景重建会把
// 之前下发的 actor 全清掉。
//
// 本用例复用 p25 建立的夹具（`newPVPRegressionSession` / `drainGameSessionPackets`），
// 该文件由 p25 创建，故本补丁 ORDER 必须大于 p25。
func TestPVPMatchingRoomInfoCarriesResolvedMapIndex(t *testing.T) {
	service := pvpRoomTestService(t)
	service.questCatalog = buildPVPResultQuestCatalog(t)
	service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	service.pvpMapOrder = []int64{50000, 50042, 50041}
	service.gameSessions = make(map[uint16]*gameSession)

	a := newPVPRegressionSession(service, 29, 1) // 广场：p27 起独立房间禁止发起决斗
	b := newPVPRegressionSession(service, 30, 1)

	request := []byte{1, 0, 0, 0, 0, 255, 255, 255, 255, 255, 255, 255, 255, 0}
	if err := service.handleGameCommand(a, byte(dnfenum.GameCmdCommand), 520, request); err != nil {
		t.Fatalf("首位玩家发起匹配：%v", err)
	}
	drainGameSessionPackets(t, a) // 丢掉搜索 ACK
	if err := service.handleGameCommand(b, byte(dnfenum.GameCmdCommand), 520, request); err != nil {
		t.Fatalf("第二位玩家发起匹配：%v", err)
	}
	if a.pvpRoom == nil || b.pvpRoom != a.pvpRoom {
		t.Fatal("匹配没有建立同一个房间")
	}

	// op41 房间信息体布局：u16(1) + u16 roomID + u8 nameType(=5) + u8 state
	// + u8 manager + u16 mapIndex + u8 battleMode + 座位表。
	const mapIndexOffset = 2 + 2 + 1 + 1 + 1
	announced := -1
	for _, packet := range drainGameSessionPackets(t, a) {
		if packet.Header.MsgID != currentPVPRoomInfoMsgID {
			continue
		}
		if len(packet.Body) < mapIndexOffset+2 {
			t.Fatalf("op41 体长度 %d 不足以读出 mapIndex", len(packet.Body))
		}
		announced = int(binary.LittleEndian.Uint16(packet.Body[mapIndexOffset : mapIndexOffset+2]))
	}
	if announced < 0 {
		t.Fatal("匹配路径没有下发 op41 房间信息")
	}
	if announced == 0 {
		t.Fatal("op41 房间信息报的 mapIndex 是 0（随机行）：客户端不会预加载决斗地图，进房看不到对方")
	}
	if announced >= len(service.pvpMapOrder) {
		t.Fatalf("op41 报的 mapIndex %d 越界（地图表 %d 项）", announced, len(service.pvpMapOrder))
	}

	owner := a
	if err := service.startCurrentPVPRoomLocked(owner.pvpRoom, owner); err != nil {
		t.Fatalf("开始决斗失败：%v", err)
	}
	started := -1
	for _, packet := range drainGameSessionPackets(t, owner) {
		if packet.Header.MsgID == 45 {
			started = int(packet.Body[0])
		}
	}
	if started != announced {
		t.Fatalf("op41 报图 %d、op45 报图 %d：两条包必须一致，否则客户端按 0/错图处理", announced, started)
	}
}
