package dnfbridge

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// The paired native 2254F80 sender exports exactly 0xBA bytes. A GUI A/B save
// proves awakening costume is u16 word74, not character-option word126 (full
// avatar visibility). Native 22C61D6/20072E0 map this option inversely to
// user_state_bits bit4; full-avatar visibility is the distinct bit3.
func currentAwakeningAvatarOption(blob []byte) (uint16, bool) {
	if len(blob) != 186 || blob[149] != 0 || blob[148] > 1 {
		return 0, false
	}
	return uint16(blob[148]), true
}

const currentAwakeningAvatarUserStateMask int64 = 0x10

func awakeningAvatarOwnerCurrent(session *gameSession, characterID uint16, generation uint64) bool {
	return session != nil &&
		session.selectedCharacterID == characterID &&
		session.characterGeneration == generation
}

func applyCurrentAwakeningAvatarOptionToCharacter(
	ctx context.Context,
	characters repo.CharacterRepository,
	characterID string,
	accountID string,
	value uint16,
) (bool, error) {
	if characters == nil || value > 1 {
		return false, fmt.Errorf("awakening option owner unavailable")
	}
	character, found, err := characters.Load(ctx, characterID)
	if err != nil {
		return false, err
	}
	if !found || strings.TrimSpace(character.AccountID) != accountID {
		return false, fmt.Errorf("awakening option owner mismatch")
	}
	bits := int64(3)
	if character.Stats != nil {
		if saved, exists := character.Stats["user_state_bits"]; exists {
			bits = saved
		}
	}
	if bits < 0 || bits > 0xff {
		return false, fmt.Errorf("awakening option invalid state")
	}
	next := bits &^ currentAwakeningAvatarUserStateMask
	if value == 0 {
		next |= currentAwakeningAvatarUserStateMask
	}
	if next == bits {
		return false, nil
	}
	character = repo.CloneCharacter(character)
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	character.Stats["user_state_bits"] = next
	if err := repo.SaveCharacterFields(ctx, characters, character, repo.CharacterFieldStats); err != nil {
		return false, err
	}
	return true, nil
}

// saveCurrentAwakeningAvatarGameOption persists op197 and its derived actor bit
// in one transaction. CharacterSettlement deliberately hides account settings,
// so use the existing account+character+settings boundary without invoking any
// shop operation. The caller sends op357 only after this commits.
func (s *Service) saveCurrentAwakeningAvatarGameOption(
	session *gameSession,
	accountID string,
	blob []byte,
	value uint16,
) (bool, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || value > 1 {
		return false, fmt.Errorf("awakening option owner unavailable")
	}
	characterID := session.selectedCharacterID
	generation := session.characterGeneration
	characterKey := strconv.Itoa(int(characterID))
	repos, ok := s.repositoryGroup()
	if !ok || repos.CeraShopAssets == nil || repos.Settings == nil {
		return false, fmt.Errorf("awakening option transaction unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()

	changed := false
	err := repos.CeraShopAssets.WithinCeraShopAssets(
		ctx, accountID, characterKey, accountGameOptionScope(accountID),
		func(_ repo.AccountRepository, characters repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, settings repo.SettingsRepository) error {
			if !awakeningAvatarOwnerCurrent(session, characterID, generation) ||
				strings.TrimSpace(s.accountIDForSession(session)) != accountID {
				return fmt.Errorf("awakening option owner changed")
			}
			if settings == nil {
				return errSettingsRepositoryUnavailable
			}
			var err error
			changed, err = applyCurrentAwakeningAvatarOptionToCharacter(
				ctx, characters, characterKey, accountID, value)
			if err != nil {
				return err
			}
			return s.saveSettingsValuesUsingRepository(
				ctx,
				settings,
				session,
				accountGameOptionScope(accountID),
				map[string]string{
					"main_game_option_hex": hex.EncodeToString(blob),
					"main_game_option_len": strconv.Itoa(len(blob)),
					"source":               "current_exe_op197",
				},
			)
		},
	)
	if err != nil {
		return false, err
	}
	return changed, nil
}

// syncCurrentAwakeningAvatarOption is independent of the one-shot account
// notice and DNFBRIDGE_GAME_OPTION_NOTI. Every selected character entry and
// switch applies the persisted option before actor construction.
func (s *Service) syncCurrentAwakeningAvatarOption(session *gameSession, characterID uint16) (bool, error) {
	if s == nil || session == nil || characterID == 0 ||
		s.options.currentClientVersion != "official-24.9" ||
		session.selectedCharacterID != characterID {
		return false, nil
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		return false, nil
	}
	generation := session.characterGeneration
	characterKey := strconv.Itoa(int(characterID))
	repos, ok := s.repositoryGroup()
	if !ok || repos.CeraShopAssets == nil || repos.Settings == nil {
		return false, fmt.Errorf("awakening option transaction unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()

	changed := false
	err := repos.CeraShopAssets.WithinCeraShopAssets(
		ctx, accountID, characterKey, accountGameOptionScope(accountID),
		func(_ repo.AccountRepository, characters repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, settings repo.SettingsRepository) error {
			if !awakeningAvatarOwnerCurrent(session, characterID, generation) ||
				strings.TrimSpace(s.accountIDForSession(session)) != accountID {
				return fmt.Errorf("awakening option owner changed")
			}
			if settings == nil {
				return errSettingsRepositoryUnavailable
			}
			record, found, err := settings.Load(ctx, accountGameOptionScope(accountID))
			if err != nil || !found {
				return err
			}
			value, known := currentAwakeningAvatarOption(decodeSettingsHex(record.Values["main_game_option_hex"]))
			if !known {
				return nil
			}
			changed, err = applyCurrentAwakeningAvatarOptionToCharacter(
				ctx, characters, characterKey, accountID, value)
			return err
		},
	)
	if err != nil {
		return false, err
	}
	return changed, nil
}
