package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件钉死 A1（op34 结算 ctx 超时/取消也必须回包）与 A2-3（op34 三段计时）的
// 可证伪行为。证据链见 `_dist/quest-finish-latency-diagnosis.md` §2.2 / §6 A1/A2。
//
// A1 的失败包形状**不是新发明的**：它就是 `quest_finish.go` 里业务失败分支一直在
// 线上用的那条（msg 34 / class 1 / 体 `00 16`）。下面的
// `finishBridgeBusinessFailureFrame` 就是"改动前就已经在线的业务失败回包"，本文件
// 用它逐字节对比修复档的回包 ⇒ 修复没有引入任何新的帧形状。

// finishBridgeFailureReplyFrameHex 是修复档失败回包的**完整 15 字节线上帧**：
// class=01 msg=0022(34) len=0f000000 checksum=56b961c2 seq=0000 body=0016。
// checksum 覆盖 seq+body（`00 00 00 16`），校验算法 = protocol/checksum.go 的
// 反射表 CRC-32（poly 0x4DB89129，init/final 取反）。这一行是硬钉：任何帧形状、
// 字段顺序或校验算法变化都会在这里失败。
const finishBridgeFailureReplyFrameHex = "0122000f00000056b961c200000016"

func finishBridgeRequest() dnfquest.FinishQuestRequest {
	return dnfquest.FinishQuestRequest{
		QuestID:           3145,
		RewardSelectIndex: ^uint16(0),
		Multiplier:        1,
		Reserved:          dnfquest.CurrentFinishQuestObservedTailMarker,
	}
}

// finishBridgeValidRequestBody 是 op34 的 10 字节合法请求体（任务 3145 = 0x0C49）。
func finishBridgeValidRequestBody() []byte {
	return []byte{0x22, 0x00, 0x49, 0x0C, 0xFF, 0xFF, 0x01, 0x00, 0xFF, 0xFF}
}

// finishBridgeDeadlineSettlementError 复刻线上那两行错误（errors.Join）：
//
//	context deadline exceeded
//	sql: transaction has already been committed or rolled back
func finishBridgeDeadlineSettlementError() error {
	return errors.Join(context.DeadlineExceeded, errors.New("sql: transaction has already been committed or rolled back"))
}

type finishBridgeFailureRun struct {
	raw     []byte
	packets []dnfproto.ChannelPacket
	session *gameSession
	logText string
}

