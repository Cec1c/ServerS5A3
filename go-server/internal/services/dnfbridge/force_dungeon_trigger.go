package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

// 本文件实现**外部可触发的「模拟队长选定副本（整队进图）」入口**，只用于自动化测试。
//
// # 为什么需要它
//
// 操作者客户端带完整腾讯 TP 反外挂：游戏内键鼠注入被拦（2026-09-27 实测：方向键按住数十次、
// 角色坐标像素级不变）。而"在选择器里选一个副本并按空格"这一步在服务端就是一条
// class1/op16（21 字节）⇒ `handleDungeonSelectUpper(session, body)`
// （`dungeon_navigation_runtime.go:44`，内部走 `handleDungeonSelectUpperPlanned(session, body, nil, true)`，
// `fanoutParty=true` ⇒ 队长这一次选图会把整队带进副本）。
//
// 没有这个入口，"一起打怪 / 副本内队友状态同步 / 队员进图"这些现场症状无法在无人值守下复现与验证。
//
// # 请求体形状（照抄真实客户端，不臆造一个字节）
//
// 现场历史日志里真实客户端发的 op16 字段是：
//
//	game-dungeon-select-request-decoded body_len=21 dungeon_id=3 difficulty=0 entry_option=0
//	selection_mode=0 runtime_state=0 runtime_token=65535 reserved=0 party_state=0
//	offset16_unproven=0 special_mode=0
//
// 本触发器复用同一条 21 字节写入器 `encodeCurrentDungeonSelectRequest`，只把
// `LeaderObjectKey` 换成发起者本人的场景 actor object key（队伍进图时该字段必须指向队长，
// 见 `dungeon_navigation_runtime.go:230` 对"队员按 space 也会发 op16（runtime_token=0xFFFF +
// 队长的 object key）"的现场记录），其余字段与上面那份日志逐字段一致。
//
// # 协议（命令文件）
//
//	`DNFBRIDGE_FORCE_DUNGEON_CMD`（默认 `force_dungeon.cmd`，相对进程工作目录）第一行有效：
//	  `<char_id> <dungeon_id> [difficulty]`（十进制；多余字段忽略，可用来追加 nonce 绕过去重）
//	结果文件 `…_RESULT`（默认 `force_dungeon.result`）：
//	  `ok char_id=… dungeon_id=… difficulty=… body_len=21`
//	  或 `rejected reason=<枚举> char_id=…`
//	处理成功后命令文件被删除；同一内容重复出现会被记忆跳过（防删除失败时重复执行）。
//
// # 安全边界（与其它测试触发器同一套范式）
//
//   - 开关 `DNFBRIDGE_FORCE_DUNGEON` **默认 off**：off 档不启动 goroutine、不读命令文件、
//     不写结果文件、不发一个字节。
//   - 只对**在线且权威**的角色生效；只调用既有入口 `handleDungeonSelectUpper`，
//     **不新增任何协议字节**，等级/任务/深渊派对/队伍 owner 等既有闸门全部原样生效
//     （被拒时既有代码会按既有口径回拒绝包并记具名事件，本触发器只负责把请求递进去）。
const forceDungeonEnv = "DNFBRIDGE_FORCE_DUNGEON"

const (
	forceDungeonCommandPathEnv = "DNFBRIDGE_FORCE_DUNGEON_CMD"
	forceDungeonResultPathEnv  = "DNFBRIDGE_FORCE_DUNGEON_RESULT"
	forceDungeonIntervalEnv    = "DNFBRIDGE_FORCE_DUNGEON_INTERVAL_MS"
)

const (
	defaultForceDungeonCommandPath = "force_dungeon.cmd"
	defaultForceDungeonResultPath  = "force_dungeon.result"
	defaultForceDungeonIntervalMS  = 1000
	minForceDungeonIntervalMS      = 100
)

// 现场真实客户端 op16 里 runtime_token 固定是 0xFFFF（见文件头那份日志）。
const forceDungeonRuntimeToken = 0xFFFF

const (
	forceDungeonReasonBadCommand         = "bad_command"
	forceDungeonReasonZeroCharacterID    = "zero_character_id"
	forceDungeonReasonZeroDungeonID      = "zero_dungeon_id"
	forceDungeonReasonCharacterNotOnline = "character_not_online"
	forceDungeonReasonNotAuthoritative   = "not_authoritative_session"
	forceDungeonReasonCallFailedPrefix   = "call_failed:"
	forceDungeonSourceCommandFile        = "force_dungeon_command_file"
	forceDungeonEventRejected            = "game-force-dungeon-rejected"
	forceDungeonEventRequested           = "game-force-dungeon-requested"
	forceDungeonEventCompleted           = "game-force-dungeon-completed"
)

