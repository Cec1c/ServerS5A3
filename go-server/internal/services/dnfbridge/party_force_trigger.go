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

// 本文件实现**外部可触发的「强制组队」入口**，只用于自动化测试。
//
// ## 为什么需要它
//
// 现场（2026-09-26）已经查明：操作者的游戏客户端带完整腾讯 TP 反外挂
// （`TenRpcs.dll`/`TenSE.dll`/`TenSLX.dll`/`TenProxy.dll`/`TenSP.dll`/`TenSM.dll`/
// `TenBount.dll` + `Tenparty.dat`），用 keybd_event / SendInput（绝对与相对鼠标）/
// 扫描码 / PostMessage 四种方式注入键鼠，**游戏内角色不动、ESC 菜单不弹、技能格无反应**
// （只有光标位置与窗口前置能改）⇒ 判定为 TP 拦截合成输入。
//
// 于是"把两个角色组成队伍"这一步**无法由客户端侧自动化**：而它恰恰是验证组队 UDP
// 中继链路（探测 → 观测端点 → 首包播种 → 双向转发）的**唯一前置条件**。
// 本文件把这一步搬到服务端：只要两个角色都在线且各自持有权威会话，服务端就把它们
// 组成同一个运行期队伍，并按**与「客户端自己邀请 + 接受」完全相同的原语**推送完整名册。
//
// ## 设计（为什么用命令文件，而不是 GM HTTP 端点）
//
// 两个候选入口：
//
//	① GM HTTP（`DNFBRIDGE_GM_LISTEN`，现场 7003）加 `POST /party/force`；
//	② 轮询一个本地命令文件（本实现）。
//
// 选 ② 的理由：**不引入新的网络面与鉴权分支**（GM 面板有密码/会话机制，给它加一个
// "直接改运行时状态"的端点会扩大攻击面），而且港机上直接 `echo "3 4" > force_party.cmd`
// 就是一次触发，运维成本最低。需要 HTTP 触发时，用一条 `curl`/`ssh` 写文件即可。
//
// ## 协议
//
//	命令文件（`DNFBRIDGE_PARTY_FORCE_CMD`，默认 `force_party.cmd`，相对进程工作目录）：
//	  第一行有效，格式 `<leader_char_id> <member_char_id>`（十进制，空格/制表符分隔）。
//	  处理成功后文件被**删除**；同一内容重复出现会被记忆跳过（防删除失败时重复执行）。
//	结果文件（`DNFBRIDGE_PARTY_FORCE_RESULT`，默认 `force_party.result`）：
//	  单行 `ok party_id=<id> leader=<id> member=<id> member_count=<n>` 或
//	  `rejected reason=<枚举> leader=<id> member=<id>`。
//
// ## 行为（只做加法）
//
//   - 复用既有原语：`createOrJoinManagedRuntimeParty`（与 op705/op90/op11 接受路径同一个
//     唯一入口）→ `sendManagedRuntimePartySnapshots`（op11 端点行 + op9 + op153 全套）→
//     再给加入方补一次权威名单 `sendRuntimePartyRosterLocal`（与邀请路径一致的
//     `party_invite_join_authoritative_roster` 补发，只是 source 换成 `party_force_...`）。
//   - **绝不改动** op10/op11/op13/op14 任何既有路径：本文件不注册任何 handler，
//     只在开关打开时起一个轮询 goroutine。
//   - 失败一律具名事件 + 具名结果行，原因见下面 `partyForceReason*` 常量。
//
// ## 安全边界
//
//   - `DNFBRIDGE_PARTY_FORCE` **默认 off**：off 档下**不启动 goroutine、不读命令文件、
//     不写结果文件、不发一个字节**（`startPartyForceTrigger` 直接 return），
//     现行为逐字节不变。
//   - 只对**在线且权威**的两个角色生效：任一端没有当前会话、或该角色已有更新的会话
//     （`boundGameSessionCharacterSnapshot` 失败）都直接拒绝，绝不"猜"一个会话去改状态。
//   - 不放宽任何既有校验：`createOrJoinManagedRuntimeParty` 里的频道隔离、代际校验、
//     旧队伍解绑（`reconcileManagedPriorPartyLeave`）全部原样生效。
const partyForceEnv = "DNFBRIDGE_PARTY_FORCE"

