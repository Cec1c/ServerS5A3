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
	if unchanged {
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
