package dnfbridge

import (
	"time"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 调用方持本会话dungeon锁，且本人的op42回城已成功写出。只接受当前临时队长；
// 不锁其他会话，由既有逐会话维护重试回城，避免双人同时撤退互锁。
func (s *Service) beginCurrentFiendWarPartyRetreatLocked(p *gameSession, r *runtimeDungeonState, now time.Time) error {
	if !isCurrentFiendWarChannel(p) || !s.currentRaidAuthenticated(p) || r == nil || p.dungeon.runtime != r || r.raidEntry == nil || r.Session == nil || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || !r.townReturnOp24Sent {
		return nil
	}
	party := s.runtimePartyStateForSession(p)
	if party.PartyID <= 0 || party.UserID != p.selectedCharacterID || len(runtimePartyMembers(party)) < 2 || r.partyInstance == nil || int(r.partyInstance.key.PartyID) != party.PartyID {
		return nil
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	e := r.raidEntry
	state := s.raids[e.RaidKey]
	if state == nil || !state.Started || state.Battle == nil || state.Battle.FiendWar == nil || state.Battle.InstanceID != e.InstanceID || state.Battle.ChannelID != p.channel.ID || state.Battle.ChannelType != p.channel.Type || state.Battle.TimedOut || state.Battle.PhaseCompleted || state.Battle.MemberAccounts[p.selectedCharacterID] != s.accountIDForSession(p) {
		return nil
	}
	b := state.Battle
	if err := validateCurrentFiendWarDungeon(b, p.selectedCharacterID, r); err != nil {
		return err
	}
	if b.FiendWarRetreatRuns[e.RunID] {
		r.fiendWarRetreatKeepsParty = true
		return nil
	}
	next := b.FiendWar.Clone()
	if err := next.Abandon(int(r.Dungeon.ID), now); err != nil {
		return err
	}
	if b.FiendWarRetreatRuns == nil {
		b.FiendWarRetreatRuns = map[string]bool{}
	}
	// 罗什任一侧撤退会按PVF让另一侧也退出；只标记本次已存在的遭遇战。
	// 保留遭遇战占位至最后一位成员确认回城，防止旧回城释放新进图。
	for area, encounter := range b.FiendWarEncounters {
		if b.FiendWar.Areas[area].Group != 0 && next.Areas[area].Group == 0 {
			b.FiendWarRetreatRuns[encounter.RunID] = true
		}
	}
	b.FiendWar = next
	b.ProgressVersion++
	r.fiendWarRetreatKeepsParty = true
	syncCurrentFiendWarDungeons(b)
	return nil
}