// partyForceCommandPathEnv / partyForceResultPathEnv 是命令文件与结果文件路径。
// 默认是**相对路径**（相对进程工作目录，现场即 `/opt/dnf90/force_party.cmd`），
// 这样源码里不写死任何绝对路径。
const (
	partyForceCommandPathEnv = "DNFBRIDGE_PARTY_FORCE_CMD"
	partyForceResultPathEnv  = "DNFBRIDGE_PARTY_FORCE_RESULT"
	partyForceIntervalEnv    = "DNFBRIDGE_PARTY_FORCE_INTERVAL_MS"
)

const (
	partyForceValueOn  = "on"
	partyForceValueOff = "off"
)

const (
	defaultPartyForceCommandPath = "force_party.cmd"
	defaultPartyForceResultPath  = "force_party.result"
	defaultPartyForceIntervalMS  = 1000
	minPartyForceIntervalMS      = 100
)

// 失败原因枚举（写进结果文件与 `game-force-party-rejected` 事件）。
const (
	partyForceReasonBadCommand             = "bad_command"
	partyForceReasonZeroCharacterID        = "zero_character_id"
	partyForceReasonSameCharacter          = "same_character"
	partyForceReasonLeaderNotOnline        = "leader_not_online"
	partyForceReasonMemberNotOnline        = "member_not_online"
	partyForceReasonLeaderNotAuthoritative = "leader_not_authoritative_session"
	partyForceReasonMemberNotAuthoritative = "member_not_authoritative_session"
	partyForceReasonJoinFailedPrefix       = "party_join_failed:"
	partyForceReasonSnapshotFailedPrefix   = "party_snapshot_failed:"
	partyForceReasonRosterFailedPrefix     = "party_roster_failed:"
)

// partyForceMode 是 partyForceEnv 的归一化取值。
type partyForceMode int

const (
	// partyForceOff 是默认档（零值）：完全不启动触发器。
	partyForceOff partyForceMode = iota
	// partyForceOn 启动命令文件轮询。
	partyForceOn
)

func (mode partyForceMode) String() string {
	if mode == partyForceOn {
		return partyForceValueOn
	}
	return partyForceValueOff
}

func (mode partyForceMode) enabled() bool {
	return mode == partyForceOn
}

// readPartyForceMode 解析 DNFBRIDGE_PARTY_FORCE。
// **默认 off**：空/未设置/非法值都是 off，只有显式 `on`（大小写任意）才启用。
func readPartyForceMode(value string) partyForceMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case partyForceValueOn, "true", "1", "yes":
		return partyForceOn
	default:
		return partyForceOff
	}
}

// partyForce 返回当前 Service 的归一化档位（零值 options 的测试/探针路径 = off）。
func (s *Service) partyForce() partyForceMode {
	if s == nil {
		return partyForceOff
	}
	return s.options.partyForce
}

// partyForceIntervalFromMS 把毫秒配置归一化成轮询间隔（非法值回默认，且有下限）。
func partyForceIntervalFromMS(value int) time.Duration {
	if value < minPartyForceIntervalMS {
		value = defaultPartyForceIntervalMS
	}
	return time.Duration(value) * time.Millisecond
}

// partyForceRuntime 承载触发器的运行时状态（记忆已处理过的命令内容，防重复执行）。
type partyForceRuntime struct {
	mu   sync.Mutex
	last string
}

func (s *Service) partyForceState() *partyForceRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.partyForceRuntime == nil {
		s.partyForceRuntime = &partyForceRuntime{}
	}
	return s.partyForceRuntime
}

