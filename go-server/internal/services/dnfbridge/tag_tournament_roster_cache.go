package dnfbridge

import (
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

type currentTagTournamentRosterCacheKey struct {
	account string
	pid     uint32
	server  byte
}
type currentTagTournamentRosterCacheEntry struct {
	rows   []repo.CharacterRecord
	sentAt time.Time
}

// 切频道会新建TCP会话，客户端仍沿用原op2紧凑顺序。仅按同一账号、进程号和
// 服务器继承实际成功下发的表；不能读当前SQL列表重新排序，也不能借另一窗口的表。
func (s *Service) rememberCurrentTagTournamentSentRoster(p *gameSession, rows []repo.CharacterRecord) {
	if s == nil || s.options.currentClientVersion != "official-24.9" || p == nil || p.clientPID == 0 || p.accountID == "" || !p.loginPacketSeen {
		return
	}
	server := s.channelServerID()
	if server <= 0 || server > 255 {
		return
	}
	key := currentTagTournamentRosterCacheKey{p.accountID, p.clientPID, byte(server)}
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
	if p.clientPID == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.tagTournamentSentRosters[currentTagTournamentRosterCacheKey{p.accountID, p.clientPID, server}]
	return append([]repo.CharacterRecord(nil), entry.rows...)
}
