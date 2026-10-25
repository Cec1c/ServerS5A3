package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Reconnect projects a channel-specific landing before sending the actor. The
// next town-door request loads the character again, so persist the same landing
// under the aggregate transaction instead of leaving it in packet-only state.
func (s *Service) commitCurrentReconnectLocation(ctx context.Context, characterID uint16, projected dnfrepo.CharacterRecord) (dnfrepo.CharacterRecord, error) {
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		return dnfrepo.CharacterRecord{}, dnfrepo.ErrCharacterSettlementTransactionUnavailable
	}
	id := strconv.FormatUint(uint64(characterID), 10)
	current, found, err := repositories.Character.Load(ctx, id)
	if err != nil {
		return dnfrepo.CharacterRecord{}, err
	}
	if !found {
		return dnfrepo.CharacterRecord{}, fmt.Errorf("reconnect character %s missing", id)
	}
	unchanged := true
	for _, key := range []string{"town_id", "area_id", "pos_x", "pos_y", "direction", "area_state"} {
		value, exists := projected.Stats[key]
		old, saved := current.Stats[key]
		if !exists || !saved || value != old {
			unchanged = false
			break
		}
	}
	clearChannelTownOrigin := currentRegisteredChannelTown(current.Stats[currentTownPrevVillageTownStat]) &&
		!currentRegisteredChannelTown(projected.Stats["town_id"])
	clearReturnOrigin := !currentRegisteredChannelTown(projected.Stats["town_id"]) &&
		(current.Stats[currentRaidReturnOriginMarker] == 1 || current.Stats[currentPVPReturnOriginMarker] == 1)
	if unchanged && !clearChannelTownOrigin && !clearReturnOrigin {
		return current, nil
	}
	if repositories.CharacterSettlement == nil {
		return dnfrepo.CharacterRecord{}, dnfrepo.ErrCharacterSettlementTransactionUnavailable
	}
	var committed dnfrepo.CharacterRecord
	err = repositories.CharacterSettlement.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error {
		character, found, err := tx.Character.Load(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("reconnect character %s missing", id)
		}
		character = dnfrepo.CloneCharacter(character)
		if character.Stats == nil {
			character.Stats = make(map[string]int64)
		}
		changed := false
		// 非决斗专用频道保存普通来源，跨特殊频道不覆盖首次来源。
		if targetTown := projected.Stats["town_id"]; targetTown != 10 && currentRegisteredChannelTown(targetTown) && character.Stats["town_id"] != targetTown {
			if err := s.currentSaveRaidReturnOrigin(characterID, &character); err != nil {
				return err
			}
		}
		if projected.Stats["town_id"] == 10 && character.Stats["town_id"] != 10 {
			if err := s.currentSavePVPReturnOrigin(characterID, &character); err != nil {
				return err
			}
		}
		if !currentRegisteredChannelTown(projected.Stats["town_id"]) {
			if currentClearPVPReturnOrigin(character.Stats) {
				changed = true
			}
			if currentClearRaidReturnOrigin(character.Stats) {
				changed = true
			}
		}
		for _, key := range []string{"town_id", "area_id", "pos_x", "pos_y", "direction", "area_state"} {
			value, exists := projected.Stats[key]
			if !exists {
				return fmt.Errorf("reconnect location field %s missing", key)
			}
			if old, exists := character.Stats[key]; !exists || old != value {
				character.Stats[key] = value
				changed = true
			}
		}
		// 旧版本可能已保存决斗场为赛丽亚房间出口，随普通落点一起原子清除。
		if currentRegisteredChannelTown(character.Stats[currentTownPrevVillageTownStat]) &&
			!currentRegisteredChannelTown(projected.Stats["town_id"]) {
			for _, key := range []string{currentTownPrevVillageTownStat, currentTownPrevVillageAreaStat, currentTownPrevVillagePosXStat, currentTownPrevVillagePosYStat, currentTownPrevVillageDirectionStat} {
				if _, exists := character.Stats[key]; exists {
					delete(character.Stats, key)
					changed = true
				}
			}
		}
		if changed {
			character.UpdatedAt = s.gameplayNow()
			if err := dnfrepo.SaveCharacterFields(ctx, tx.Character, character, dnfrepo.CharacterFieldStats); err != nil {
				return err
			}
		}
		committed = character
		return nil
	})
	return committed, err
}
