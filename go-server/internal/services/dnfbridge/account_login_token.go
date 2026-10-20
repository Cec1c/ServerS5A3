package dnfbridge

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 启动器账号令牌（launcher login token）。
//
// 背景：远程部署下服务端无法从游戏连接本身识别账号——游戏连接没有任何账号字段，
// 频道连接开头那 32 字节前言又是客户端自己生成的随机数（同一次启动内稳定，与账号
// 无关）。此前只能按来源 IP 猜账号，同一 NAT 下多个客户端、或服务端重启清空内存
// 绑定时就会把玩家放到别的账号上（表现为"角色全没了"）。
//
// 做法：启动器登录账号 API 时服务端签发一条令牌，启动器把它写进 TCLS 启动串的
// outer token 字段；当前 EXE 会把该字符串原样放进 class1/op1 登录包（实测
// body+6 起 32 个 ASCII 十六进制字符）。服务端据此把这条游戏连接直接绑定到登录的
// 账号，不再依赖 IP，令牌同时落库，重启后依然有效。
//
// 令牌格式：固定 8 位兼容前缀 + 24 位随机十六进制。前缀必须保持 de509f65 不变，
// 因为客户端会校验服务端回显的该 uint32（buildLoginSuccess/传输帧头都在用它）。
//
// 没有令牌（旧启动器/客户端没带/令牌查不到）时**不再按来源 IP 猜账号**：那条
// 通道已在 2026-09-25 删除（见 client_account_ip.go）。此时会话直接落到共享
// 兜底账号（env DNFBRIDGE_ACCOUNT_ID），并由 repository_access.go 的
// warnSharedAccountFallback 写一条 WARN 记录原因。

const (
	loginTokenChars        = 32
	loginTokenPrefix       = "de509f65"
	loginTokenRandomBytes  = (loginTokenChars - len(loginTokenPrefix)) / 2
	loginTokenTTL          = 7 * 24 * time.Hour
	loginTokenCacheTTL     = 10 * time.Minute
	loginTokenMissCacheTTL = 30 * time.Second

	// 实测布局：body+0 标记, body+1 容量 0x30, body+2 u32 长度 32, body+6 起 32 字符。
	loginTokenBodyOffset = 6
	// 结构不符时只在登录包开头做一次有界扫描，避免在大 body 里乱找。
	loginTokenScanLimit = 64
)

type accountLoginTokenEntry struct {
	accountID string
	expiresAt time.Time
}

// accountLoginTokenStore 缓存令牌到账号的映射，让游戏连接上的解析不查库。
type accountLoginTokenStore struct {
	mu     sync.Mutex
	tokens map[string]accountLoginTokenEntry
	misses map[string]time.Time
}

func (r *accountLoginTokenStore) remember(token string, accountID string, expiresAt time.Time) {
	if r == nil || token == "" || accountID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tokens == nil {
		r.tokens = make(map[string]accountLoginTokenEntry)
	}
	delete(r.misses, token)
	r.tokens[token] = accountLoginTokenEntry{accountID: accountID, expiresAt: expiresAt}
}