// runFinishBridgeSettlementFailure 跑一次"结算失败"的完成任务，并同时收集
// 回包字节与包日志文本（包日志用来钉住可 grep 的线上证据行）。
func runFinishBridgeSettlementFailure(t *testing.T, settlementErr error) finishBridgeFailureRun {
	t.Helper()
	repositories := finishBridgeSeed(t)
	repositories.CharacterSettlement = finishBridgeFailingSettlement{err: settlementErr}
	logPath := filepath.Join(t.TempDir(), "finish-failure-reply.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options:   options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		packetLog: logger,
	}
	session := &gameSession{conn: connection, connID: "finish-quest-context-failure", selectedCharacterID: 19}
	if err := service.handleCurrentFinishQuestWithResources(
		session,
		finishBridgeRequest(),
		repositories,
		finishBridgeCatalog(t),
		finishBridgeProgression(t),
		finishBridgeAllocator(nil),
	); err != nil {
		t.Fatalf("finish handler returned error: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), connection.write.Bytes()...)
	return finishBridgeFailureRun{
		raw:     raw,
		packets: splitAllFinishBridgePackets(t, raw),
		session: session,
		logText: string(logged),
	}
}

// finishBridgeBusinessFailureFrame 采集"改动前就已经在线"的业务失败回包，作为
// 修复档回包的逐字节参照（非业务失败 + 合法回执形状的 op34 走到
// `currentFinishQuestBusinessFailure` 分支）。
func finishBridgeBusinessFailureFrame(t *testing.T) []byte {
	t.Helper()
	repositories := finishBridgeSeed(t)
	connection := &bufferConn{}
	service := &Service{
		options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: connection, connID: "finish-quest-business-failure-reference", selectedCharacterID: 19}
	// 末两字节不是已观测哨兵 FF FF ⇒ 走 handleCurrentFinishQuest 语义校验 →
	// sendGameUpperFailure(34, 22)（业务失败分支，改动前就在线上）。
	if err := service.handleCurrentFinishQuest(
		session,
		[]byte{0x22, 0x00, 0x49, 0x0C, 0xFF, 0xFF, 0x01, 0x00, 0xFF, 0x00},
	); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() == 0 {
		t.Fatal("business-failure reference path wrote no packet")
	}
	return append([]byte(nil), connection.write.Bytes()...)
}

// 修复档（默认 = 未设置/空/未知值）下，结算 ctx 超时必须回**恰好一个**失败包，
// 且字节与已在线的业务失败回包完全一致。
func TestCurrentFinishQuestSettlementDeadlineAnswersClient(t *testing.T) {
	for _, raw := range []string{"", "on", "banana"} {
		t.Run("switch="+raw, func(t *testing.T) {
			t.Setenv(currentQuestFinishFailureReplyEnv, raw)
			run := runFinishBridgeSettlementFailure(t, finishBridgeDeadlineSettlementError())

			if got := hex.EncodeToString(run.raw); got != finishBridgeFailureReplyFrameHex {
				t.Fatalf("context-deadline failure frame=%s want=%s", got, finishBridgeFailureReplyFrameHex)
			}
			if len(run.packets) != 1 {
				t.Fatalf("context-deadline failure emitted %d packet(s) want 1: %s", len(run.packets), hex.EncodeToString(run.raw))
			}
			packet := run.packets[0]
			if packet.Header.Classification != dnfproto.DefaultChannelClassification ||
				packet.Header.MsgID != uint16(dnfenum.CmdPacketFinishQuest) ||
				!bytes.Equal(packet.Body, []byte{0, 22}) {
				t.Fatalf("context-deadline failure packet=%+v body=%x", packet.Header, packet.Body)
			}
			// 与"改动前就在线的业务失败回包"逐字节相同 ⇒ 没有新帧形状。
			if reference := finishBridgeBusinessFailureFrame(t); !bytes.Equal(run.raw, reference) {
				t.Fatalf("fix reply frame=%x differs from the proven business-failure frame=%x",
					run.raw, reference)
			}
			// 可 grep 的线上证据行。
			for _, want := range []string{
				"EVENT kind=game-upper-finish-quest-blocked",
				"reason=pvf_db_atomic_finish_rejected",
				"EVENT kind=game-upper-finish-quest-failure-reply",
				"quest_id=3145",
				"stage=settlement",
				"msg_id=34",
				"classification=1",
				"failure_code=22",
				"reason=settlement_context_deadline_exceeded",
			} {
				if !strings.Contains(run.logText, want) {
					t.Fatalf("packet log missing %q:\n%s", want, run.logText)
				}
			}
			// 失败不置位回执 ⇒ 客户端重试仍然可用（不会触发 2026-07-25 那次
			// "重复回执 NULL 解引用崩客户端"）。
			if run.session.currentFinishQuestAnswered(newCurrentFinishQuestReplayKey(19, finishBridgeRequest())) {
				t.Fatal("failure reply marked the quest as answered; retry would be suppressed")
			}
		})
	}
}

// 回退档：字面量 legacy（含大小写/空白）逐字节回到 2026-09-27 的静默行为。
func TestCurrentFinishQuestSettlementDeadlineLegacyStaysSilent(t *testing.T) {
	for _, raw := range []string{"legacy", " Legacy ", "LEGACY"} {
		t.Run("switch="+raw, func(t *testing.T) {
			t.Setenv(currentQuestFinishFailureReplyEnv, raw)
			run := runFinishBridgeSettlementFailure(t, finishBridgeDeadlineSettlementError())
			if len(run.raw) != 0 {
				t.Fatalf("legacy tier emitted %d byte(s)=%x want 0", len(run.raw), run.raw)
			}
			// 静默的是**回包**，不是证据：旧日志行必须原样保留。
			if !strings.Contains(run.logText, "reason=pvf_db_atomic_finish_rejected") {
				t.Fatalf("legacy tier lost the blocked evidence line:\n%s", run.logText)
			}
			if strings.Contains(run.logText, "game-upper-finish-quest-failure-reply") {
				t.Fatalf("legacy tier logged a failure reply it did not send:\n%s", run.logText)
			}
		})
	}
}

// ctx 取消与 ctx 超时同等对待（同一个 createWriteTimeout ctx 的两种终态）。
func TestCurrentFinishQuestSettlementCancelAnswersClient(t *testing.T) {
	t.Setenv(currentQuestFinishFailureReplyEnv, "")
	run := runFinishBridgeSettlementFailure(t, errors.Join(context.Canceled, errors.New("sql: transaction has already been committed or rolled back")))
	if got := hex.EncodeToString(run.raw); got != finishBridgeFailureReplyFrameHex {
		t.Fatalf("context-canceled failure frame=%s want=%s", got, finishBridgeFailureReplyFrameHex)
	}
	if !strings.Contains(run.logText, "reason=settlement_context_canceled") {
		t.Fatalf("packet log missing the cancel reason:\n%s", run.logText)
	}
}

// 反向约束：修复档只覆盖"上下文失败"。其它非业务失败（今天就是静默）必须继续静默，
// 否则会把"未知故障"伪装成"业务失败"。
func TestCurrentFinishQuestNonContextFailureStaysSilent(t *testing.T) {
	t.Setenv(currentQuestFinishFailureReplyEnv, "")
	run := runFinishBridgeSettlementFailure(t, errors.New("settlement commit rejected"))
	if len(run.raw) != 0 {
		t.Fatalf("non-context failure emitted %d byte(s)=%x want 0", len(run.raw), run.raw)
	}
	if strings.Contains(run.logText, "game-upper-finish-quest-failure-reply") {
		t.Fatalf("non-context failure logged a failure reply:\n%s", run.logText)
	}
}

func TestCurrentFinishQuestContextFailureClassifier(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"deadline", context.DeadlineExceeded, "settlement_context_deadline_exceeded"},
		{"joined deadline", finishBridgeDeadlineSettlementError(), "settlement_context_deadline_exceeded"},
		{"wrapped cancel", errors.Join(errors.New("tx"), context.Canceled), "settlement_context_canceled"},
		{"deadline wins over cancel", errors.Join(context.Canceled, context.DeadlineExceeded), "settlement_context_deadline_exceeded"},
		{"business error", errors.New("settlement commit rejected"), ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := currentFinishQuestContextFailureReason(testCase.err); got != testCase.want {
				t.Fatalf("reason=%q want=%q", got, testCase.want)
			}
		})
	}
}

