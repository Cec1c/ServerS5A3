package dnfbridge

import (
	"encoding/hex"
	"os"
	"testing"
)

// ── "谁算受邀"：未受邀 458 会崩客户端（2026-09-29 线上实证）──────────────────────
//
// 上一版把 `DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_SOLICIT` 的默认档设成 `state-request`
// ——"客户端发来体长为 0 的 457 就算受邀"，于是线上 `REWARD_ACK=select` 下 3s 兜底自动选奖
// 也会回那条 7 字节 458 选中投影。它在 2026-09-29 05:03:10 +0800 随二进制上线，
// 下一局青龙大会就把客户端打崩了。原始行（`/opt/dnf90/logs/packet_log.txt`，
// `grep -a 'conn_id=game-002721 '`，`t=` 是日志行内时间戳，UTC）：
//
//	RECV kind=game-legacy conn_id=game-002721 pkt_seq=539 cmd=1 type=457
//	     runtime_cmd_name=ENUM_CMDPACKET_TOURNAMENT_REWARD_SELECT_STATE
//	     header_seq=143 body_len=0 len=13 hex=01c9010d000000115a66a78f00
//	     t=2026-09-29T06:05:16.185175316Z                   ← 客户端开口要选奖态
//	SEND kind=game-upper conn_id=game-002721 pkt_seq=540 msg_id=457 ... plain_body_len=9
//	     hex=00c90119000000c33499a3e2000000000101ffffff01ffffff
//	     t=2026-09-29T06:05:16.185223636Z
//	EVENT kind=game-dungeon-tournament-reward-auto-select-fired ... t=2026-09-29T06:05:19.186380153Z
//	SEND ... pkt_seq=545 msg_id=458 ... plain_body_len=7
//	     hex=00ca0117000000e01af484e500000000010200ff02ffff t=2026-09-29T06:05:19.229440582Z
//	SEND ... pkt_seq=548 msg_id=458 ... plain_body_len=7
//	     hex=00ca01170000002fa2929be800000000010200ff0200ff t=2026-09-29T06:05:19.272088076Z
//	RECV kind=game-upper conn_id=game-002721 pkt_seq=551 msg_id=690
//	     runtime_cmd_name=ENUM_CMDPACKET_STATICS_RUNTIME_TING t=2026-09-29T06:05:20.426046166Z
//	EVENT kind=game-read-close conn_id=game-002721 ... quiet=true
//	     error=read tcp 109.244.63.71:10010->39.172.226.229:21945: read: connection reset by peer
//	     t=2026-09-29T06:05:20.494985829Z                   ← 最后一条 458 之后 1.265s 客户端进程死
//
// 对照（这一步是判据的关键）：全日志只有 7 条 `SEND msg_id=458`，分属 4 个会话
// （game-000003 ×1 / game-000034 ×2 / game-000007 ×2 / game-002721 ×2），
// **4/4 在收到 458 后 1~2 秒内 RST**；**没有**收到 458 的 6 个会话
// （game-000006 / game-000016 / game-000009 / game-001151 / game-001204 / game-000012）
// 全部存活 —— game-001151 在 3s 兜底落奖后继续跑 `msg_id=1206` 心跳、`msg_id=123`、
// `RECV msg_id=42 ENUM_CMDPACKET_GIVEUP_GAME` 与 `dungeon-completed-town-route-send`；
// game-001204 落奖后又跑了 6 分钟（`pet-satiety-settled ... source=timequeue_tick
// elapsed_ms=360006`）才 `EOF` 正常关闭；game-000012 落奖后接着打了 dungeon 104。
//
// 所以本文件把**两个开关的零值都钉成安全档**：
//
//	DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_ACK      零值/空/未知 = `select`（只认 solicited）
//	DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_SOLICIT  零值/空/未知 = `legacy`（bodyless 457 不算受邀）
//
// 上一版的危险档位仍然保留，但**必须显式写出字面量**才生效（用于 A/B 复现，不是默认）。

// liveTournamentRewardSelectConfigForTest 复刻线上那一局的**档位组合**：
// `DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_ACK=select`（线上 `/opt/dnf90/dnf90.env` 现取值）。
//
// 夹具里的 Service 是结构体字面量、不走 `optionsFromConfig`，所以必须直接写
// options 字段（与 `setTournamentRewardAckRollbackForTest` 同一做法），
// 同时用 `t.Setenv` 把 env 也对上，保证 `..._SOLICIT` 的真实读 env 路径被覆盖。
func liveTournamentRewardSelectConfigForTest(t *testing.T, service *Service) {
	t.Helper()
	t.Setenv(dungeonTournamentRewardAckEnvKey, dungeonTournamentRewardAckValueSelect)
	if got := readDungeonTournamentRewardAckMode(
		os.Getenv(dungeonTournamentRewardAckEnvKey)); got != dungeonTournamentRewardAckSelect {
		t.Fatalf("live env value normalized to %v want %v", got, dungeonTournamentRewardAckSelect)
	}
	service.options.dungeonTournamentRewardAck = dungeonTournamentRewardAckSelect
	if service.dungeonTournamentRewardAck().answersTimerSelection() {
		t.Fatal("the live `select` tier must NOT answer the timer path by itself (that is the crash being reproduced)")
	}
	if !service.dungeonTournamentRewardAck().answersSolicitedSelection() {
		t.Fatal("the live `select` tier must still answer a real solicitation")
	}
}

