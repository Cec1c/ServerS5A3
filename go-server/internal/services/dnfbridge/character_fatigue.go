package dnfbridge

import (
	"context"
	"fmt"
	dnffatigue "longheng.io/server/internal/modules/dnf/fatigue"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"strconv"
	"time"
)

func (s *Service) updateCurrentCharacterFatigue(ctx context.Context, session *gameSession, debit bool) (dnfrepo.CharacterRecord, error) {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return dnfrepo.CharacterRecord{}, fmt.Errorf("fatigue character repository unavailable")
	}
	account := dnfrepo.AccountRecord{}
	if repos.Account != nil {
		var err error
		account, _, err = repos.Account.Load(ctx, s.accountIDForSession(session))
		if err != nil {
			return dnfrepo.CharacterRecord{}, err
		}
	}
	key := strconv.Itoa(int(session.selectedCharacterID))
	var result dnfrepo.CharacterRecord
	apply := func(characters dnfrepo.CharacterRepository) error {
		character, found, err := characters.Load(ctx, key)
		if err != nil {
			return err
		}
		if !found {
			return errDungeonCharacterNotFound
		}
		if character.AccountID != "" && character.AccountID != s.accountIDForSession(session) {
			return errDungeonAccountMismatch
		}
		character = dnfrepo.CloneCharacter(character)
		changed := dnffatigue.Refresh(&character, account, s.gameplayNow())
		if debit {
			dnffatigue.Debit(&character)
			changed = true
		}
		if changed {
			if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
				return err
			}
		}
		result = character
		return nil
	}
	var err error
	if repos.CharacterAssets != nil {
		err = repos.CharacterAssets.WithinCharacterAssets(ctx, key, func(characters dnfrepo.CharacterRepository, _ dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
			return apply(characters)
		})
	} else {
		err = apply(repos.Character)
	}
	return result, err
}

func (s *Service) armCurrentFatigueDailyRefresh(session *gameSession) {
	characterID, generation, err := gameSessionCharacterEventIdentity(session, session.selectedCharacterID)
	if err != nil {
		return
	}
	now := s.gameplayNow()
	local := now.In(time.FixedZone("UTC+8", 8*3600))
	next := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, local.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	name := fmt.Sprintf("character-fatigue-day:%s", session.connID)
	err = s.ensureGameplayTimeQueue().ScheduleAfter(name, next.Sub(now), func(time.Time) {
		if session.selectedCharacterID != characterID || session.characterGeneration != generation {
			return
		}
		if err := s.postGameSessionCharacterEvent(session, "character-fatigue-day", characterID, generation, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			character, err := s.updateCurrentCharacterFatigue(ctx, session, false)
			if err != nil {
				return err
			}
			session.dungeon.mu.Lock()
			if session.dungeon.runtime != nil {
				session.dungeon.runtime.Character = character
			}
			session.dungeon.mu.Unlock()
			s.armCurrentFatigueDailyRefresh(session)
			if err := s.sendGameUpperRawClass(session, currentFatigueMsgID, buildCurrentFatigueBody(character, true), 0); err != nil {
				return err
			}
			return s.sendCurrentFatiguePotionUsageForCharacter(ctx, session, character, "six_am_refresh_after_fatigue")
		}); err != nil {
			s.logGameEvent(session, "game-character-fatigue-daily-refresh-deferred", "error", err)
		}
	})
	if err != nil {
		s.logGameEvent(session, "game-character-fatigue-timer-deferred", "error", err)
	}
}

// Coordinates own fatigue: revisits and layered maps in the same room do not
// charge again. Each member owns a separate balance in the shared instance.
func (s *Service) debitDungeonRoomFatigue(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene) error {
	if runtime.fatigueRooms == nil {
		runtime.fatigueRooms = make(map[worldmap.RoomCoordinate]bool)
	}
	if runtime.fatigueRooms[scene.Coordinate] {
		return nil
	}
	if currentDungeonUsesNoFatigue(runtime.Dungeon) {
		runtime.fatigueRooms[scene.Coordinate] = true
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	character, err := s.updateCurrentCharacterFatigue(ctx, session, true)
	if err != nil {
		return fmt.Errorf("debit room fatigue: %w", err)
	}
	runtime.Character = character
	runtime.fatigueRooms[scene.Coordinate] = true
	s.logGameEvent(session, "game-dungeon-fatigue-room-debited", "char_id", session.selectedCharacterID, "room", scene.Coordinate.String(), "remaining", character.Stats["fatigue"], "limit", character.Stats["fatigue_limit"])
	return nil
}