func TestCurrentQuestFinishFailureReplySwitchPolicy(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", true}, {"on", true}, {"0", true}, {"off", true}, {"false", true},
		{"banana", true}, {" legacy ", false}, {"legacy", false}, {"LEGACY", false},
	}
	for _, testCase := range cases {
		t.Setenv(currentQuestFinishFailureReplyEnv, testCase.raw)
		if got := currentQuestFinishFailureReplyEnabled(); got != testCase.want {
			t.Fatalf("switch=%q enabled=%t want=%t", testCase.raw, got, testCase.want)
		}
	}
}

func TestCurrentQuestFinishTimingSwitchPolicy(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", false}, {"off", false}, {"legacy", false}, {"banana", false},
		{"on", true}, {"ON", true}, {"1", true}, {"true", true},
	}
	for _, testCase := range cases {
		t.Setenv(currentQuestFinishTimingEnv, testCase.raw)
		if got := currentQuestFinishTimingEnabled(); got != testCase.want {
			t.Fatalf("switch=%q enabled=%t want=%t", testCase.raw, got, testCase.want)
		}
	}
}

// A2-3 默认档（off）不得给包日志增加任何一行：加载失败路径的日志与改动前一致。
func TestCurrentQuestFinishTimingDefaultOffAddsNoLogLine(t *testing.T) {
	t.Setenv(currentQuestFinishTimingEnv, "")
	logText, packets := runFinishBridgeCatalogLoadFailure(t)
	if !strings.Contains(logText, "reason=quest_catalog_unavailable") {
		t.Fatalf("expected the unchanged blocked line:\n%s", logText)
	}
	if strings.Contains(logText, "game-upper-finish-quest-timing") {
		t.Fatalf("default-off timing switch still wrote a timing line:\n%s", logText)
	}
	if len(packets) != 0 {
		t.Fatalf("catalog-load failure answered with %d packet(s)", len(packets))
	}
}