// requestTournamentRewardSelectStateForTest 复刻客户端自己发来的那条 bodyless 457，
// 返回后把写入缓冲清空（后续断言只关心 3s 兜底路径发了什么）。
func requestTournamentRewardSelectStateForTest(t *testing.T, service *Service, session *gameSession) {
	t.Helper()
	handled, err := service.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelectState, nil)
	if err != nil || !handled {
		t.Fatalf("state request handled=%t err=%v", handled, err)
	}
	if !session.tournamentRewardStateRequested {
		t.Fatal("the bodyless 457 must set the reward-UI latch")
	}
	session.conn.(*bufferConn).write.Reset()
}

// autoSelectTournamentRewardsForTest 跑一次 3s 兜底自动选奖（= unsolicited 的那条路径）。
func autoSelectTournamentRewardsForTest(t *testing.T, service *Service, session *gameSession, runtime *runtimeDungeonState) {
	t.Helper()
	session.dungeon.mu.Lock()
	service.autoSelectCurrentTournamentRewardsLocked(session, runtime, tournamentRewardSelectionSourceTimer)
	session.dungeon.mu.Unlock()
	if !runtime.tournamentReward.isSelectionComplete() {
		t.Fatalf("timer auto-select = %+v want both groups delivered", runtime.tournamentReward.Selected)
	}
}

// TestTournamentRewardSolicitSwitchDefaultsToLegacy 钉开关解析口径：
// 只有字面量 `state-request` 进入危险档；未设置 / 空 / 大小写 / 未知值都是默认（安全）档。
func TestTournamentRewardSolicitSwitchDefaultsToLegacy(t *testing.T) {
	for _, test := range []struct {
		value string
		want  dungeonTournamentRewardSolicitMode
	}{
		{value: "", want: dungeonTournamentRewardSolicitLegacy},
		{value: "legacy", want: dungeonTournamentRewardSolicitLegacy},
		{value: "LEGACY ", want: dungeonTournamentRewardSolicitLegacy},
		{value: "legacyx", want: dungeonTournamentRewardSolicitLegacy},
		{value: "vendor", want: dungeonTournamentRewardSolicitLegacy},
		{value: "bogus", want: dungeonTournamentRewardSolicitLegacy},
		{value: "state-request", want: dungeonTournamentRewardSolicitStateRequest},
		{value: "STATE-REQUEST", want: dungeonTournamentRewardSolicitStateRequest},
		{value: " state-request ", want: dungeonTournamentRewardSolicitStateRequest},
	} {
		if got := readDungeonTournamentRewardSolicitMode(test.value); got != test.want {
			t.Fatalf("readDungeonTournamentRewardSolicitMode(%q) = %v want %v", test.value, got, test.want)
		}
	}
	if got := dungeonTournamentRewardSolicitStateRequest.String(); got != dungeonTournamentRewardSolicitStateRequestValue {
		t.Fatalf("dangerous tier name = %q want %q", got, dungeonTournamentRewardSolicitStateRequestValue)
	}
	if got := dungeonTournamentRewardSolicitLegacy.String(); got != dungeonTournamentRewardSolicitLegacyValue {
		t.Fatalf("default tier name = %q want %q", got, dungeonTournamentRewardSolicitLegacyValue)
	}
	if !dungeonTournamentRewardSolicitStateRequest.treatsStateRequestAsSolicitation() {
		t.Fatal("the dangerous tier must treat the client's bodyless 457 as a solicitation")
	}
	if dungeonTournamentRewardSolicitLegacy.treatsStateRequestAsSolicitation() {
		t.Fatal("the default tier must not treat the client's bodyless 457 as a solicitation")
	}
	// 线上 env 没有这一行 ⇒ 必须落在默认（安全）档。
	t.Setenv(dungeonTournamentRewardSolicitEnvKey, "")
	if got := dungeonTournamentRewardSolicitModeFromEnv(); got != dungeonTournamentRewardSolicitLegacy {
		t.Fatalf("unset env must be the safe tier, got %v", got)
	}
}

