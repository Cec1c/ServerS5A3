package dnfbridge

import (
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

type currentTagTournamentRosterCacheKey struct {
	account string
	pid     uint32
	server  byte
	flow    [32]byte
}
type currentTagTournamentRosterCacheEntry struct {
	rows   []repo.CharacterRecord
	sentAt time.Time
}

// Remote clients have no local PID. Reuse the authenticated channel-flow
// identity already established by LOGIN, never an account/IP-only fallback.
// A token-authenticated client must not borrow a recycled PID's old roster.
func currentTagTournamentSentRosterCacheKey(p *gameSession, server byte) (currentTagTournamentRosterCacheKey, bool) {
	if p == nil || !p.loginPacketSeen || p.accountID == "" || p.loginRevoked.Load() {
		return currentTagTournamentRosterCacheKey{}, false
	}
	if identity, ok := currentChannelHandoffKey(p); ok {
		return currentTagTournamentRosterCacheKey{account: identity.account, server: server, flow: identity.flow}, true
	}
	if p.accountFromToken || p.loginChannelFlowKey != [32]byte{} || p.clientPID == 0 {
		return currentTagTournamentRosterCacheKey{}, false
	}
	return currentTagTournamentRosterCacheKey{account: p.accountID, pid: p.clientPID, server: server}, true
}

// 切频道会新建TCP会话，客户端仍沿用原op2紧凑顺序。以同账号、已认证登录
// 流程和服务器继承实际成功下发的表；本地无令牌流程保留原PID隔离。
// 不能读当前SQL列表重新排序，也不能借另一次登录的表。
func (s *Service) rememberCurrentTagTournamentSentRoster(p *gameSession, rows []repo.CharacterRecord) {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return
	}
	server := s.channelServerID()
	if server <= 0 || server > 255 {
		return
	}
	key, ok := currentTagTournamentSentRosterCacheKey(p, byte(server))
	if !ok {
		return
	}
	valid := len(rows) <= 255
	minimal := make([]repo.CharacterRecord, len(rows))
	for i, ch := range rows {
		if ch.AccountID != p.accountID || ch.CharacterID == "" || ch.Name == "" || ch.Slot < 0 {
			valid = false
			break
		}
		minimal[i] = repo.CharacterRecord{CharacterID: ch.CharacterID, AccountID: ch.AccountID, Slot: ch.Slot, Name: ch.Name, Job: ch.Job, Level: ch.Level}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(rows) == 0 || !valid {
		delete(s.tagTournamentSentRosters, key)
		return
	}
	if s.tagTournamentSentRosters == nil {
		s.tagTournamentSentRosters = map[currentTagTournamentRosterCacheKey]currentTagTournamentRosterCacheEntry{}
	}
	if _, exists := s.tagTournamentSentRosters[key]; !exists && len(s.tagTournamentSentRosters) >= 128 {
		var oldest currentTagTournamentRosterCacheKey
		var stamp time.Time
		for k, v := range s.tagTournamentSentRosters {
			if stamp.IsZero() || v.sentAt.Before(stamp) {
				oldest, stamp = k, v.sentAt
			}
		}
		delete(s.tagTournamentSentRosters, oldest)
	}
	s.tagTournamentSentRosters[key] = currentTagTournamentRosterCacheEntry{minimal, time.Now()}
}

func (s *Service) cachedCurrentTagTournamentSentRoster(p *gameSession, server byte) []repo.CharacterRecord {
	key, ok := currentTagTournamentSentRosterCacheKey(p, server)
	if s == nil || !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.tagTournamentSentRosters[key]
	return append([]repo.CharacterRecord(nil), entry.rows...)
}