// A2-3 打开后每个 op34 请求恰好一行，且三段字段齐全。
func TestCurrentQuestFinishTimingOnEmitsOneLinePerAttempt(t *testing.T) {
	t.Setenv(currentQuestFinishTimingEnv, "on")
	logText, _ := runFinishBridgeCatalogLoadFailure(t)
	if got := strings.Count(logText, "game-upper-finish-quest-timing"); got != 1 {
		t.Fatalf("timing line count=%d want 1:\n%s", got, logText)
	}
	for _, want := range []string{
		"EVENT kind=game-upper-finish-quest-timing",
		"quest_id=3145",
		"load_ms=",
		"tx_ms=",
		"ack_ms=",
		"total_ms=",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("timing log missing %q:\n%s", want, logText)
		}
	}
	// 加载阶段就失败 ⇒ 结算与 ACK 两段必须为 0（可证伪：任何非零都意味着打点错位）。
	if got := finishBridgeTimingMillis(t, logText, "tx_ms"); got != 0 {
		t.Fatalf("tx_ms=%d want 0 on the catalog-load failure path:\n%s", got, logText)
	}
	if got := finishBridgeTimingMillis(t, logText, "ack_ms"); got != 0 {
		t.Fatalf("ack_ms=%d want 0 on the catalog-load failure path:\n%s", got, logText)
	}
}

// A2-3 的 tx_ms 必须真的量到结算事务：给结算套 25ms 延迟后 tx_ms 必须 ≥ 25。
func TestCurrentQuestFinishTimingMeasuresSettlementStage(t *testing.T) {
	t.Setenv(currentQuestFinishTimingEnv, "on")
	const settlementDelay = 25 * time.Millisecond

	repositories := finishBridgeSeed(t)
	repositories.CharacterSettlement = finishBridgeDelayedSettlement{
		inner: repositories.CharacterSettlement,
		delay: settlementDelay,
	}
	logPath := filepath.Join(t.TempDir(), "finish-timing.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options:   options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		packetLog: logger,
	}
	session := &gameSession{conn: connection, connID: "finish-quest-timing", selectedCharacterID: 19}

	// 复刻 handleCurrentFinishQuest 的边界顺序：开始计时 → 资源就绪（本次直接注入）
	// → 结算+ACK → 写唯一那行。
	timing := startCurrentQuestFinishTiming()
	if timing == nil {
		t.Fatal("timing switch is on but startCurrentQuestFinishTiming returned nil")
	}
	timing.markQuest(finishBridgeRequest().QuestID)
	timing.markLoadDone()
	if err := service.handleCurrentFinishQuestTimed(
		session,
		finishBridgeRequest(),
		repositories,
		finishBridgeCatalog(t),
		finishBridgeProgression(t),
		finishBridgeAllocator(nil),
		timing,
	); err != nil {
		t.Fatalf("finish handler returned error: %v", err)
	}
	timing.log(service, session)
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logged)
	if got := strings.Count(logText, "game-upper-finish-quest-timing"); got != 1 {
		t.Fatalf("timing line count=%d want 1:\n%s", got, logText)
	}
	txMillis := finishBridgeTimingMillis(t, logText, "tx_ms")
	if txMillis < settlementDelay.Milliseconds() {
		t.Fatalf("tx_ms=%d want >= %d (settlement stage was not measured):\n%s",
			txMillis, settlementDelay.Milliseconds(), logText)
	}
	if loadMillis := finishBridgeTimingMillis(t, logText, "load_ms"); loadMillis >= txMillis {
		t.Fatalf("load_ms=%d >= tx_ms=%d: the load/tx boundary is misplaced:\n%s", loadMillis, txMillis, logText)
	}
	// 成功路径必须有且只有一条 ACK。
	if packets := splitAllFinishBridgePackets(t, connection.write.Bytes()); len(packets) == 0 ||
		packets[0].Header.MsgID != uint16(dnfenum.CmdPacketFinishQuest) {
		t.Fatalf("successful timing run packets=%v", finishBridgePacketIDs(packets))
	}
}

