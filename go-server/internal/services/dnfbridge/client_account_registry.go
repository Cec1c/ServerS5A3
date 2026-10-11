package dnfbridge

import (
	"errors"
	"strings"
	"sync"
	"time"
)

const clientAccountRegistrationTTL = 24 * time.Hour

type clientAccountRegistration struct {
	accountID string
	// registeredAt 是启动器（或令牌确认）写入这条绑定的时刻。
	registeredAt time.Time
	expiresAt    time.Time
	// tokenAccountID 是该 PID **被启动器令牌证明过**的账号。非空时它就是这条
	// PID 的权威账号：启动器登记值只有在与它一致时才被采信（见
	// confirmClientAccountPID 与 registeredClientAccountForPID 的注释）。
	tokenAccountID string
	// tokenConfirmedAt 是最后一次令牌确认的时刻（用于把"同一进程生命周期内
	// 的重复登记"与"新进程生命周期（含 OS PID 复用）"区分开）。
	tokenConfirmedAt             time.Time
	spendTimeEventInfoAuthorized bool
	spendTimeEventInfoSendState  uint8
}

const (
	spendTimeEventInfoUnsent uint8 = iota
	spendTimeEventInfoSending
	spendTimeEventInfoSent
)

// clientAccountRegistry 保存"客户端进程号 → 账号"的登记。
//
// 这条通道只在 Windows 本机一键端可用：客户端进程号由 TCP 连接所属进程解析
// （tcp_owner_windows.go），Linux/公网部署恒不可用（tcp_owner_other.go）。
// 因此它不是香港（Linux）上"不同账号看到同一批角色"的原因，但它本身也有一条
// 会串账号的路径，已按下面两条规则收口：
//
//  1. **令牌优先且自愈**：某条会话用启动器令牌解析出账号后，该账号成为这个 PID
//     的权威账号（tokenAccountID），并覆盖启动器登记值。PID 复用或一次丢失的
//     启动器登记留下的陈旧账号，会在下一次令牌到达时被永久纠正。
//  2. **只在没有令牌时才用登记值**：accountIDForSession 的优先级是
//     令牌 → PID 登记 → 共享兜底；一旦会话拿到了令牌，登记值不再参与判定。
//
// 注意：本结构里**没有**任何按来源 IP 的绑定 —— 那条通道已在 2026-09-25 删除，
// 理由见 client_account_ip.go 与 repository_access.go。
type clientAccountRegistry struct {
	mu    sync.Mutex
	byPID map[uint32]clientAccountRegistration
}

func newClientAccountRegistry() clientAccountRegistry {
	return clientAccountRegistry{
		byPID: make(map[uint32]clientAccountRegistration),
	}
}

// RegisterClientAccount binds one launcher-authenticated local DNF process to
// its repository account. The loopback authenticated admin endpoint is the
// only production caller; no password or session secret enters the bridge.
func (s *Service) RegisterClientAccount(pid uint32, accountID string) error {
	if s == nil {
		return errors.New("dnf bridge is unavailable")
	}
	accountID = strings.TrimSpace(accountID)
	if pid == 0 {
		return errors.New("client PID must be positive")
	}
	if accountID == "" {
		return errors.New("client account ID is required")
	}
	now := time.Now().UTC()
	s.clientAccounts.mu.Lock()
	defer s.clientAccounts.mu.Unlock()
	if s.clientAccounts.byPID == nil {
		s.clientAccounts.byPID = make(map[uint32]clientAccountRegistration)
	}
	for registeredPID, entry := range s.clientAccounts.byPID {
		if !entry.expiresAt.After(now) {
			delete(s.clientAccounts.byPID, registeredPID)
		}
	}
	entry, found := s.clientAccounts.byPID[pid]
	if found && entry.expiresAt.After(now) && strings.TrimSpace(entry.accountID) == accountID &&
		entry.spendTimeEventInfoAuthorized &&
		entry.spendTimeEventInfoSendState == spendTimeEventInfoUnsent {
		// A transport retry before the new process reaches op108 is idempotent.
		// Once sending has begun, a later launcher registration is a new process
		// lifecycle (including OS PID reuse) and must receive a fresh gate.
		entry.expiresAt = now.Add(clientAccountRegistrationTTL)
		s.clientAccounts.byPID[pid] = entry
		return nil
	}
	// 新进程生命周期（含 OS PID 复用）：连同令牌证明一起作废，避免上一个进程
	// 的权威账号被套到新进程上。
	s.clientAccounts.byPID[pid] = clientAccountRegistration{
		accountID:                    accountID,
		registeredAt:                 now,
		expiresAt:                    now.Add(clientAccountRegistrationTTL),
		spendTimeEventInfoAuthorized: true,
	}
	return nil
}

