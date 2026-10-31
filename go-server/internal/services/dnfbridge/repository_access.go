package dnfbridge

import (
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) repositoryGroup() (dnfrepo.Group, bool) {
	if s == nil || s.repositoryProvider == nil {
		return dnfrepo.Group{}, false
	}
	return s.repositoryProvider()
}

func (s *Service) accountID() string {
	if s != nil {
		if value := strings.TrimSpace(s.options.accountID); value != "" {
			return value
		}
		prefix := strings.TrimSpace(s.options.accountPrefix)
		if prefix == "" {
			prefix = defaultAccountPrefix
		}
		return prefix + "1"
	}
	return defaultAccountPrefix + "1"
}

// accountIDForSession 解析一条游戏会话使用的账号。
//
// 优先级（2026-09-25 操作者口径「不需要任何 IP 绑定」，现场取证见下）：
//
//	① session.accountID —— 客户端登录包里的启动器令牌解析出的账号
//	   （account_login_token.go），或本地 Windows 一键端按客户端进程号
//	   登记/确认过的账号（client_account_registry.go）。
//	② 共享兜底账号 s.accountID()（env DNFBRIDGE_ACCOUNT_ID；香港现为 dnf:1）。
//	   走到这一档意味着**这条会话没能识别出账号**：同一台机器上换任何账号登录
//	   都会看到同一个角色池。因此这一档必须留下 WARN 级日志
//	   （warnSharedAccountFallback），不能静默。
//
// 这里**不再**按来源 IP 猜账号。2026-09-25 香港取证（dnf_local.dnf90_web_accounts
// + /opt/dnf90/logs）：
//
//	玩家 sample-account-a/sample-account-b/sample-account-c/sample-account-d 四个账号的 last_login_ip 都是 192.0.2.14
//	（启动器走 HTTP 登录的出口），而同一台机器启动的游戏客户端 TCP 来源 IP 是
//	192.0.2.13 / 192.0.2.16 / 192.0.2.17 / 192.0.2.18 —— 它们是
//	加速器出口，还会在会话之间漂移。落到共享账号 dnf:1 的 9 条会话全部满足
//	client_ip≈192.0.2.13、account_from_ip=false、account_from_last_login_ip=false。
//	即：启动器登录 IP 与游戏连接来源 IP 根本不是同一个地址，按 IP 绑定在结构上
//	不可能正确，只会把"两个账号共用一个角色池"的后果放大。
func (s *Service) accountIDForSession(sessions ...*gameSession) string {
	if s.options.playerStrict && (len(sessions) == 0 || sessions[0] == nil || !sessions[0].accountFromToken) {
		return ""
	}
	if len(sessions) > 0 && sessions[0] != nil {
		session := sessions[0]
		if value := strings.TrimSpace(session.accountID); value != "" {
			return value
		}
		// 客户端已经交出登录包后账号就定案了：此时还没解析出账号，说明这条会话
		// 确实拿不到令牌（旧启动器/客户端没带/令牌查不到），必须告警。
		// 登录包还没来时先不判（GET_USERINFO 那条路径会兜住"永远不发登录包"的
		// 情形，见 buildUpperGetUserInfoRosterBody）。
		if session.loginPacketSeen {
			s.warnSharedAccountFallback(session)
		}
	}
	return s.accountID()
}

// sessionUsesSharedAccount 判断该会话当前是否会落到共享兜底账号。
// 供"玩家即将看到错误角色表"的关键路径（角色表构建）判读与告警使用。
func (s *Service) sessionUsesSharedAccount(session *gameSession) bool {
	if session == nil {
		return true
	}
	return strings.TrimSpace(session.accountID) == ""
}

// warnSharedAccountFallback 在会话没能识别出账号、即将使用共享兜底账号时写一条
// WARN。这是线上判读「不同账号登录看到同一个角色」的唯一入口，所以：
//   - 必须是 WARN（服务端日志，不受包日志缓冲影响）；
//   - 每条会话只写一次（sync.Once），不会刷日志；
//   - 字段必须能当场判死原因：token_found / login_packet_seen / client_pid /
//     client_ip / reason。
func (s *Service) warnSharedAccountFallback(session *gameSession) {
	if s == nil || session == nil {
		return
	}
	session.sharedAccountWarnOnce.Do(func() {
		accountID := s.accountID()
		reason := session.sharedAccountFallbackReason
		if reason == "" {
			// 没有更具体的失败点，就按"客户端到底有没有交过登录包"判：
			// 交过 ⇒ 包里没有令牌；没交过 ⇒ 令牌根本没机会被读到。
			reason = sharedAccountReasonNoLoginPacket
			if session.loginPacketSeen {
				reason = sharedAccountReasonNoLoginToken
			}
		}
		s.logWarn("dnfbridge game account fell back to shared account",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"client_pid", session.clientPID,
			"client_ip", session.clientIP,
			"account_id", accountID,
			"token_found", session.loginTokenSeen,
			"login_token_resolved", session.accountFromToken,
			"pid_binding_found", session.accountFromPID,
			"login_packet_seen", session.loginPacketSeen,
			"reason", reason,
			"impact", "every account reaching this connection shares one character pool")
		s.logPacketEvent("game-account-shared-fallback",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"client_pid", session.clientPID,
			"client_ip", session.clientIP,
			"account_id", accountID,
			"token_found", session.loginTokenSeen,
			"login_token_resolved", session.accountFromToken,
			"pid_binding_found", session.accountFromPID,
			"reason", reason)
	})
}

const (
	// sharedAccountReasonNoLoginToken：登录包里没有 32 位十六进制令牌
	// （旧启动器 / 客户端没把 TCLS 的 outer token 放进登录包 / 客户端根本没发登录包）。
	sharedAccountReasonNoLoginToken = "no_login_token_in_client_login_packet"
	// sharedAccountReasonTokenNotRecognised：登录包里有令牌，但令牌查不到账号
	// （旧启动器的固定兼容串、令牌过期、或令牌表里没有这一条）。
	sharedAccountReasonTokenNotRecognised = "login_token_not_recognised"
	// sharedAccountReasonNoLoginPacket：客户端始终没有发 class1/op1 登录包，
	// 因此令牌根本没机会被读到（现场 9/9 条落到 dnf:1 的会话都是这种）。
	sharedAccountReasonNoLoginPacket = "client_never_sent_login_packet"
)