// op34 的三条"加载阶段"ctx 失败路径必须与结算路径同样"永不静默"：
// 默认档各回**恰好一个**失败包（帧与结算路径逐字节相同），`legacy` 档各零字节
// （逐字节回到 2026-09-27 的静默）。逐路径一个用例，stage 必须能区分是哪一段。
func TestCurrentFinishQuestLoadStageContextFailuresAnswerClient(t *testing.T) {
	loadErr := finishBridgeDeadlineSettlementError()

	cases := []struct {
		name    string
		stage   string
		questID uint16
		blocked string
		prepare func(t *testing.T, service *Service)
	}{
		{
			name:    "quest_catalog_load",
			stage:   "quest_catalog_load",
			questID: 3145,
			blocked: "reason=quest_catalog_unavailable",
			prepare: func(_ *testing.T, service *Service) {
				service.questCatalogLoadErr = loadErr
				// The stale context cache must now be cleared. Keep the current
				// catalog reload failing at its archive entry point so this still
				// tests the actual context-failure reply, not a missing fixture PVF.
				service.initialEquipmentLoadErr = loadErr
			},
		},
		{
			name:    "progression_or_item_pvf_load",
			stage:   "progression_or_item_pvf_load",
			questID: 3145,
			blocked: "reason=progression_or_item_pvf_unavailable",
			prepare: func(t *testing.T, service *Service) {
				service.questCatalog = finishBridgeCatalog(t)
				service.questFinishResourceLoader = func(context.Context) (*progression.Tables, *pvfDungeonDropCatalog, error) {
					return nil, nil, loadErr
				}
			},
		},
		{
			name:    "profession_pvf_load",
			stage:   "profession_pvf_load",
			questID: 100,
			blocked: "reason=profession_pvf_resources_unavailable",
			prepare: func(t *testing.T, service *Service) {
				service.questCatalog = finishBridgeProfessionCatalog(t)
				// 让前一条加载"成功"，才能走到专家职业资源这条分支（这正是该注入点
				// 存在的唯一理由）；专家职业加载自身的第一跳（PVF 归档）失败。
				service.questFinishResourceLoader = func(context.Context) (*progression.Tables, *pvfDungeonDropCatalog, error) {
					return finishBridgeProgression(t), nil, nil
				}
				service.initialEquipmentLoadErr = loadErr
			},
		},
	}

	run := func(t *testing.T, testCase struct {
		name    string
		stage   string
		questID uint16
		blocked string
		prepare func(t *testing.T, service *Service)
	}, switchValue string) finishBridgeFailureRun {
		t.Helper()
		t.Setenv(currentQuestFinishFailureReplyEnv, switchValue)
		repositories := finishBridgeSeed(t)
		logPath := filepath.Join(t.TempDir(), "finish-load-failure-"+testCase.name+".log")
		logger, err := openPacketLogger(logPath)
		if err != nil {
			t.Fatal(err)
		}
		connection := &bufferConn{}
		service := &Service{
			options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
			packetLog:          logger,
			repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		}
		testCase.prepare(t, service)
		session := &gameSession{conn: connection, connID: "finish-quest-load-failure-" + testCase.name, selectedCharacterID: 19}
		if err := service.handleCurrentFinishQuest(session, finishBridgeRequestBody(testCase.questID)); err != nil {
			t.Fatalf("finish handler returned error: %v", err)
		}
		if err := logger.Close(); err != nil {
			t.Fatal(err)
		}
		logged, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		raw := append([]byte(nil), connection.write.Bytes()...)
		return finishBridgeFailureRun{
			raw:     raw,
			packets: splitAllFinishBridgePackets(t, raw),
			session: session,
			logText: string(logged),
		}
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Run("default", func(t *testing.T) {
				run := run(t, testCase, "")
				if got := hex.EncodeToString(run.raw); got != finishBridgeFailureReplyFrameHex {
					t.Fatalf("%s failure frame=%s want=%s", testCase.stage, got, finishBridgeFailureReplyFrameHex)
				}
				if len(run.packets) != 1 {
					t.Fatalf("%s emitted %d packet(s) want 1: %s", testCase.stage, len(run.packets), hex.EncodeToString(run.raw))
				}
				if len(run.raw) != 0 && !bytes.Equal(run.raw, finishBridgeBusinessFailureFrame(t)) {
					t.Fatalf("%s frame=%x differs from the proven business-failure frame", testCase.stage, run.raw)
				}
				for _, want := range []string{
					testCase.blocked,
					"EVENT kind=game-upper-finish-quest-failure-reply",
					"stage=" + testCase.stage,
					"msg_id=34",
					"classification=1",
					"failure_code=22",
					"reason=settlement_context_deadline_exceeded",
				} {
					if !strings.Contains(run.logText, want) {
						t.Fatalf("%s log missing %q:\n%s", testCase.stage, want, run.logText)
					}
				}
				// 加载阶段失败同样不得置位回执（客户端重试仍必须走完整流程）。
				request := finishBridgeRequest()
				request.QuestID = testCase.questID
				if run.session.currentFinishQuestAnswered(newCurrentFinishQuestReplayKey(19, request)) {
					t.Fatalf("%s failure reply marked the quest as answered; retry would be suppressed", testCase.stage)
				}
			})

			t.Run("legacy", func(t *testing.T) {
				run := run(t, testCase, "legacy")
				if len(run.raw) != 0 {
					t.Fatalf("%s legacy tier emitted %d byte(s)=%x want 0", testCase.stage, len(run.raw), run.raw)
				}
				if !strings.Contains(run.logText, testCase.blocked) {
					t.Fatalf("%s legacy tier lost the blocked evidence line:\n%s", testCase.stage, run.logText)
				}
				if strings.Contains(run.logText, "game-upper-finish-quest-failure-reply") {
					t.Fatalf("%s legacy tier logged a failure reply it did not send:\n%s", testCase.stage, run.logText)
				}
			})
		})
	}
}