// startPartyForceTrigger 在开关打开时起一个轮询 goroutine；off 档直接返回（零副作用）。
func (s *Service) startPartyForceTrigger(ctx context.Context) {
	if s == nil || ctx == nil || !s.partyForce().enabled() {
		return
	}
	commandPath := strings.TrimSpace(s.options.partyForceCommandPath)
	resultPath := strings.TrimSpace(s.options.partyForceResultPath)
	interval := s.options.partyForceInterval
	if commandPath == "" || interval <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runPartyForceLoop(ctx, commandPath, resultPath, interval)
	}()
}

func (s *Service) runPartyForceLoop(ctx context.Context, commandPath, resultPath string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// 启动即查一次：部署后立刻写命令文件也能被处理，不必等第一个 tick。
	s.pollPartyForceCommand(commandPath, resultPath)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollPartyForceCommand(commandPath, resultPath)
		}
	}
}

// pollPartyForceCommand 读一次命令文件；不存在/空内容/内容与上次相同都直接返回。
func (s *Service) pollPartyForceCommand(commandPath, resultPath string) {
	raw, err := os.ReadFile(commandPath)
	if err != nil {
		return
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return
	}
	state := s.partyForceState()
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

	leaderID, memberID, parseErr := parsePartyForceCommand(text)
	if parseErr != nil {
		s.logPacketEvent("game-force-party-rejected",
			"reason", partyForceReasonBadCommand,
			"command", text,
			"detail", parseErr.Error())
		s.writePartyForceResult(resultPath, "rejected reason="+partyForceReasonBadCommand+" leader=0 member=0")
		return
	}
	ok, partyID, reason, memberCount := s.forceManagedRuntimeParty(leaderID, memberID)
	if !ok {
		s.writePartyForceResult(resultPath, fmt.Sprintf(
			"rejected reason=%s leader=%d member=%d", reason, leaderID, memberID))
		return
	}
	s.writePartyForceResult(resultPath, fmt.Sprintf(
		"ok party_id=%d leader=%d member=%d member_count=%d", partyID, leaderID, memberID, memberCount))
}

// parsePartyForceCommand 解析 `<leader_char_id> <member_char_id>`；多余字段忽略。
func parsePartyForceCommand(text string) (uint16, uint16, error) {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("want `<leader_id> <member_id>`, got %d field(s)", len(fields))
	}
	leader, err := strconv.ParseUint(fields[0], 10, 16)
	if err != nil {
		return 0, 0, fmt.Errorf("leader id %q is not a decimal character id", fields[0])
	}
	member, err := strconv.ParseUint(fields[1], 10, 16)
	if err != nil {
		return 0, 0, fmt.Errorf("member id %q is not a decimal character id", fields[1])
	}
	return uint16(leader), uint16(member), nil
}

// writePartyForceResult 覆盖写结果文件（失败只记日志，不影响触发器本身）。
func (s *Service) writePartyForceResult(resultPath, line string) {
	if strings.TrimSpace(resultPath) == "" {
		return
	}
	if err := os.WriteFile(resultPath, []byte(line+"\n"), 0o644); err != nil {
		s.logPacketEvent("game-force-party-result-write-failed",
			"path", resultPath,
			"detail", err.Error())
	}
}

