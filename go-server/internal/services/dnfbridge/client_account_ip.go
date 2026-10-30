package dnfbridge

import (
	"net"
	"strings"
)

// 本文件历史上承载"按来源 IP 把游戏连接绑定到启动器账号"这条通道：
// RegisterClientAccountIP / resolveClientAccountIP / peekClientAccountIP /
// activeAccountsForIP / acquireActiveAccountIP / releaseActiveAccountIP /
// ClientAccountBindings / accountIDForLastLoginIP（后者在 repository_access.go）
// 及其内存表 byIP / lastForIP / activeByIP 与 60s 缓存。
//
// 2026-09-25 现场取证后**整条通道已删除**，理由见 repository_access.go 的
// accountIDForSession 注释：启动器 HTTP 登录的出口 IP（dnf90_web_accounts.
// last_login_ip，玩家四个账号都是 192.0.2.14）与游戏客户端 TCP 连接的来源 IP
// （加速器出口 192.0.2.13 / 192.0.2.16 / 192.0.2.17 / 192.0.2.18，
// 会话之间还会漂移）不是同一个地址。按 IP 猜账号在结构上不可能正确，并且会把
// "两个账号共用一个角色池"的后果放大。账号的唯一权威来源是启动器令牌
// （account_login_token.go）；本地 Windows 一键端另有按客户端进程号的登记
// （client_account_registry.go）。
//
// 这里只保留与账号判定无关、其它链路（账号 API 审计/限流、连接日志、组队中继）
// 仍在使用的纯工具函数。

// normalizeClientIP 去掉端口，并把 IPv4-mapped IPv6 归一成 IPv4。
func normalizeClientIP(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	if ip := net.ParseIP(value); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
		return ip.String()
	}
	return ""
}

// remoteIPOf 从连接上取来源 IP（已归一）。只用于日志与中继，不参与账号判定。
func remoteIPOf(conn net.Conn) string {
	if conn == nil || conn.RemoteAddr() == nil {
		return ""
	}
	return normalizeClientIP(conn.RemoteAddr().String())
}