// registeredClientAccount 返回该 PID 当前可用的账号登记（令牌证明优先）。
func (s *Service) registeredClientAccount(pid uint32) (string, bool) {
	accountID, _, found := s.registeredClientAccountForPID(pid)
	return accountID, found
}

// registeredClientAccountForPID 返回该 PID 当前可用的账号与登记时刻。
// 令牌证明过的账号优先于启动器登记值。
func (s *Service) registeredClientAccountForPID(pid uint32) (string, time.Time, bool) {
	if s == nil || pid == 0 {
		return "", time.Time{}, false
	}
	now := time.Now().UTC()
	s.clientAccounts.mu.Lock()
	defer s.clientAccounts.mu.Unlock()
	entry, found := s.clientAccounts.byPID[pid]
	if !found {
		return "", time.Time{}, false
	}
	if !entry.expiresAt.After(now) {
		delete(s.clientAccounts.byPID, pid)
		return "", time.Time{}, false
	}
	accountID := strings.TrimSpace(entry.accountID)
	registeredAt := entry.registeredAt
	if proven := strings.TrimSpace(entry.tokenAccountID); proven != "" {
		accountID = proven
		if entry.tokenConfirmedAt.After(registeredAt) {
			registeredAt = entry.tokenConfirmedAt
		}
	}
	if accountID == "" {
		return "", time.Time{}, false
	}
	return accountID, registeredAt, true
}

// confirmClientAccountPID 用启动器令牌证明的账号纠正该 PID 的登记。
// 返回 true 表示这次确认**改变了**该 PID 原先的账号（即发现了一条陈旧/串号的
// 登记），调用方据此写 WARN。
func (s *Service) confirmClientAccountPID(pid uint32, accountID string) bool {
	if s == nil || pid == 0 {
		return false
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return false
	}
	now := time.Now().UTC()
	s.clientAccounts.mu.Lock()
	defer s.clientAccounts.mu.Unlock()
	if s.clientAccounts.byPID == nil {
		s.clientAccounts.byPID = make(map[uint32]clientAccountRegistration)
	}
	entry, found := s.clientAccounts.byPID[pid]
	if !found || !entry.expiresAt.After(now) {
		entry = clientAccountRegistration{
			expiresAt:                    now.Add(clientAccountRegistrationTTL),
			spendTimeEventInfoAuthorized: false,
		}
	}
	previous := strings.TrimSpace(entry.accountID)
	changed := previous != "" && previous != accountID
	entry.accountID = accountID
	entry.tokenAccountID = accountID
	entry.tokenConfirmedAt = now
	entry.registeredAt = now
	s.clientAccounts.byPID[pid] = entry
	return changed
}