// TestTournamentRewardLiveTierNeverSendsAnUnsolicited458 是本缺陷的**主回归用例**：
//
// 线上那一局的档位组合（`REWARD_ACK=select` + `..._SOLICIT` 未设置）+ 客户端已发来
// bodyless 457（latch）+ 3s 兜底自动选奖 ⇒ **一个字节的 458 都不许发**，
// 下行必须与那 6 个"活下来了"的会话逐字节同形（只有 9 字节 457 选奖态）。
//
// 这条用例在上一版（默认 `state-request`）下是**红**的 —— 那一版会发出
// `010200ff02ffff` 与 `010200ff0200ff`，正是 game-002721 崩掉前的那两条。
func TestTournamentRewardLiveTierNeverSendsAnUnsolicited458(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	liveTournamentRewardSelectConfigForTest(t, service)
	t.Setenv(dungeonTournamentRewardSolicitEnvKey, "")
	if got := dungeonTournamentRewardSolicitModeFromEnv(); got != dungeonTournamentRewardSolicitLegacy {
		t.Fatalf("unset solicit env normalized to %v want %v", got, dungeonTournamentRewardSolicitLegacy)
	}

	requestTournamentRewardSelectStateForTest(t, service, session)
	autoSelectTournamentRewardsForTest(t, service, session, runtime)

	assertTournamentRewardStateOnlyReply(t, session, 1)
}

// TestTournamentRewardDeployDefaultsNeverSendAnUnsolicited458 钉**全新部署（两个开关
// 都没有 env 行）**的默认行为：`REWARD_ACK` 零值 + `REWARD_SOLICIT` 零值 ⇒
// 3s 兜底路径同样一个字节的 458 都不发。这条是"默认 = 安全"的第二半：
// 上一版的 `answersTimerSelection()` 在零值档为真（默认 `select+timer`），
// 就算把 SOLICIT 收回 `legacy`，只靠 ② 也会发 458。
func TestTournamentRewardDeployDefaultsNeverSendAnUnsolicited458(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	t.Setenv(dungeonTournamentRewardAckEnvKey, "")
	t.Setenv(dungeonTournamentRewardSolicitEnvKey, "")
	if got := readDungeonTournamentRewardAckMode(""); got != dungeonTournamentRewardAckSelect {
		t.Fatalf("unset ack env normalized to %v want %v", got, dungeonTournamentRewardAckSelect)
	}
	if service.dungeonTournamentRewardAck() != dungeonTournamentRewardAckSelect {
		t.Fatalf("zero-value fixture ack mode = %v want %v",
			service.dungeonTournamentRewardAck(), dungeonTournamentRewardAckSelect)
	}
	if service.dungeonTournamentRewardAck().answersTimerSelection() {
		t.Fatal("the zero-value ack tier must not answer the 3s timer path")
	}

	requestTournamentRewardSelectStateForTest(t, service, session)
	autoSelectTournamentRewardsForTest(t, service, session, runtime)

	assertTournamentRewardStateOnlyReply(t, session, 1)
}

// TestTournamentRewardSolicitStateRequestOptInReproducesTheCrashingTier 钉**危险档仍然
// 逐字节可复现**：显式 `..._SOLICIT=state-request` + 线上 `REWARD_ACK=select` ⇒
// 发出的握手就是 game-002721 崩掉前的那三条（458 `010200ff02ffff` → 457 → 458
// `010200ff0200ff`）。这条是 A/B 复现档，不是默认行为。
func TestTournamentRewardSolicitStateRequestOptInReproducesTheCrashingTier(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	liveTournamentRewardSelectConfigForTest(t, service)
	t.Setenv(dungeonTournamentRewardSolicitEnvKey, dungeonTournamentRewardSolicitStateRequestValue)
	if got := dungeonTournamentRewardSolicitModeFromEnv(); got != dungeonTournamentRewardSolicitStateRequest {
		t.Fatalf("dangerous env normalized to %v want %v", got, dungeonTournamentRewardSolicitStateRequest)
	}

	requestTournamentRewardSelectStateForTest(t, service, session)
	autoSelectTournamentRewardsForTest(t, service, session, runtime)

	packets := tournamentSelectionHandshakePackets(t, session)
	// 顺序与线上 game-002721 一致：458(type0) → 457 → 458(type1)。
	want := []struct {
		msgID uint16
		hex   string
	}{
		{msgID: a21CmdTournamentRewardSelect, hex: "010200ff02ffff"},
		{msgID: a21CmdTournamentRewardSelectState},
		{msgID: a21CmdTournamentRewardSelect, hex: "010200ff0200ff"},
	}
	if len(packets) != len(want) {
		t.Fatalf("handshake packets = %d want %d: %+v", len(packets), len(want), packets)
	}
	for index, expected := range want {
		if packets[index].msgID != expected.msgID {
			t.Fatalf("handshake[%d] msg_id = %d want %d", index, packets[index].msgID, expected.msgID)
		}
		if expected.hex == "" {
			if len(packets[index].body) != 9 || packets[index].body[0] != 1 {
				t.Fatalf("handshake[%d] state body = %x want the 9-byte success + 2x4 slot projection",
					index, packets[index].body)
			}
			continue
		}
		if got := hex.EncodeToString(packets[index].body); got != expected.hex {
			t.Fatalf("handshake[%d] 458 projection = %s want %s", index, got, expected.hex)
		}
	}
}

