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

// 本文件实现**外部可触发的「走出赛丽亚房间（op1425 回上一个村镇）」入口**，只用于自动化测试。
//
// # 为什么需要它
//
// 操作者客户端带完整腾讯 TP 反外挂：游戏内键鼠注入被拦（2026-09-27 实测：按住方向键数十次、
// 每次 260 ms，角色坐标与镜头像素级不变）。而"走到门口出门"这一步在服务端就是一条
// class1/op1425 ⇒ `handleCurrentPrevVillage(session, nil)`（`town_prev_village.go:134`，
// 现场实测该请求 body 长度必须是 0，非空会被 `current_exe_op1425_body_boundary_mismatch` 拒）。
//
// 为什么组队测试必须走这条路而不是改数据：登录三名落点的第一级是"该城镇自己的赛丽亚房间"，
// 两个角色各自落在**每角色一份**的私有实例里，服务端侧 `PeerInSameArea` 恒假、客户端互相看不见。
// 只有"出门"这条**生产路径**会把两人放进同一个共享城镇区域（艾尔文防线 38/0，门坐标 622,196），
// 并按既有口径发布存在感（`prev_village_return_from_private_seria_room`）——
// 因此测试必须复刻这条路径，而不是把角色数据改到别的城镇去（那样会破坏账号数据与等级/任务口径）。
//
// # 协议（命令文件）
//
//	`DNFBRIDGE_FORCE_TOWN_EXIT_CMD`（默认 `force_town_exit.cmd`，相对进程工作目录）第一行有效：
//	  `<char_id>`（十进制，多余字段忽略）
//	结果文件 `…_RESULT`（默认 `force_town_exit.result`）：
//	  `ok char_id=… town_id=… area_id=… position_x=… position_y=…`
//	  或 `rejected reason=<枚举> char_id=…`
//	处理成功后命令文件被删除；同一内容重复出现会被记忆跳过（防删除失败时重复执行）。
//	**注意**：命令内容需要每次不同（追加任意 nonce 即可，多余字段会被忽略），否则会被去重跳过。
//
// # 安全边界（与 party_force_trigger.go / force_select_trigger.go 同一套范式）
//
//   - 开关 `DNFBRIDGE_FORCE_TOWN_EXIT` **默认 off**：off 档不启动 goroutine、不读命令文件、
//     不写结果文件、不发一个字节。
//   - 只对**在线且权威**的角色生效：没有当前会话、或该角色已有更新的会话都直接拒绝。
//   - 只调用既有入口 `handleCurrentPrevVillage`，**不新增任何协议字节**、不改任何既有 handler；
//     该入口自身的全部前置校验（必须在赛丽亚房间、无副本运行时、场景已收尾）原样生效。
const forceTownExitEnv = "DNFBRIDGE_FORCE_TOWN_EXIT"

const (
	forceTownExitCommandPathEnv = "DNFBRIDGE_FORCE_TOWN_EXIT_CMD"
	forceTownExitResultPathEnv  = "DNFBRIDGE_FORCE_TOWN_EXIT_RESULT"
	forceTownExitIntervalEnv    = "DNFBRIDGE_FORCE_TOWN_EXIT_INTERVAL_MS"
)

const (
	defaultForceTownExitCommandPath = "force_town_exit.cmd"
	defaultForceTownExitResultPath  = "force_town_exit.result"
	defaultForceTownExitIntervalMS  = 1000
	minForceTownExitIntervalMS      = 100
)

// 失败原因枚举（写进结果文件与 `game-force-town-exit-rejected` 事件）。
const (
	forceTownExitReasonBadCommand         = "bad_command"
	forceTownExitReasonZeroCharacterID    = "zero_character_id"
	forceTownExitReasonCharacterNotOnline = "character_not_online"
	forceTownExitReasonNotAuthoritative   = "not_authoritative_session"
	forceTownExitReasonCallFailedPrefix   = "call_failed:"
	forceTownExitSourceCommandFile        = "force_town_exit_command_file"
	forceTownExitEventRejected            = "game-force-town-exit-rejected"
	forceTownExitEventRequested           = "game-force-town-exit-requested"
	forceTownExitEventCompleted           = "game-force-town-exit-completed"
)

// forceTownExitEnabled 只有显式 `on` 才打开（与 DNFBRIDGE_PARTY_FORCE / DNFBRIDGE_FORCE_SELECT 同口径）。
func forceTownExitEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(forceTownExitEnv)), "on")
}

func forceTownExitCommandPath() string {
	if value := strings.TrimSpace(os.Getenv(forceTownExitCommandPathEnv)); value != "" {
		return value
	}
	return defaultForceTownExitCommandPath
}

func forceTownExitResultPath() string {
	if value := strings.TrimSpace(os.Getenv(forceTownExitResultPathEnv)); value != "" {
		return value
	}
	return defaultForceTownExitResultPath
}

func forceTownExitInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(forceTownExitIntervalEnv))
	if raw == "" {
		return time.Duration(defaultForceTownExitIntervalMS) * time.Millisecond
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minForceTownExitIntervalMS {
		return time.Duration(defaultForceTownExitIntervalMS) * time.Millisecond
	}
	return time.Duration(value) * time.Millisecond
}

// forceTownExitRuntime 承载触发器的运行时状态（记忆已处理过的命令内容，防重复执行）。
type forceTownExitRuntime struct {
	mu   sync.Mutex
	last string
}

func (s *Service) forceTownExitState() *forceTownExitRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forceTownExitRuntime == nil {
		s.forceTownExitRuntime = &forceTownExitRuntime{}
	}
	return s.forceTownExitRuntime
}

