package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// 角色列表登录落点的区域门槛开关。**默认 relaxed = 门控前的行为**。
//
// 为什么需要它：登录落点解析（currentCharacterListLoginTransition 的三级落点）从来不校验
// 目标区域的 PVF 门槛，而运行时 PVF 的 town 文档确实声明了 `[need level]` / `[need quest]`
// （dnftown.Area.MinLevel ← `[need level]`、dnftown.Area.NeedQuests ← `[need quest]`，
// 见 internal/modules/dnf/town/parse.go:43-49 与 types.go:29-44）。实测例：赫顿玛尔
// （town 39 / area 0）声明 `[need level] 17` + `[need quest] 3156`，如果把一个 Lv11 的角色
// 落进那里，客户端就进了一个它本来就进不去的区域——走门进图那条链路（op36）是拒绝的，
// 登录落点却是放行的，两条链路口径不一致。
//
// 唯一的既有实现是 op36 的 town_move.go:137（等级 → 原因 target_area_min_level_not_met）
// 与 town_move.go:146-161（任务 → target_area_need_quests...），任务判定复用同一个
// s.townAreaNeedQuestsCompleted（town_move.go:450-478），所以这里语义完全一致。
// 判定顺序（先等级、后任务）与 C# 参考实现
// `Server/DfoServer/GameWorld/TownAreaPermissionPolicy.cs` 一致；其中 `[exceptional character]`
// 在参考实现里是**故意不执行**的，这里同样不执行；数据读不到时两边都**放行**（fail-open）。
//
//	DNFBRIDGE_LOGIN_AREA_GATES
//	  ""/"relaxed"（默认）：登录落点完全不评估区域门槛 —— 无评估、无日志，字节与门控前一致。
//	  "log"/"dry-run"：评估两条规则；**被拒时**只打 game-login-area-gate-denied，落点不变，
//	    用于先上线观察"线上到底有多少角色会撞门槛"再决定是否开 strict。
//	  "strict"/"enforce"/"pvf"：评估两条规则；被拒时把落点换成既有的第三级
//	    38/1 艾尔文防线赛丽亚房间（currentInitialTownSeriaLoginLocation，不再二次评估）。
//	    重定向对**所有来源**的落点生效：不管是持久化精确落点（DNFBRIDGE_LOGIN_PERSISTED_TOWN=keep
//	    打开的旧口径）、该城镇自己的赛丽亚房间，还是原本就要回落的 38/1。
//
// 两个日志事件的语义必须与名字一致：**只有真有拒绝（拒绝原因非空）才打
// game-login-area-gate-denied**；评估跑了但通过（拒绝原因为空）打
// game-login-area-gate-evaluated（denied=false）。绝不能出现 reason 为空的 -denied 事件 ——
// 线上就是靠"有没有 -denied、reason 是什么"来判断门控结果的。
// 注意 currentLoginAreaGateDenyReason 的第二个返回值是 evaluated 而不是 denied，
// 调用方必须用**拒绝原因非空**判断是否被拒（见该函数注释）。

// 为什么默认 relaxed：登录是与"重新进游戏"绑定的**不可重试**路径（客户端一次登录失败没有
// 自动重试），把一条线上从未跑过的规则默认打开，风险是"谁都进不去"。所以先 relaxed/log 观察，
// strict 由操作者显式打开。
const currentLoginAreaGatesEnv = "DNFBRIDGE_LOGIN_AREA_GATES"

const (
	// currentLoginAreaGatesRelaxed：默认值，等于门控前的行为（不评估）。
	currentLoginAreaGatesRelaxed = "relaxed"
	// currentLoginAreaGatesLog：只评估并记录，绝不改落点。
	currentLoginAreaGatesLog = "log"
	// currentLoginAreaGatesStrict：评估并把被拒的落点重定向到 38/1 赛丽亚房间。
	currentLoginAreaGatesStrict = "strict"

	// currentLoginAreaGateFallback 是 game-login-area-gate-denied 事件里 fallback 字段的取值：
	// strict 重定向到 38/1 赛丽亚房间时写它，log（只记录）时该字段为空串。
	currentLoginAreaGateFallback = "elvengard_seria"
)

// currentLoginAreaGatesMode 解析开关取值（大小写/空白无关）：
// strict/enforce/pvf → strict；log/dry-run → log；其余（含空值）→ relaxed。
// 与 town_teleport.go:97-104 的 currentTeleportPotionAreaGatesMode 同一写法。
func currentLoginAreaGatesMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(currentLoginAreaGatesEnv))) {
	case currentLoginAreaGatesStrict, "enforce", "pvf":
		return currentLoginAreaGatesStrict
	case currentLoginAreaGatesLog, "dry-run":
		return currentLoginAreaGatesLog
	default:
		return currentLoginAreaGatesRelaxed
	}
}