// TestTournamentRewardAckSelectTimerOptInStillAnswersTheTimerPath 钉**第二个危险档**：
// 显式 `REWARD_ACK=select+timer`（旧的零值档）仍然让 3s 兜底路径回 458
// —— 它的字节形状与逐字节回退都要留着（2026-09-27 dungeon 123 的 `game-000007`
// 走的就是这一档：`reward-auto-select-fired` 之后 `SEND 458 ... 010200ff02ffff`，
// 随后 RST）。
func TestTournamentRewardAckSelectTimerOptInStillAnswersTheTimerPath(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	t.Setenv(dungeonTournamentRewardAckEnvKey, dungeonTournamentRewardAckValueSelectTimer)
	t.Setenv(dungeonTournamentRewardSolicitEnvKey, "")
	if got := readDungeonTournamentRewardAckMode(dungeonTournamentRewardAckValueSelectTimer); got != dungeonTournamentRewardAckSelectTimer {
		t.Fatalf("select+timer env normalized to %v want %v", got, dungeonTournamentRewardAckSelectTimer)
	}
	service.options.dungeonTournamentRewardAck = dungeonTournamentRewardAckSelectTimer

	requestTournamentRewardSelectStateForTest(t, service, session)
	autoSelectTournamentRewardsForTest(t, service, session, runtime)

	packets := tournamentSelectionHandshakePackets(t, session)
	want := []struct {
		msgID uint16
		hex   string
	}{
		{msgID: a21CmdTournamentRewardSelect, hex: "010200ff02ffff"},
		{msgID: a21CmdTournamentRewardSelectState},
		{msgID: a21CmdTournamentRewardSelect, hex: "010200ff0200ff"},
	}
	if len(packets) != len(want) {
		t.Fatalf("handshake packets = %d want %d: %+v", len(packets), len(want), packets)
	}
	for index, expected := range want {
		if packets[index].msgID != expected.msgID {
			t.Fatalf("handshake[%d] msg_id = %d want %d", index, packets[index].msgID, expected.msgID)
		}
		if expected.hex != "" && hex.EncodeToString(packets[index].body) != expected.hex {
			t.Fatalf("handshake[%d] 458 projection = %s want %s",
				index, hex.EncodeToString(packets[index].body), expected.hex)
		}
	}
}

// TestTournamentRewardSolicitStateRequestStillHonoursTheFullRollback 钉"更深一档"仍在：
// `REWARD_ACK=state` 时，两个危险档都显式打开也仍然**一个字节的 458 都不发**
// （`state` 是一键完全回退，任何新档都不能越过它）。
func TestTournamentRewardSolicitStateRequestStillHonoursTheFullRollback(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	setTournamentRewardAckRollbackForTest(t, service)
	t.Setenv(dungeonTournamentRewardSolicitEnvKey, dungeonTournamentRewardSolicitStateRequestValue)
	if got := dungeonTournamentRewardSolicitModeFromEnv(); got != dungeonTournamentRewardSolicitStateRequest {
		t.Fatalf("dangerous tier expected for the explicit solicit switch, got %v", got)
	}

	requestTournamentRewardSelectStateForTest(t, service, session)
	autoSelectTournamentRewardsForTest(t, service, session, runtime)

	assertTournamentRewardStateOnlyReply(t, session, 1)
}

// TestTournamentRewardSolicitNeverAnswersWithoutTheRewardUILatch 钉"未受邀仍不发"：
// 两个危险档都显式打开，但客户端**没有**发过 bodyless 457（latch 未置位）
// ⇒ 一个字节的 458 都不发。危险档只把"客户端自己开口要过"这一种情形算成受邀，
// 不放宽到"任何 3s 兜底都发"。
func TestTournamentRewardSolicitNeverAnswersWithoutTheRewardUILatch(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	liveTournamentRewardSelectConfigForTest(t, service)
	t.Setenv(dungeonTournamentRewardSolicitEnvKey, dungeonTournamentRewardSolicitStateRequestValue)
	if session.tournamentRewardStateRequested {
		t.Fatal("fixture must start without the reward-UI latch")
	}

	session.conn.(*bufferConn).write.Reset()
	autoSelectTournamentRewardsForTest(t, service, session, runtime)

	assertTournamentRewardStateOnlyReply(t, session, 1)
}