// forceDungeonRuntime 承载触发器的去重状态。
//
// 这里刻意用**包级**状态而不是 Service 字段：本文件是纯测试入口，包级状态让它可以完全
// 自包含地加入/移除，不必改动 Service 结构体（其余触发器用 Service 字段是历史选择）。
var forceDungeonState = struct {
	mu   sync.Mutex
	last string
}{}

// forceDungeonEnabled 只有显式 `on` 才打开（与其它测试触发器同口径）。
func forceDungeonEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(forceDungeonEnv)), "on")
}

func forceDungeonCommandPath() string {
	if value := strings.TrimSpace(os.Getenv(forceDungeonCommandPathEnv)); value != "" {
		return value
	}
	return defaultForceDungeonCommandPath
}

func forceDungeonResultPath() string {
	if value := strings.TrimSpace(os.Getenv(forceDungeonResultPathEnv)); value != "" {
		return value
	}
	return defaultForceDungeonResultPath
}

func forceDungeonInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(forceDungeonIntervalEnv))
	if raw == "" {
		return time.Duration(defaultForceDungeonIntervalMS) * time.Millisecond
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minForceDungeonIntervalMS {
		return time.Duration(defaultForceDungeonIntervalMS) * time.Millisecond
	}
	return time.Duration(value) * time.Millisecond
}

// startForceDungeonTrigger 在开关打开时起一个轮询 goroutine；off 档直接返回（零副作用）。
func (s *Service) startForceDungeonTrigger(ctx context.Context) {
	if s == nil || ctx == nil || !forceDungeonEnabled() {
		return
	}
	commandPath := forceDungeonCommandPath()
	if commandPath == "" {
		return
	}
	interval := forceDungeonInterval()
	if interval <= 0 {
		return
	}
	resultPath := forceDungeonResultPath()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runForceDungeonLoop(ctx, commandPath, resultPath, interval)
	}()
}

func (s *Service) runForceDungeonLoop(ctx context.Context, commandPath, resultPath string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	s.pollForceDungeonCommand(commandPath, resultPath)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollForceDungeonCommand(commandPath, resultPath)
		}
	}
}

// pollForceDungeonCommand 读一次命令文件；不存在/空内容/内容与上次相同都直接返回。
func (s *Service) pollForceDungeonCommand(commandPath, resultPath string) {
	raw, err := os.ReadFile(commandPath)
	if err != nil {
		return
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return
	}
	forceDungeonState.mu.Lock()
	duplicate := text == forceDungeonState.last
	if !duplicate {
		forceDungeonState.last = text
	}
	forceDungeonState.mu.Unlock()
	if duplicate {
		return
	}
	_ = os.Remove(commandPath)

	characterID, dungeonID, difficulty, entryOption, parseErr := parseForceDungeonCommand(text)
	if parseErr != nil {
		s.logPacketEvent(forceDungeonEventRejected,
			"reason", forceDungeonReasonBadCommand,
			"command", text,
			"detail", parseErr.Error())
		s.writeForceDungeonResult(resultPath, "rejected reason="+forceDungeonReasonBadCommand+" char_id=0")
		return
	}
	ok, reason := s.forceDungeonSelect(characterID, dungeonID, difficulty, entryOption)
	if !ok {
		s.writeForceDungeonResult(resultPath, fmt.Sprintf(
			"rejected reason=%s char_id=%d", reason, characterID))
		return
	}
	s.writeForceDungeonResult(resultPath, fmt.Sprintf(
		"ok char_id=%d dungeon_id=%d difficulty=%d entry_option=%d body_len=%d",
		characterID, dungeonID, difficulty, entryOption, dungeoncmd.SelectDungeonRequestSize))
}

