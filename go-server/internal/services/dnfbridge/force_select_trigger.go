package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 本文件实现**外部可触发的「模拟某个会话打开副本选择器」入口**，只用于自动化测试。
//
// # 为什么需要它
//
// 操作者客户端带完整腾讯 TP 反外挂：游戏内键鼠注入被拦（2026-09-27 再次实测：在两个客户端上
// 按住方向键 6+6+4+4 次、每次 260 ms，角色坐标与镜头**像素级不变**）。而"走到副本门口按空格"
// 这一步在服务端就是一条 class1/op15：`sendEnterSelectDungeonState(session, source,
// transport=false, requestDriven=true)`。
//
// 没有这个入口，组队相关的现场症状在无人值守下无法复现与验证：
//   - 队长开选择器 → 队员应当拿到完整的"跟随者选择器转换"（见
//     `synchronizeRuntimePartyMembersEnterSelect` / `prepareRuntimePartyFollowerEnterSelect`）；
//   - **队员自己先开选择器** → 既有实现在 `game-party-enter-select-non-leader-initiated`
//     处只记一条日志就返回（现场 `packet_log` 里这条出现了 6 次），队员只拿到普通 op15 应答、
//     拿不到那条完整转换，客户端表现为"只有一个背景、没有副本列表"（操作者报障原文）。
//
// # 协议（命令文件）
//
//	`DNFBRIDGE_FORCE_SELECT_CMD`（默认 `force_select.cmd`，相对进程工作目录）第一行有效：
//	  `<char_id>`（十进制，可跟任意多余字段，忽略）
//	结果文件 `…_RESULT`（默认 `force_select.result`）：
//	  `ok char_id=… party_id=… leader_char_id=… is_leader=…`
//	  或 `rejected reason=<枚举> char_id=…`
//	处理成功后命令文件被删除；同一内容重复出现会被记忆跳过（防删除失败时重复执行）。
//
// # 安全边界（与 party_force_trigger.go 同一套范式）
//
//   - 开关 `DNFBRIDGE_FORCE_SELECT` **默认 off**：off 档不启动 goroutine、不读命令文件、
//     不写结果文件、不发一个字节。
//   - 只对**在线且权威**的角色生效：没有当前会话、或该角色已有更新的会话都直接拒绝。
//   - 只调用既有入口 `sendEnterSelectDungeonState`，**不新增任何协议字节**、不改任何既有 handler。
const forceSelectEnv = "DNFBRIDGE_FORCE_SELECT"

const (
	forceSelectCommandPathEnv = "DNFBRIDGE_FORCE_SELECT_CMD"
	forceSelectResultPathEnv  = "DNFBRIDGE_FORCE_SELECT_RESULT"
	forceSelectIntervalEnv    = "DNFBRIDGE_FORCE_SELECT_INTERVAL_MS"
)

const (
	defaultForceSelectCommandPath = "force_select.cmd"
	defaultForceSelectResultPath  = "force_select.result"
	defaultForceSelectIntervalMS  = 1000
	minForceSelectIntervalMS      = 100
)

// 失败原因枚举（写进结果文件与 `game-force-select-rejected` 事件）。
const (
	forceSelectReasonBadCommand           = "bad_command"
	forceSelectReasonZeroCharacterID      = "zero_character_id"
	forceSelectReasonCharacterNotOnline   = "character_not_online"
	forceSelectReasonNotAuthoritative     = "not_authoritative_session"
	forceSelectReasonCallFailedPrefix     = "call_failed:"
	forceSelectSourceForceSelectCommand   = "force_select_command_file"
	forceSelectEventRejected              = "game-force-select-rejected"
	forceSelectEventRequested             = "game-force-select-requested"
	forceSelectEventCompleted             = "game-force-select-completed"
	forceSelectResultOkFormat             = "ok char_id=%d party_id=%d leader_char_id=%d is_leader=%t"
	forceSelectResultRejectedFormatPrefix = "rejected reason="
)

// forceSelectEnabled 只有显式 `on` 才打开（与 DNFBRIDGE_PARTY_FORCE 同一口径）。
func forceSelectEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(forceSelectEnv)), "on")
}

func forceSelectCommandPath() string {
	if value := strings.TrimSpace(os.Getenv(forceSelectCommandPathEnv)); value != "" {
		return value
	}
	return defaultForceSelectCommandPath
}

func forceSelectResultPath() string {
	if value := strings.TrimSpace(os.Getenv(forceSelectResultPathEnv)); value != "" {
		return value
	}
	return defaultForceSelectResultPath
}

func forceSelectInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(forceSelectIntervalEnv))
	if raw == "" {
		return time.Duration(defaultForceSelectIntervalMS) * time.Millisecond
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minForceSelectIntervalMS {
		return time.Duration(defaultForceSelectIntervalMS) * time.Millisecond
	}
	return time.Duration(value) * time.Millisecond
}

// forceSelectRuntime 承载触发器的运行时状态（记忆已处理过的命令内容，防重复执行）。
type forceSelectRuntime struct {
	mu   sync.Mutex
	last string
}

func (s *Service) forceSelectState() *forceSelectRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forceSelectRuntime == nil {
		s.forceSelectRuntime = &forceSelectRuntime{}
	}
	return s.forceSelectRuntime
}

// startForceSelectTrigger 在开关打开时起一个轮询 goroutine；off 档直接返回（零副作用）。
func (s *Service) startForceSelectTrigger(ctx context.Context) {
	if s == nil || ctx == nil || !forceSelectEnabled() {
		return
	}
	commandPath := forceSelectCommandPath()
	if commandPath == "" {
		return
	}
	interval := forceSelectInterval()
	if interval <= 0 {
		return
	}
	resultPath := forceSelectResultPath()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runForceSelectLoop(ctx, commandPath, resultPath, interval)
	}()
}