// forceManagedRuntimeParty 把 member 加入 leader 的队伍，效果与"客户端邀请 + 接受"一致。
// 返回值：(是否成功, party id, 失败原因, 成员数)。
func (s *Service) forceManagedRuntimeParty(leaderID, memberID uint16) (bool, int, string, int) {
	if leaderID == 0 || memberID == 0 {
		s.logPacketEvent("game-force-party-rejected",
			"reason", partyForceReasonZeroCharacterID,
			"leader_char_id", leaderID,
			"member_char_id", memberID)
		return false, 0, partyForceReasonZeroCharacterID, 0
	}
	if leaderID == memberID {
		s.logPacketEvent("game-force-party-rejected",
			"reason", partyForceReasonSameCharacter,
			"leader_char_id", leaderID,
			"member_char_id", memberID)
		return false, 0, partyForceReasonSameCharacter, 0
	}
	leader, online := s.onlineGameSession(leaderID)
	if !online {
		s.logPacketEvent("game-force-party-rejected",
			"reason", partyForceReasonLeaderNotOnline,
			"leader_char_id", leaderID,
			"member_char_id", memberID)
		return false, 0, partyForceReasonLeaderNotOnline, 0
	}
	if _, current := s.boundGameSessionCharacterSnapshot(leader); !current {
		s.logGameEvent(leader, "game-force-party-rejected",
			"reason", partyForceReasonLeaderNotAuthoritative,
			"leader_char_id", leaderID,
			"member_char_id", memberID,
			"character_generation", leader.characterGeneration)
		return false, 0, partyForceReasonLeaderNotAuthoritative, 0
	}
	member, online := s.onlineGameSession(memberID)
	if !online {
		s.logGameEvent(leader, "game-force-party-rejected",
			"reason", partyForceReasonMemberNotOnline,
			"leader_char_id", leaderID,
			"member_char_id", memberID)
		return false, 0, partyForceReasonMemberNotOnline, 0
	}
	if _, current := s.boundGameSessionCharacterSnapshot(member); !current {
		s.logGameEvent(member, "game-force-party-rejected",
			"reason", partyForceReasonMemberNotAuthoritative,
			"leader_char_id", leaderID,
			"member_char_id", memberID,
			"character_generation", member.characterGeneration)
		return false, 0, partyForceReasonMemberNotAuthoritative, 0
	}
	s.logGameEvent(member, "game-force-party-requested",
		"leader_char_id", leaderID,
		"member_char_id", memberID,
		"source", "party_force_command_file")

	// joiner = member / anchor = leader：与 op11 接受路径同一原语（anchor 拥有队伍即加入它，
	// 否则由 anchor 建队后加入），频道隔离/代际校验/旧队伍解绑全部原样生效。
	state, result, joined := s.createOrJoinManagedRuntimeParty(member, leader)
	if !joined {
		reason := partyForceReasonJoinFailedPrefix + result.Reason
		s.logGameEvent(member, "game-force-party-rejected",
			"reason", reason,
			"leader_char_id", leaderID,
			"member_char_id", memberID,
			"party_id", result.Party.ID)
		return false, 0, reason, 0
	}
	if err := s.sendManagedRuntimePartySnapshots(state); err != nil {
		reason := partyForceReasonSnapshotFailedPrefix + err.Error()
		s.logGameEvent(member, "game-force-party-rejected",
			"reason", reason,
			"leader_char_id", leaderID,
			"member_char_id", memberID,
			"party_id", state.PartyID)
		return false, state.PartyID, reason, len(state.Members)
	}
	// 与邀请路径同构的收尾：加入成功后再给**加入方**补一次权威名单（op153 + op9，不带 op11），
	// 因为本地 op12 投影可能让它以为自己仍是队长（见 party_online.go 的现场注释）。
	if manager := s.runtimePartyManagerForService(); manager != nil {
		if snapshot, found := manager.SnapshotByID(uint16(state.PartyID)); found {
			joinerState := snapshot.StateFor(memberID)
			if err := s.sendRuntimePartyRosterLocal(member, joinerState, "party_force_authoritative_roster"); err != nil {
				reason := partyForceReasonRosterFailedPrefix + err.Error()
				s.logGameEvent(member, "game-force-party-rejected",
					"reason", reason,
					"leader_char_id", leaderID,
					"member_char_id", memberID,
					"party_id", state.PartyID)
				return false, state.PartyID, reason, len(state.Members)
			}
		}
	}
	s.logGameEvent(member, "game-force-party-completed",
		"leader_char_id", leaderID,
		"member_char_id", memberID,
		"party_id", state.PartyID,
		"leader_id", state.UserID,
		"member_count", len(state.Members),
		"source", "party_force_command_file")
	return true, state.PartyID, "", len(state.Members)
}