// loginAreaGateLevelDenyReason 是 `[need level]` 这条规则的**纯函数**判定，便于表驱动测试。
//
// 与 op36 同域：只有 area.MinLevel > 0（PVF 真声明了门槛）且角色等级低于它才算被拒；
// MinLevel=0/缺失表示"该区域没有等级门槛"（运行时 PVF 里绝大多数区域都是这样），
// 绝不能把 0 当成"要求 0 级"以外的东西。
// 返回值第一个是稳定的、可 grep 的拒绝原因，第二个是"是否被拒"。
func loginAreaGateLevelDenyReason(area dnftown.Area, characterLevel int64) (string, bool) {
	if area.MinLevel > 0 && characterLevel < area.MinLevel {
		return fmt.Sprintf("level=%d<%d", characterLevel, area.MinLevel), true
	}
	return "", false
}

// currentLoginAreaGateDenyReason 评估一个**已解析的登录落点**的两条 PVF 区域门槛规则。
//
// 返回 (denyReason, evaluated)：
//   - evaluated=false 表示这个 (town, area) 在运行时 PVF 里查不到区域（无法评估），调用方放行；
//   - evaluated=true 且 denyReason=="" 表示两条规则都通过；
//   - denyReason 形如 `level=11<17`（等级）或 `quest=3156_not_cleared`（任务），两者都稳定可 grep。
//
// **第二个返回值是 evaluated，不是 denied**：它在所有路径上都是 true —— 等级不够、没有任务
// 门槛、任务全过、任务仓储读失败（fail-open）都返回 true。**调用方判断"是否被拒"只能用
// 拒绝原因是否非空**（denyReason != ""）。曾经在登录门控块里把 evaluated 当 denied 用，
// 结果是 strict 模式下"评估通过"的落点也被换成了 38/1 艾尔文防线赛丽亚房间（线上实测）。
//
// 顺序与 op36 / C# 参考实现一致：**先等级、后任务**（等级不够时根本不必查任务）。
//
// 任务判定复用 op36 的 s.townAreaNeedQuestsCompleted，所以"完成"的语义（QuestRecord 的
// Progress/States + townQuestStateCompleted）与走门进图逐字节相同。
//
// **任务侧 fail-open**：任务仓储缺失或读失败时只打
// game-login-area-gate-quest-check-failed 并放行。理由与 op36 不同——op36 拒绝后客户端
// 还在城里、可以再走一次门；而登录是"重新进游戏"这条不可重试的路径，一次误拒就是
// 操作者进不去游戏。宁可放行一个可能不满足门槛的落点，也不让登录失败。
func (s *Service) currentLoginAreaGateDenyReason(
	ctx context.Context,
	session *gameSession,
	repositories dnfrepo.Group,
	characterID uint16,
	townID int64,
	area dnftown.Area,
	characterLevel int64,
) (string, bool) {
	if reason, denied := loginAreaGateLevelDenyReason(area, characterLevel); denied {
		return reason, true
	}
	if len(area.NeedQuests) == 0 {
		return "", true
	}
	recordCharacterID := fmt.Sprintf("%d", characterID)
	needQuestsCompleted, err := s.townAreaNeedQuestsCompleted(ctx, repositories, recordCharacterID, area.NeedQuests)
	if err != nil {
		s.logGameEvent(session, "game-login-area-gate-quest-check-failed",
			"char_id", characterID,
			"town_id", townID,
			"area_id", area.ID,
			"need_quests", fmt.Sprint(area.NeedQuests),
			"error", err)
		return "", true
	}
	if !needQuestsCompleted {
		return fmt.Sprintf("quest=%d_not_cleared", firstLoginAreaGateNeedQuest(area.NeedQuests)), true
	}
	return "", true
}

// firstLoginAreaGateNeedQuest 取出拒绝原因里要报的那个任务号：PVF 可以一次声明多个
// `[need quest]`，只报第一个未完成的——但 townAreaNeedQuestsCompleted 只回答"全过了吗"，
// 不回答"哪个没过"，所以这里取声明里的第一个正数。多个任务的区域在运行时 PVF 里很少，
// 而日志里同时还有 need_quests 全量清单可以对照。
func firstLoginAreaGateNeedQuest(needQuests []int64) int64 {
	for _, questID := range needQuests {
		if questID > 0 {
			return questID
		}
	}
	return 0
}