// finishBridgeRequestBody 组一个 op34 的 10 字节合法请求体（回显 0x0022 + 任务号
// + FF FF 选奖哨兵 + 倍数 1 + 已观测尾哨兵 FF FF）。
func finishBridgeRequestBody(questID uint16) []byte {
	return []byte{0x22, 0x00, byte(questID), byte(questID >> 8), 0xFF, 0xFF, 0x01, 0x00, 0xFF, 0xFF}
}

// runFinishBridgeCatalogLoadFailure 走完整 op34 入口，但任务目录加载必然失败
// （questCatalogLoadErr 已注入）⇒ 不碰 PVF、不碰数据库、不依赖真实环境。
func runFinishBridgeCatalogLoadFailure(t *testing.T) (string, []dnfproto.ChannelPacket) {
	t.Helper()
	repositories := finishBridgeSeed(t)
	logPath := filepath.Join(t.TempDir(), "finish-timing-catalog-failure.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options:             options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		packetLog:           logger,
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
		questCatalogLoadErr: errors.New("finish timing test: quest catalog unavailable"),
	}
	session := &gameSession{conn: connection, connID: "finish-quest-timing-catalog-failure", selectedCharacterID: 19}
	if err := service.handleCurrentFinishQuest(session, finishBridgeValidRequestBody()); err != nil {
		t.Fatalf("finish handler returned error: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(logged), splitAllFinishBridgePackets(t, connection.write.Bytes())
}

func finishBridgeTimingMillis(t *testing.T, text string, field string) int64 {
	t.Helper()
	marker := field + "="
	index := strings.Index(text, marker)
	if index < 0 {
		t.Fatalf("timing log missing %q:\n%s", marker, text)
	}
	rest := text[index+len(marker):]
	end := strings.IndexAny(rest, " \n")
	if end < 0 {
		end = len(rest)
	}
	value, err := strconv.ParseInt(rest[:end], 10, 64)
	if err != nil {
		t.Fatalf("timing log field %s is not an integer: %q", field, rest[:end])
	}
	return value
}

type finishBridgeDelayedSettlement struct {
	inner dnfrepo.CharacterSettlementUnitOfWork
	delay time.Duration
}

func (f finishBridgeDelayedSettlement) WithinCharacterSettlement(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.Group) error,
) error {
	time.Sleep(f.delay)
	return f.inner.WithinCharacterSettlement(ctx, characterID, apply)
}

var _ dnfrepo.CharacterSettlementUnitOfWork = finishBridgeDelayedSettlement{}
