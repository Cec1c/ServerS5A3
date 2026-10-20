package dnfbridge

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// 现场（2026-09-25 香港）：操作者报「副职业分解机无法放置」。op598
// CREATE_EXPERT_JOB_STORE(Kind=0=DisjointMachine) 在日志里出现 5 次，4 次回 `01`，
// 1 次回 `00 13`（错误码 19）：
//
//	packet_log.20260925-165109.txt:10469  conn=game-000003 char=9
//	  body=00 01000000 01 01000000 8b02 c500 ffff
//	  → Kind=0 / nameLen=1 / cost=1 / pos=(651,197) / dir=-1 ；回包 body=00 13
//
// 错误码 19 有 6 种成因（在副本内 / 组队中 / 城镇注册缺失 / 职业不符 / 配置缺失 /
// 机器已存在），当时服务端一个诊断字节都没写，所以无法归因。下面两条钉住：
//  1. 拒绝时**必须**留下 `game-expert-job-store-create-blocked` + reason 一行；
//  2. 回包体仍然是现场那两字节 `00 13`，判定与码值都没变。
func TestCreateExpertJobStoreRejectionLeavesAttributableReasonLine(t *testing.T) {
	service, session, _ := newTownMoveTest(t)
	logPath := filepath.Join(t.TempDir(), "packet_log.txt")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service.packetLog = logger
	// 现场那次放置是"人在城镇里"的正常会话状态；把在线玩家条目补齐，
	// 让判定走到专家职业目录这一层（本次测试没有目录 ⇒ catalog_unavailable）。
	service.onlinePlayers = newOnlinePlayerManager()
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 29, Name: "t", TownID: 38, AreaID: 1, ChannelID: int(session.channel.ID),
		PositionX: 450, PositionY: 234, Direction: 5, AreaState: 3, Session: session,
	})

	// 现场同形状的 op598 请求体：Kind=0(DisjointMachine) / nameLen=1 / name=0x01 /
	// cost=1 / pos=(651,197) / dir=-1。
	body := []byte{0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x01, 0x00, 0x00, 0x00, 0x8b, 0x02, 0xc5, 0x00, 0xff, 0xff}
	if _, err := parseCurrentExpertJobStoreCreateRequest(body); err != nil {
		t.Fatalf("live op598 body must parse: %v", err)
	}
	if err := service.handleCurrentExpertJobStoreCreate(session, uint16(dnfenum.CmdPacketCreateExpertJobStore), body); err != nil {
		t.Fatal(err)
	}
	if err := service.handleCurrentExpertJobStoreCreate(session, uint16(dnfenum.CmdPacketCreateExpertJobStore), []byte{0x00, 0x01}); err != nil {
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

	// 回包形状不变：现场失败体就是 {0, 19}。
	failure, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	if failure.Header.MsgID != uint16(dnfenum.CmdPacketCreateExpertJobStore) ||
		!bytes.Equal(failure.Body, []byte{0, 19}) {
		t.Fatalf("op598 failure body=%x want=0013", failure.Body)
	}
	if _, next := splitGameServerUpperPacket(t, rest); next == nil {
		t.Fatal("second op598 rejection produced no packet")
	}

	for _, want := range []string{
		"kind=game-expert-job-store-create-blocked",
		"reason=expert_job_catalog_unavailable",
		"reason=store_create_request_body_invalid",
		"position_x=651",
		"position_y=197",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rejection log is missing %q\n--- log ---\n%s", want, text)
		}
	}
}