// parseForceDungeonCommand 解析 `<char_id> <dungeon_id> [difficulty|nonce] [entry_option]`；
// 多余字段忽略。
//
// 第 4 个字段（entry_option）是为了能真正走到**深渊派对**那条链路：op16 的
// `body[5:7]` 是 `EntryOption`（u16 LE，见 dungeoncmd/request.go:138），
// 客户端点「挑战深渊派对」时低字节写 1；而逐槽位深渊资格判定只在带这个标志的
// 请求上跑（dungeon_navigation_runtime.go 的 evaluateHellPartyDungeonEntry）。
// 本触发器原先把它硬编码为 0，于是**永远测不到深渊分支**。
// 不写该字段时仍为 0 ⇒ 既有用法逐字节不变。
func parseForceDungeonCommand(text string) (uint16, uint32, byte, uint16, error) {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return 0, 0, 0, 0, fmt.Errorf("want `<char_id> <dungeon_id> [difficulty|nonce] [entry_option]`, got %d field(s)", len(fields))
	}
	characterID, err := strconv.ParseUint(fields[0], 10, 16)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("character id %q is not a decimal character id", fields[0])
	}
	dungeonID, err := strconv.ParseUint(fields[1], 10, 32)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("dungeon id %q is not a decimal dungeon id", fields[1])
	}
	difficulty := uint64(0)
	if len(fields) >= 3 {
		// 第三个字段是"可选的难度"：是十进制数字就按难度解析（越界即拒绝），
		// 不是数字就按 **nonce** 忽略 —— 现场约定每次触发都写不同内容绕过去重记忆。
		if value, err := strconv.ParseUint(fields[2], 10, 8); err == nil {
			difficulty = value
		} else if forceDungeonAllDigits(fields[2]) {
			return 0, 0, 0, 0, fmt.Errorf("difficulty %q is not a decimal byte", fields[2])
		}
	}
	entryOption := uint64(0)
	if len(fields) >= 4 {
		// 第 4 个字段同样是"数字才是 entry_option"：非数字按**现场约定的 nonce** 忽略，
		// 这样既有的 `<char> <dungeon> <difficulty> <nonce>` 写法逐字节不变
		// （force_dungeon_trigger_test.go 里 `4 5 2 nonce-p9` 就是这条约定）。
		if value, err := strconv.ParseUint(fields[3], 10, 16); err == nil {
			entryOption = value
		} else if forceDungeonAllDigits(fields[3]) {
			return 0, 0, 0, 0, fmt.Errorf("entry option %q is not a decimal u16", fields[3])
		}
	}
	return uint16(characterID), uint32(dungeonID), byte(difficulty), uint16(entryOption), nil
}

// forceDungeonAllDigits 判断一个字段是否是纯十进制数字（用来区分"越界的难度"与"nonce"）。
func forceDungeonAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// forceDungeonOnlineDiagnostics 只在"角色被判为不在线"这条拒绝路径上调用，
// 把两张登记表（`gameSessions` 角色目录 / `liveGameSessions` 活传输索引）与
// `onlinePlayers` 的现状一次性写出来。**纯只读**：不注册、不清理、不改任何状态。
//
// 为什么要它（现场 2026-09-27 18:28–18:35）：客户端明明在城里（截图 + 服务端
// `game-upper-select-character-resolved char_id=3`），触发器却连续 4 次报
// `character_not_online`。只报"不在线"无法区分三种完全不同的成因：
//
//	① 角色目录里**根本没有**这个角色（登记漏了 / 被删了）；
//	② 目录里有，但指向一条已关闭的旧会话（`onlineGameSession` 会返回它，
//	   那会走到 `not_authoritative_session` 而不是这里）；
//	③ 活传输索引里还有同角色的 socket，只是它没被登记为目录 owner。
//
// 这三种成因的修法完全不同，所以拒绝事件必须自带这三张表的现场。
func (s *Service) forceDungeonOnlineDiagnostics(characterID uint16) []any {
	if s == nil {
		return []any{"diag", "service_nil"}
	}
	s.mu.Lock()
	catalogTotal := len(s.gameSessions)
	catalogConn := ""
	if session, ok := s.gameSessions[characterID]; ok && session != nil {
		catalogConn = session.connID
	}
	liveTotal := len(s.liveGameSessions)
	liveBindings := make([]string, 0, len(s.liveGameSessions))
	liveSame := make([]string, 0, 2)
	for connID, session := range s.liveGameSessions {
		if session == nil {
			continue
		}
		if session.selectedCharacterID != 0 {
			liveBindings = append(liveBindings,
				connID+":"+strconv.Itoa(int(session.selectedCharacterID)))
		}
		if session.selectedCharacterID == characterID {
			liveSame = append(liveSame, connID)
		}
	}
	s.mu.Unlock()
	sort.Strings(liveBindings)
	sort.Strings(liveSame)
	onlinePlayersConn := ""
	onlinePlayersKnown := false
	if s.onlinePlayers != nil {
		onlinePlayersKnown = true
		if resident := s.onlinePlayers.SessionForCharacter(characterID); resident != nil {
			onlinePlayersConn = resident.connID
		}
	}
	return []any{
		"diag_catalog_total", catalogTotal,
		"diag_catalog_conn", catalogConn,
		"diag_live_total", liveTotal,
		"diag_live_bindings", strings.Join(liveBindings, ","),
		"diag_live_same_char", strings.Join(liveSame, ","),
		"diag_online_players_wired", onlinePlayersKnown,
		"diag_online_players_conn", onlinePlayersConn,
	}
}

