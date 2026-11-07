package dnfbridge

import (
	"testing"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
)

// TestPVPStandaloneRoomRequestDetectsAreaZero 锁定「独立房间」的判据本身。
//
// PVP 频道 `town/pvp.twn`：`[area] 0` = `Fair_PVP/Gate_Hendon.map`（`[gate]`，
// 每角色一间的独立房间）；`[area] 1` = `Fair_PVP/Hendon.map`（`[normal]`，共享广场）。
// 2026-10-31 玩家实测在独立房间发起决斗仍看不到对方，用户要求直接屏蔽该入口。
func TestPVPStandaloneRoomRequestDetectsAreaZero(t *testing.T) {
	service := pvpRoomTestService(t)
	service.gameSessions = make(map[uint16]*gameSession)

	if !currentPVPStandaloneRoomRequest(newPVPRegressionSession(service, 29, 0)) {
		t.Fatal("area 0 是独立房间，必须判定为要屏蔽")
	}
	if currentPVPStandaloneRoomRequest(newPVPRegressionSession(service, 30, 1)) {
		t.Fatal("area 1 是共享广场，不能误判 —— 会把正常决斗全挡住")
	}

	// 位置快照还没建立时 fail-open：宁可按老行为跑，也不要挡住所有人。
	bare := newPVPRegressionSession(service, 31, 0)
	bare.townPositionSnapshot = currentTownPositionSnapshot{}
	if currentPVPStandaloneRoomRequest(bare) {
		t.Fatal("位置未知时应放行")
	}

	// 非 PVP 频道（组名不是 pvp）不走这条屏蔽。
	ordinary := newPVPRegressionSession(service, 32, 0)
	ordinary.channel.Group = "cain"
	if currentPVPStandaloneRoomRequest(ordinary) {
		t.Fatal("非 PVP 频道不该被判成独立房间")
	}
}

// TestPVPStandaloneRoomRejectsDuelRequests 锁定「独立房间发起的匹配 / 开房一律拒绝」。
//
// 拒绝必须**回一个失败包**：只丢包不回，客户端界面会一直转圈，比报错更难处理。
func TestPVPStandaloneRoomRejectsDuelRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opcode uint16
		body   []byte
	}{
		{"匹配", 520, []byte{1, 0, 0, 0, 0, 255, 255, 255, 255, 255, 255, 255, 255, 0}},
		{"开房", 50, []byte{5, 0, 0, 0, 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := pvpRoomTestService(t)
			service.gameSessions = make(map[uint16]*gameSession)
			session := newPVPRegressionSession(service, 29, 0)
			if !currentPVPStandaloneRoomRequest(session) {
				t.Fatal("夹具不在独立房间")
			}
			if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), tc.opcode, tc.body); err != nil {
				t.Fatalf("请求返回错误：%v", err)
			}
			if session.pvpRoom != nil {
				t.Fatal("独立房间里不该建成房间")
			}
			if len(service.pvpMatching) != 0 {
				t.Fatal("独立房间里不该进入匹配队列")
			}
			packets := drainGameSessionPackets(t, session)
			if len(packets) == 0 {
				t.Fatal("拒绝时必须回失败包，否则客户端界面一直转圈")
			}
			last := packets[len(packets)-1]
			if last.Header.MsgID != tc.opcode || len(last.Body) < 2 || last.Body[0] != 0 {
				t.Fatalf("期望 op%d 失败包（body[0]=0），实际 op%d body=%x",
					tc.opcode, last.Header.MsgID, last.Body)
			}
		})
	}
}
