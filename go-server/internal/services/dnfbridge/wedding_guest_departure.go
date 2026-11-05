package dnfbridge

import (
	"context"
	"strconv"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

// Called before the ordinary login owner starts its own transaction. The
// caller may hold town/selection assets; this path never takes wedding.mu,
// dungeon.mu or another asset reservation and never nests repository UOWs.
func (s *Service) recoverCurrentWeddingGuestLogin(ctx context.Context, session *gameSession, characterID uint16, expected r.CharacterRecord) (r.CharacterRecord, bool, error) {
	if expected.Stats["wedding_v1_guest_key"] == 0 || expected.Stats["town_id"] != 39 {
		return expected, false, nil
	}
	area, found := s.townArea(39, expected.Stats["area_id"])
	if !found || !townMapPathLooksLikeWeddingChurch(area.MapPath) {
		return expected, false, nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.character != characterID || expected.CharacterID != strconv.Itoa(int(characterID)) || expected.AccountID != s.accountIDForSession(session) {
		return r.CharacterRecord{}, false, wedding.ErrOwnership
	}
	owner, err := s.currentWeddingOwner()
	if err != nil {
		return r.CharacterRecord{}, false, err
	}
	current, moved, err := owner.RecoverGuestChurchDeparture(ctx, wedding.Actor{AccountID: expected.AccountID, CharacterID: expected.CharacterID}, expected, s.gameplayNow(), s.currentWeddingCeremonyReturn, func() bool { return s.boundGameSessionCharacterCurrent(identity) })
	if err != nil {
		return r.CharacterRecord{}, false, err
	}
	if moved {
		// Persisted origins must not bind the old private Church to a Seria
		// exit after this durable departure. Valid ordinary origins are kept.
		for _, snapshot := range []*currentTownPositionSnapshot{&session.townPrevVillageSnapshot, &session.townSeriaRoomEntrySnapshot} {
			if snapshot.CharacterID == characterID {
				if old, ok := s.townArea(int64(snapshot.TownID), int64(snapshot.AreaID)); ok && townMapPathLooksLikeWeddingChurch(old.MapPath) {
					*snapshot = currentTownPositionSnapshot{}
				}
			}
		}
		s.logGameEvent(session, "game-wedding-guest-login-departure", "character_id", characterID, "town_id", current.Stats["town_id"], "area_id", current.Stats["area_id"])
	}
	return current, moved, nil
}