// startForceTownExitTrigger 在开关打开时起一个轮询 goroutine；off 档直接返回（零副作用）。
func (s *Service) startForceTownExitTrigger(ctx context.Context) {
	if s == nil || ctx == nil || !forceTownExitEnabled() {
		return
	}
	commandPath := forceTownExitCommandPath()
	if commandPath == "" {
		return
	}
	interval := forceTownExitInterval()
	if interval <= 0 {
		return
	}
	resultPath := forceTownExitResultPath()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runForceTownExitLoop(ctx, commandPath, resultPath, interval)
	}()
}

func (s *Service) runForceTownExitLoop(ctx context.Context, commandPath, resultPath string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	s.pollForceTownExitCommand(commandPath, resultPath)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollForceTownExitCommand(commandPath, resultPath)
		}
	}
}

// pollForceTownExitCommand 读一次命令文件；不存在/空内容/内容与上次相同都直接返回。
func (s *Service) pollForceTownExitCommand(commandPath, resultPath string) {
	raw, err := os.ReadFile(commandPath)
	if err != nil {
		return
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return
	}
	state := s.forceTownExitState()
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

	characterID, parseErr := parseForceTownExitCommand(text)
	if parseErr != nil {
		s.logPacketEvent(forceTownExitEventRejected,
			"reason", forceTownExitReasonBadCommand,
			"command", text,
			"detail", parseErr.Error())
		s.writeForceTownExitResult(resultPath, "rejected reason="+forceTownExitReasonBadCommand+" char_id=0")
		return
	}
	ok, landing, reason := s.forceTownExitSeriaRoom(characterID)
	if !ok {
		s.writeForceTownExitResult(resultPath, fmt.Sprintf(
			"rejected reason=%s char_id=%d", reason, characterID))
		return
	}
	s.writeForceTownExitResult(resultPath, fmt.Sprintf(
		"ok char_id=%d town_id=%d area_id=%d position_x=%d position_y=%d",
		characterID, landing.TownID, landing.AreaID, landing.PositionX, landing.PositionY))
}

// parseForceTownExitCommand 解析 `<char_id>`；多余字段忽略。
func parseForceTownExitCommand(text string) (uint16, error) {
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

// writeForceTownExitResult 覆盖写结果文件（失败只记日志，不影响触发器本身）。
func (s *Service) writeForceTownExitResult(resultPath, line string) {
	if strings.TrimSpace(resultPath) == "" {
		return
	}
	if err := os.WriteFile(resultPath, []byte(line+"\n"), 0o644); err != nil {
		s.logPacketEvent("game-force-town-exit-result-write-failed",
			"path", resultPath,
			"detail", err.Error())
	}
}

// forceTownExitSeriaRoom 让指定角色的会话走一遍"客户端走出赛丽亚房间"的既有入口。
// 返回值：(是否成功, 落点快照, 失败原因)。
func (s *Service) forceTownExitSeriaRoom(characterID uint16) (bool, currentTownPositionSnapshot, string) {
	if characterID == 0 {
		s.logPacketEvent(forceTownExitEventRejected,
			"reason", forceTownExitReasonZeroCharacterID,
			"char_id", characterID)
		return false, currentTownPositionSnapshot{}, forceTownExitReasonZeroCharacterID
	}
	session, online := s.onlineGameSession(characterID)
	if !online || session == nil {
		s.logPacketEvent(forceTownExitEventRejected,
			"reason", forceTownExitReasonCharacterNotOnline,
			"char_id", characterID)
		return false, currentTownPositionSnapshot{}, forceTownExitReasonCharacterNotOnline
	}
	if _, current := s.boundGameSessionCharacterSnapshot(session); !current {
		s.logGameEvent(session, forceTownExitEventRejected,
			"reason", forceTownExitReasonNotAuthoritative,
			"char_id", characterID,
			"character_generation", session.characterGeneration)
		return false, currentTownPositionSnapshot{}, forceTownExitReasonNotAuthoritative
	}
	session.townMu.Lock()
	before := session.townPositionSnapshot
	session.townMu.Unlock()
	s.logGameEvent(session, forceTownExitEventRequested,
		"char_id", characterID,
		"town_id", before.TownID,
		"area_id", before.AreaID,
		"map_source", "before_force_town_exit",
		"source", forceTownExitSourceCommandFile)

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	// op1425 的现场请求体长度必须是 0（非空会被 current_exe_op1425_body_boundary_mismatch 拒）。
	err := s.callGameSession(ctx, session, "force-town-exit-command", func() error {
		return s.handleCurrentPrevVillage(session, nil)
	})
	if err != nil {
		s.logGameEvent(session, forceTownExitEventRejected,
			"reason", forceTownExitReasonCallFailedPrefix+err.Error(),
			"char_id", characterID)
		return false, before, forceTownExitReasonCallFailedPrefix + err.Error()
	}
	session.townMu.Lock()
	after := session.townPositionSnapshot
	session.townMu.Unlock()
	s.logGameEvent(session, forceTownExitEventCompleted,
		"char_id", characterID,
		"before_town_id", before.TownID,
		"before_area_id", before.AreaID,
		"town_id", after.TownID,
		"area_id", after.AreaID,
		"position_x", after.PositionX,
		"position_y", after.PositionY,
		"landing_changed", after.TownID != before.TownID || after.AreaID != before.AreaID,
		"source", forceTownExitSourceCommandFile)
	return true, after, ""
}