// beginCurrentSpendTimeEventInfo deduplicates initial op108 traffic by registered
// client process. Its in-flight state preserves descriptor/progress ordering.
// Complete catalogs are also valid native replacements: a changed service day
// is handled independently by sendCurrentSpendTimeProtocolState.
//
// PID=0（当前 90CN 部署的实际情况）不再是"永不发送"：那种写法会让活动目录
// 一个会话都发不出去，客户端活动表为空并在进城时开出未初始化的韩文
// BLUE/RED 竞技窗（2026-09-26 现场弹窗）。官方对照是每个会话一个 op108，
// 所以 PID=0 时按会话去重。
func (s *Service) beginCurrentSpendTimeEventInfo(session *gameSession) (send bool, ready bool) {
	if s == nil || session == nil {
		return false, false
	}
	if session.clientPID == 0 {
		// 2026-09-26 现场证据：当前 90CN 部署里游戏会话拿不到客户端 PID
		// （dnf90.out 的 login token bound 与 packet_log 的
		// game-spend-time-* 都是 client_pid=0）。旧写法在这里直接把
		// eventInfoSent 置真并返回"不发"，于是**每一个**会话都跳过 op108
		// 活动目录：客户端活动表为空，进城时 sub_F2D0A0 走"活动不可用"
		// 分支，开出未初始化的韩文 BLUE/RED 竞技窗（现场弹窗即此）。
		//
		// 官方服务端的对照组：runtime/logs/packet_log.txt 里 5 个会话发了
		// 5 个逐字节相同的 op108（msg_id=108，body_codec=
		// current_op108_spend_time_and_all_day_joust_catalog_zlib），
		// 即"每个会话发一次"，与客户端进程身份无关。
		//
		// 因此 PID 不可用时按**会话**去重：同一会话只发一次（重连不重发），
		// 新会话重新发一次。finishCurrentSpendTimeEventInfo 在 sent=true 时
		// 已经会置 session.spendTime.eventInfoSent，这里只需读它。
		if session.spendTime.eventInfoSent {
			return false, true
		}
		return true, true
	}
	now := time.Now().UTC()
	s.clientAccounts.mu.Lock()
	defer s.clientAccounts.mu.Unlock()
	if s.clientAccounts.byPID == nil {
		s.clientAccounts.byPID = make(map[uint32]clientAccountRegistration)
	}
	entry, found := s.clientAccounts.byPID[session.clientPID]
	if !found || !entry.expiresAt.After(now) {
		entry = clientAccountRegistration{expiresAt: now.Add(clientAccountRegistrationTTL)}
	}
	if !entry.spendTimeEventInfoAuthorized {
		// Preserve initial-traffic deduplication for an unregistered local PID.
		// This is a registry policy, not a restriction of the op108 grammar;
		// the service-day owner may still send a complete replacement catalog.
		session.spendTime.eventInfoSent = true
		s.clientAccounts.byPID[session.clientPID] = entry
		return false, true
	}
	switch entry.spendTimeEventInfoSendState {
	case spendTimeEventInfoSent:
		session.spendTime.eventInfoSent = true
		s.clientAccounts.byPID[session.clientPID] = entry
		return false, true
	case spendTimeEventInfoSending:
		s.clientAccounts.byPID[session.clientPID] = entry
		return false, false
	default:
		entry.spendTimeEventInfoSendState = spendTimeEventInfoSending
		s.clientAccounts.byPID[session.clientPID] = entry
		return true, true
	}
}

// finishCurrentSpendTimeEventInfo commits or releases a reservation. sent=true
// must be used as soon as the op108 socket write succeeds, even if the
// following op1206 write fails, so retries do not duplicate initial traffic.
func (s *Service) finishCurrentSpendTimeEventInfo(session *gameSession, sent bool) {
	if s == nil || session == nil {
		return
	}
	if sent {
		session.spendTime.eventInfoSent = true
	}
	if session.clientPID == 0 {
		return
	}
	now := time.Now().UTC()
	s.clientAccounts.mu.Lock()
	defer s.clientAccounts.mu.Unlock()
	if s.clientAccounts.byPID == nil {
		s.clientAccounts.byPID = make(map[uint32]clientAccountRegistration)
	}
	entry, found := s.clientAccounts.byPID[session.clientPID]
	if !found || !entry.expiresAt.After(now) {
		entry = clientAccountRegistration{expiresAt: now.Add(clientAccountRegistrationTTL)}
	}
	if sent {
		entry.spendTimeEventInfoSendState = spendTimeEventInfoSent
	} else if entry.spendTimeEventInfoSendState == spendTimeEventInfoSending {
		entry.spendTimeEventInfoSendState = spendTimeEventInfoUnsent
	}
	s.clientAccounts.byPID[session.clientPID] = entry
}