func (r *accountLoginTokenStore) recall(token string, now time.Time) (string, bool) {
	if r == nil || token == "" {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.tokens[token]
	if !ok {
		return "", false
	}
	if !now.Before(entry.expiresAt) {
		delete(r.tokens, token)
		return "", false
	}
	return entry.accountID, true
}

// recentlyMissed 避免旧启动器的固定令牌每来一条连接就查一次库。
func (r *accountLoginTokenStore) recentlyMissed(token string, now time.Time) bool {
	if r == nil || token == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.misses[token]
	return ok && now.Sub(at) < loginTokenMissCacheTTL
}

func (r *accountLoginTokenStore) rememberMiss(token string, now time.Time) {
	if r == nil || token == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.misses == nil {
		r.misses = make(map[string]time.Time)
	}
	if len(r.misses) > 4096 {
		for key, at := range r.misses {
			if now.Sub(at) >= loginTokenMissCacheTTL {
				delete(r.misses, key)
			}
		}
	}
	r.misses[token] = now
}

// newLoginToken 生成一条带兼容前缀的启动器令牌。
func newLoginToken() (string, error) {
	buf := make([]byte, loginTokenRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return loginTokenPrefix + hex.EncodeToString(buf), nil
}

// normalizeLoginToken 校验并归一化令牌：必须正好 32 个十六进制字符。
func normalizeLoginToken(value string) string {
	value = strings.TrimSpace(value)
	if len(value) != loginTokenChars {
		return ""
	}
	out := make([]byte, loginTokenChars)
	for i := 0; i < loginTokenChars; i++ {
		switch c := value[i]; {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
			out[i] = c
		case c >= 'A' && c <= 'F':
			out[i] = c + ('a' - 'A')
		default:
			return ""
		}
	}
	return string(out)
}

// clientLoginTokenFromBody 读取当前 EXE class1/op1 登录包里的启动器令牌。
func clientLoginTokenFromBody(body []byte) string {
	if len(body) < loginTokenBodyOffset+loginTokenChars {
		return ""
	}
	if binary.LittleEndian.Uint32(body[2:6]) == loginTokenChars {
		if token := normalizeLoginToken(string(body[loginTokenBodyOffset : loginTokenBodyOffset+loginTokenChars])); token != "" {
			return token
		}
	}
	limit := min(len(body)-loginTokenChars, loginTokenScanLimit)
	for offset := 0; offset <= limit; offset++ {
		if token := normalizeLoginToken(string(body[offset : offset+loginTokenChars])); token != "" {
			return token
		}
	}
	return ""
}

// accountIDForLoginToken 解析令牌所属账号：先内存缓存，再查库并回填缓存。
func (s *Service) accountIDForLoginToken(token string, ip string) string {
	if s == nil {
		return ""
	}
	token = normalizeLoginToken(token)
	if token == "" {
		return ""
	}
	now := time.Now()
	if accountID, ok := s.loginTokens.recall(token, now); ok {
		return accountID
	}
	if s.loginTokens.recentlyMissed(token, now) {
		return ""
	}
	accountID := s.lookupLoginTokenInDB(token, ip, now)
	if accountID == "" {
		s.loginTokens.rememberMiss(token, now)
		return ""
	}
	s.loginTokens.remember(token, accountID, now.Add(loginTokenCacheTTL))
	return accountID
}

// lookupLoginTokenInDB 查库解析令牌，并刷新令牌自身的使用记录。
//
// 曾经这里还会用"游戏连接观察到的 IP"回写 dnf90_web_accounts.last_login_ip，
// 因为那时账号判定会按该字段做"最近登录 IP → 账号"兜底。按来源 IP 判定账号的
// 整条通道已于 2026-09-25 删除（见 client_account_ip.go），这个回写随之删除：
// 它会用游戏连接的加速器出口 IP 覆盖启动器真实登录 IP，反而污染
// dnf90_web_accounts 里"这个账号最后一次从哪里登录"的审计事实。
// 令牌表自己的 last_used_at/last_used_ip/use_count 保留 —— 它们是
// "令牌到底有没有被客户端交上来"的唯一现场证据。
func (s *Service) lookupLoginTokenInDB(token string, ip string, now time.Time) string {
	api := s.accountAPI
	if api == nil || api.db == nil {
		return ""
	}
	table := api.loginTokenTable()
	if table == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), accountAPIWriteTimeout)
	defer cancel()

	var accountID string
	query := "SELECT account_id FROM " + table + " WHERE token = ? AND expires_at > ?"
	if err := api.db.QueryRowContext(ctx, query, token, now).Scan(&accountID); err != nil {
		return ""
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return ""
	}
	ip = normalizeClientIP(ip)
	update := "UPDATE " + table + " SET last_used_at = ?, last_used_ip = ?, use_count = use_count + 1 WHERE token = ?"
	if _, err := api.db.ExecContext(ctx, update, now, nullableClientIP(ip), token); err != nil {
		s.logWarn("dnfbridge login token touch failed", "error", err)
	}
	return accountID
}

func nullableClientIP(ip string) any {
	if ip == "" {
		return nil
	}
	return ip
}