func (s *Service) runForceSelectLoop(ctx context.Context, commandPath, resultPath string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// 启动即查一次：部署后立刻写命令文件也能被处理，不必等第一个 tick。
	s.pollForceSelectCommand(commandPath, resultPath)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollForceSelectCommand(commandPath, resultPath)
		}
	}
}

// pollForceSelectCommand 读一次命令文件；不存在/空内容/内容与上次相同都直接返回。
func (s *Service) pollForceSelectCommand(commandPath, resultPath string) {
	raw, err := os.ReadFile(commandPath)
	if err != nil {
		return
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return
	}
	state := s.forceSelectState()
	state.mu.Lock()
	duplicate := text == state.last
	if !duplicate {
		state.last = text
	}
	state.mu.Unlock()
	if duplicate {
		return
	}
	// 先删文件再执行：命令只处理一次，重复触发必须由调用方重新写文件。
	_ = os.Remove(commandPath)

	characterID, parseErr := parseForceSelectCommand(text)
	if parseErr != nil {
		s.logPacketEvent(forceSelectEventRejected,
			"reason", forceSelectReasonBadCommand,
			"command", text,
			"detail", parseErr.Error())
		s.writeForceSelectResult(resultPath, forceSelectResultRejectedFormatPrefix+forceSelectReasonBadCommand+" char_id=0")
		return
	}
	ok, partyID, leaderID, isLeader, reason := s.forceEnterSelectDungeon(characterID)
	if !ok {
		s.writeForceSelectResult(resultPath, fmt.Sprintf(
			"%s%s char_id=%d", forceSelectResultRejectedFormatPrefix, reason, characterID))
		return
	}
	s.writeForceSelectResult(resultPath, fmt.Sprintf(
		forceSelectResultOkFormat, characterID, partyID, leaderID, isLeader))
}

// parseForceSelectCommand 解析 `<char_id>`；多余字段忽略。
func parseForceSelectCommand(text string) (uint16, error) {
	fields := strings.Fields(text)
	if len(fields) < 1 {
		return 0, fmt.Errorf("want `<char_id>`, got 0 field(s)")
	}
	characterID, err := strconv.ParseUint(fields[0], 10, 16)
	if err != nil {
		return 0, fmt.Errorf("character id %q is not a decimal character id", fields[0])
	}
	return uint16(characterID), nil
}

// writeForceSelectResult 覆盖写结果文件（失败只记日志，不影响触发器本身）。
func (s *Service) writeForceSelectResult(resultPath, line string) {
	if strings.TrimSpace(resultPath) == "" {
		return
	}
	if err := os.WriteFile(resultPath, []byte(line+"\n"), 0o644); err != nil {
		s.logPacketEvent("game-force-select-result-write-failed",
			"path", resultPath,
			"detail", err.Error())
	}
}

// forceEnterSelectDungeon 让指定角色的会话走一遍"客户端打开副本选择器"的既有入口。
// 返回值：(是否成功, party id, 队长角色 id, 本人是否队长, 失败原因)。
func (s *Service) forceEnterSelectDungeon(characterID uint16) (bool, int, uint16, bool, string) {
	if characterID == 0 {
		s.logPacketEvent(forceSelectEventRejected,
			"reason", forceSelectReasonZeroCharacterID,
			"char_id", characterID)
		return false, 0, 0, false, forceSelectReasonZeroCharacterID
	}
	session, online := s.onlineGameSession(characterID)
	if !online || session == nil {
		s.logPacketEvent(forceSelectEventRejected,
			"reason", forceSelectReasonCharacterNotOnline,
			"char_id", characterID)
		return false, 0, 0, false, forceSelectReasonCharacterNotOnline
	}
	if _, current := s.boundGameSessionCharacterSnapshot(session); !current {
		s.logGameEvent(session, forceSelectEventRejected,
			"reason", forceSelectReasonNotAuthoritative,
			"char_id", characterID,
			"character_generation", session.characterGeneration)
		return false, 0, 0, false, forceSelectReasonNotAuthoritative
	}
	state := runtimePartyStateSnapshot(session)
	s.logGameEvent(session, forceSelectEventRequested,
		"char_id", characterID,
		"party_id", state.PartyID,
		"leader_char_id", state.UserID,
		"is_leader", state.UserID == characterID,
		"source", forceSelectSourceForceSelectCommand)

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	err := s.callGameSession(ctx, session, "force-select-command", func() error {
		return s.sendEnterSelectDungeonState(session, forceSelectSourceForceSelectCommand, false, true)
	})
	if err != nil {
		s.logGameEvent(session, forceSelectEventRejected,
			"reason", forceSelectReasonCallFailedPrefix+err.Error(),
			"char_id", characterID)
		return false, state.PartyID, state.UserID, false, forceSelectReasonCallFailedPrefix + err.Error()
	}
	s.logGameEvent(session, forceSelectEventCompleted,
		"char_id", characterID,
		"party_id", state.PartyID,
		"leader_char_id", state.UserID,
		"is_leader", state.UserID == characterID,
		"enter_select_sent", session.enterSelectDungeonSent,
		"enter_select_context_sent", session.enterSelectDungeonContextSent,
		"source", forceSelectSourceForceSelectCommand)
	return true, state.PartyID, state.UserID, state.UserID == characterID, ""
}
