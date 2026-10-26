package dnfbridge

import (
	"context"
	"errors"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 调用方持有townMu。在重连首次1375/23/24之前，沿用普通登录已校验的
// 持久化房门来源；只恢复房间的出口上下文，不改变房内落点或玩家资产。
func (s *Service) restoreCurrentReconnectSeriaOriginLocked(ctx context.Context, session *gameSession, characterID uint16, character dnfrepo.CharacterRecord, mapPath string) {
	session.townPrevVillageSnapshot = currentTownPositionSnapshot{}
	session.townSeriaRoomEntrySnapshot = currentTownPositionSnapshot{}
	if !townMapPathLooksLikeSeria(mapPath) || isPVPChannel(session.channel) {
		return
	}
	if _, raid := currentRaidLoginTownID(session); raid {
		return
	}
	origin, found := currentTownPrevVillageOriginFromPersistedStats(characterID, character)
	if !found || s.currentDedicatedChannelTown(int64(origin.TownID)) {
		return
	}
	originMap, persistable := s.currentTownPrevVillageOriginIsPersistable(origin)
	if !persistable {
		return
	}
	if townMapPathLooksLikeGuildHall(originMap) {
		if _, err := s.currentTownAreaInstance(ctx, session, character, originMap); err != nil {
			if !errors.Is(err, dnfguild.ErrNotMember) {
				s.logGameEvent(session, "重连房门来源校验失败", "char_id", characterID, "error", err)
			}
			return
		}
	}
	session.townPrevVillageSnapshot = origin
	if currentLoginSeriaEntryTownEnabled() {
		session.townSeriaRoomEntrySnapshot = origin
	}
	s.logTownPrevVillageOriginBound(session, origin, "频道重连恢复已保存的普通城镇房门来源")
}
