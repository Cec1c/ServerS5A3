package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfguardianshield "longheng.io/server/internal/modules/dnf/guardianshield"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentGuardianShieldDeckNotificationMsgID uint16 = 580

func buildCurrentGuardianShieldDeckNotificationBody(deck dnfguardianshield.Deck) []byte {
	body := make([]byte, dnfguardianshield.DeckSlotCount*4)
	for index, itemID := range deck {
		binary.LittleEndian.PutUint32(body[index*4:index*4+4], itemID)
	}
	return body
}

// sendSelectedGuardianShieldDeckInfo publishes the current EXE's independent
// class0/op580 five-dword deck state. It is sent only after an actor generation
// is bound; it is not appended to any mode0/mode1/userinfo body.
func (s *Service) sendSelectedGuardianShieldDeckInfo(session *gameSession, source string) (bool, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return false, nil
	}
	if session.initialTownGuardianShieldDeckSent {
		return false, nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		return false, fmt.Errorf("guardian shield projection character repository is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		return false, fmt.Errorf("load guardian shield projection character: %w", err)
	}
	if !found {
		return false, fmt.Errorf("guardian shield projection character %s not found", characterID)
	}
	if accountID := strings.TrimSpace(s.accountIDForSession(session)); accountID != "" &&
		strings.TrimSpace(character.AccountID) != accountID {
		return false, fmt.Errorf("guardian shield projection character owner mismatch")
	}
	growType, err := dnfguardianshield.EligibleGrowType(character)
	if errors.Is(err, dnfguardianshield.ErrCharacterIneligible) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	deck, err := dnfguardianshield.LoadDeck(character)
	if err != nil {
		return false, err
	}

	var resolver alignedcmd.GuardianShieldResolver
	if deck != (dnfguardianshield.Deck{}) {
		resolver, err = s.newCurrentGuardianShieldResolver(ctx)
		if err != nil {
			return false, err
		}
	}

	var questRecord dnfrepo.QuestRecord
	questLoaded := false
	seen := make(map[uint32]int, dnfguardianshield.DeckSlotCount)
	for index, itemID := range deck {
		if itemID == 0 {
			continue
		}
		if previous, duplicate := seen[itemID]; duplicate {
			return false, fmt.Errorf("guardian shield projection duplicate item=%d slots=%d,%d", itemID, previous, index)
		}
		seen[itemID] = index
		resolution, resolveErr := resolver(ctx, itemID, growType)
		if resolveErr != nil {
			return false, fmt.Errorf("validate guardian shield projection slot=%d item=%d: %w", index, itemID, resolveErr)
		}
		if resolution.ItemID != itemID {
			return false, fmt.Errorf("validate guardian shield projection slot=%d item=%d resolved=%d", index, itemID, resolution.ItemID)
		}
		if resolution.UnlockKind == alignedcmd.GuardianShieldUnlockQuest && !questLoaded {
			if repositories.Quest == nil {
				return false, dnfguardianshield.ErrQuestRepositoryRequired
			}
			questRecord, found, err = repositories.Quest.Load(ctx, characterID)
			if err != nil {
				return false, fmt.Errorf("load guardian shield projection clear quests: %w", err)
			}
			if !found {
				questRecord = dnfrepo.QuestRecord{CharacterID: characterID}
			}
			if strings.TrimSpace(questRecord.CharacterID) != characterID {
				return false, fmt.Errorf("guardian shield projection quest owner mismatch")
			}
			questLoaded = true
		}
		if err := dnfguardianshield.ValidateUnlock(character, questRecord, resolution); err != nil {
			return false, fmt.Errorf("validate guardian shield projection slot=%d item=%d unlock: %w", index, itemID, err)
		}
	}
	body := buildCurrentGuardianShieldDeckNotificationBody(deck)
	s.logPacketEvent("game-upper-selected-guardian-shield-deck-send",
		"conn_id", session.connID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"msg_id", currentGuardianShieldDeckNotificationMsgID,
		"classification", 0,
		"grow_type", growType,
		"body_len", len(body),
		"body_source", "current_exe_sub_2034ca0_five_u32_deck")
	if err := s.sendGameUpperRawClass(session, currentGuardianShieldDeckNotificationMsgID, body, 0); err != nil {
		return false, err
	}
	session.initialTownGuardianShieldDeckSent = true
	return true, nil
}

// sendSelectedGuardianShieldTownAppearanceRefreshOnce rehydrates the visible
// main shield after a town actor's equipment-bearing mode1 and op580 deck have
// been accepted. Slot 24 is outside the ordinary fourteen-row mode0 appearance
// table. The current client therefore needs its complete op342 equipment group
// followed by op357's same-state redraw. The per-generation gate makes a
// transport retry resume safely without replaying an already accepted redraw.
func (s *Service) sendSelectedGuardianShieldTownAppearanceRefreshOnce(session *gameSession, source string) (bool, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return false, nil
	}
	if session.initialTownGuardianShieldAppearanceSent {
		return false, nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Equipment == nil {
		return false, fmt.Errorf("guardian shield appearance equipment repository is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	record, found, err := repositories.Equipment.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil {
		return false, fmt.Errorf("load guardian shield appearance equipment: %w", err)
	}
	if !found {
		record = dnfrepo.EquipmentRecord{CharacterID: strconv.Itoa(int(session.selectedCharacterID))}
	}
	itemID, equipped := dnfguardianshield.MainProjectionItemID(record)
	if !equipped {
		session.initialTownGuardianShieldAppearanceSent = true
		return false, nil
	}
	if err := s.sendSelectedActorExpandedEquipmentRefresh(session, source+"_op342"); err != nil {
		return false, err
	}
	if err := s.sendSelectedActorUserStateRefresh(session, source+"_op357_after_op342"); err != nil {
		return false, err
	}
	session.initialTownGuardianShieldAppearanceSent = true
	s.logPacketEvent("game-upper-selected-guardian-shield-town-appearance-refreshed",
		"conn_id", session.connID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"actor_slot", dnfguardianshield.ActorSlot,
		"item_id", itemID,
		"sequence", "equipment_mode1_op580_op342_op357")
	return true, nil
}

// 选角只以角色盾栏为真源，通过领域事务恢复主盾投影；不从装备行授予盾牌。
func (s *Service) preSyncGuardianShieldMainProjection(ctx context.Context, session *gameSession, character dnfrepo.CharacterRecord, hasCharacter bool) error {
	if s == nil || session == nil || !hasCharacter {
		return nil
	}
	if _, err := dnfguardianshield.EligibleGrowType(character); errors.Is(err, dnfguardianshield.ErrCharacterIneligible) {
		return nil
	} else if err != nil {
		return err
	}
	if strings.TrimSpace(character.CharacterID) != strconv.Itoa(int(session.selectedCharacterID)) {
		return fmt.Errorf("盾牌投影角色不一致")
	}
	if accountID := strings.TrimSpace(s.accountIDForSession(session)); accountID != "" && accountID != strings.TrimSpace(character.AccountID) {
		return fmt.Errorf("盾牌投影账号不一致")
	}
	repositories, ok := s.repositoryGroup()
	if !ok {
		return nil
	}
	deck, err := dnfguardianshield.LoadDeck(character)
	if err != nil {
		return err
	}
	resolver, err := s.newCurrentGuardianShieldResolver(ctx)
	if err != nil {
		return err
	}
	owner, err := dnfguardianshield.NewOwner(repositories, resolver)
	if err != nil {
		return err
	}
	_, err = owner.SaveDeck(ctx, session.selectedCharacterID, s.accountIDForSession(session), deck)
	return err
}