// bindGameSessionAccountFromLoginToken 用登录包里的启动器令牌把这条游戏连接绑定到账号。
// 返回 true 表示本次认到令牌（无论是否成功绑定到账号）。
//
// 令牌是账号的**唯一权威来源**：解析出来的账号无条件覆盖会话上已有的任何值
// （包括"客户端进程号 → 账号"的本地登记），并顺手把该 PID 的登记纠正成这个账号，
// 让 PID 复用/登记丢失留下的陈旧账号自愈。失败时只记录原因，不改动会话账号：
// 调用方最终会落到共享兜底账号，并由 warnSharedAccountFallback 写 WARN。
func (s *Service) bindGameSessionAccountFromLoginToken(session *gameSession, classification byte, body []byte) bool {
	if s == nil || session == nil || classification != dnfproto.DefaultChannelClassification {
		return false
	}
	if session.accountFromToken {
		return false
	}
	token := clientLoginTokenFromBody(body)
	if token == "" {
		return false
	}
	session.loginTokenSeen = true
	if session.clientIP == "" {
		session.clientIP = remoteIPOf(session.conn)
	}
	accountID := s.accountIDForLoginToken(token, session.clientIP)
	if accountID == "" {
		session.sharedAccountFallbackReason = sharedAccountReasonTokenNotRecognised
		s.logPacketEvent("game-login-token-unbound",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"client_ip", session.clientIP,
			"token_present", true,
			"session_account_bound", session.accountID != "")
		// 令牌存在但查不到账号是"账号串号"的决定性失败点，用 WARN 而不是 INFO：
		// 出现它就说明这条连接拿不到权威账号 —— 除非本地 PID 登记兜住，否则
		// 一定会落到共享兜底账号（那不是写死在这里，而是照常往下走的结果）。
		s.logWarn("dnfbridge login token not recognised",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"client_ip", session.clientIP,
			"client_pid", session.clientPID,
			"token_present", true,
			"session_account_id", session.accountID,
			"session_account_bound", session.accountID != "",
			"reason", sharedAccountReasonTokenNotRecognised)
		return false
	}
	if err := s.admitAccountForEdits(session, accountID); err != nil {
		s.logWarn("dnfbridge account admission fence failed", "conn_id", session.connID, "error", err)
		return false
	}
	s.lockCurrentCharacterBindingMutation(session, 0)
	previous := strings.TrimSpace(session.accountID)
	session.accountID = accountID
	session.accountFromToken = true
	session.sharedAccountFallbackReason = ""
	s.mu.Unlock()
	if session.clientPID != 0 {
		if changed := s.confirmClientAccountPID(session.clientPID, accountID); changed {
			s.logWarn("dnfbridge login token overrode the client PID account binding",
				"conn_id", session.connID,
				"client_pid", session.clientPID,
				"previous_account_id", previous,
				"account_id", accountID,
				"reason", "stale_pid_binding_corrected_by_login_token")
		}
	}
	s.logPacketEvent("game-login-token-account-bound",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"client_ip", session.clientIP,
		"account_id", accountID,
		"previous_account_id", previous,
		"account_changed", previous != "" && previous != accountID)
	// 包日志是缓冲写（且现场排查时可能没开），账号绑定属于关键事件，
	// 同时写一条不受缓冲影响的服务端日志。
	s.logInfo("dnfbridge login token bound game session",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"client_ip", session.clientIP,
		"account_id", accountID,
		"previous_account_id", previous,
		"account_changed", previous != "" && previous != accountID)
	// 账号此刻定案：顺手补齐这个账号的账号级记录，让本次会话后续所有账号级读取
	// （角色表/冒险团/账号金库/晶体契约）都从第一次请求起就看得见一个真实账号。
	// 已存在时只读；缺失时按服务端自己的建档口径补一行并写 WARN；失败不改变
	// 本次绑定结果。见 account_record_bootstrap.go。
	bootstrapCtx, bootstrapCancel := context.WithTimeout(context.Background(), createWriteTimeout)
	s.ensureAccountRecordsForSelection(bootstrapCtx, session, accountRecordBootstrapLoginSource)
	bootstrapCancel()
	return true
}