// writeForceDungeonResult 覆盖写结果文件（失败只记日志，不影响触发器本身）。
func (s *Service) writeForceDungeonResult(resultPath, line string) {
	if strings.TrimSpace(resultPath) == "" {
		return
	}
	if err := os.WriteFile(resultPath, []byte(line+"\n"), 0o644); err != nil {
		s.logPacketEvent("game-force-dungeon-result-write-failed",
			"path", resultPath,
			"detail", err.Error())
	}
}

// forceDungeonSelect 用指定角色（应当是队长）发出一次与客户端逐字段同形的 op16 选图请求。
// 返回值：(是否成功递入, 失败原因)。
func (s *Service) forceDungeonSelect(characterID uint16, dungeonID uint32, difficulty byte, entryOption uint16) (bool, string) {
	if characterID == 0 {
		s.logPacketEvent(forceDungeonEventRejected,
			"reason", forceDungeonReasonZeroCharacterID,
			"char_id", characterID)
		return false, forceDungeonReasonZeroCharacterID
	}
	if dungeonID == 0 {
		s.logPacketEvent(forceDungeonEventRejected,
			"reason", forceDungeonReasonZeroDungeonID,
			"char_id", characterID)
		return false, forceDungeonReasonZeroDungeonID
	}
	session, online := s.onlineGameSession(characterID)
	if !online || session == nil {
		fields := []any{
			"reason", forceDungeonReasonCharacterNotOnline,
			"char_id", characterID,
		}
		fields = append(fields, s.forceDungeonOnlineDiagnostics(characterID)...)
		s.logPacketEvent(forceDungeonEventRejected, fields...)
		return false, forceDungeonReasonCharacterNotOnline
	}
	if _, current := s.boundGameSessionCharacterSnapshot(session); !current {
		s.logGameEvent(session, forceDungeonEventRejected,
			"reason", forceDungeonReasonNotAuthoritative,
			"char_id", characterID,
			"character_generation", session.characterGeneration)
		return false, forceDungeonReasonNotAuthoritative
	}

	state := runtimePartyStateSnapshot(session)
	body := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{
		DungeonID:       dungeonID,
		Difficulty:      difficulty,
		EntryOption:     entryOption,
		SelectionMode:   0,
		RuntimeState:    0,
		RuntimeToken:    forceDungeonRuntimeToken,
		Reserved:        0,
		PartyState:      0,
		LeaderObjectKey: uint32(currentSceneActorObjectKey(characterID)),
		SpecialMode:     0,
	})
	s.logGameEvent(session, forceDungeonEventRequested,
		"char_id", characterID,
		"dungeon_id", dungeonID,
		"difficulty", difficulty,
		"entry_option", entryOption,
		"body_len", len(body),
		"party_id", state.PartyID,
		"leader_char_id", state.UserID,
		"is_leader", state.UserID == characterID,
		"leader_object_key", currentSceneActorObjectKey(characterID),
		"runtime_token", forceDungeonRuntimeToken,
		"source", forceDungeonSourceCommandFile)

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	err := s.callGameSession(ctx, session, "force-dungeon-command", func() error {
		return s.handleDungeonSelectUpper(session, body)
	})
	if err != nil {
		s.logGameEvent(session, forceDungeonEventRejected,
			"reason", forceDungeonReasonCallFailedPrefix+err.Error(),
			"char_id", characterID)
		return false, forceDungeonReasonCallFailedPrefix + err.Error()
	}
	session.dungeon.mu.Lock()
	hasRuntime := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	s.logGameEvent(session, forceDungeonEventCompleted,
		"char_id", characterID,
		"dungeon_id", dungeonID,
		"difficulty", difficulty,
		"body_len", len(body),
		"party_id", state.PartyID,
		"leader_char_id", state.UserID,
		"session_has_dungeon_runtime", hasRuntime,
		"source", forceDungeonSourceCommandFile)
	return true, ""
}
