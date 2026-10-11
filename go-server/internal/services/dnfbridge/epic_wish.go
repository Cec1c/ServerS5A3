package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"

	dnfepicwish "longheng.io/server/internal/modules/dnf/epicwish"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

const currentEpicWishUpdateMsgID uint16 = 1352
const currentEpicWishInfoMsgID uint16 = 1288

// Official 24.9 ECE8D0 writes u8 count and count*u32 IDs. The five UI
// slots are collected at 12EA16D..12EA19F, including empty positions.
func decodeCurrentEpicWishUpdate(body []byte) (dnfepicwish.Slots, error) {
	var slots dnfepicwish.Slots
	if len(body) == 0 || int(body[0]) > len(slots) || len(body) != 1+4*int(body[0]) {
		return slots, dnfepicwish.ErrInvalid
	}
	for i := 0; i < int(body[0]); i++ {
		slots[i] = int32(binary.LittleEndian.Uint32(body[1+4*i:]))
	}
	return slots, nil
}

// Native NOTI1288 ECE5E0 resets its vector then reads u8 count/u32 IDs.
// Drop effect creation at 265CCFD..265CF21 consults this same vector via
// ECE590; restoring it enables the original wish effects without changing
// loot probabilities or adding unproved fields to NOTI38.
func buildCurrentEpicWishInfoBody(slots dnfepicwish.Slots) []byte {
	body := make([]byte, 1+4*len(slots))
	body[0] = byte(len(slots))
	for i, id := range slots {
		if id <= 0 {
			id = -1
		}
		binary.LittleEndian.PutUint32(body[1+4*i:], uint32(id))
	}
	return body
}

func (s *Service) sendCurrentEpicWishSnapshot(session *gameSession, source string) error {
	if s.options.currentClientVersion != "official-24.9" || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	var slots dnfepicwish.Slots
	repos, ok := s.repositoryGroup()
	if ok && repos.Character != nil {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
		if err != nil {
			return err
		}
		if found {
			if character.AccountID != s.accountIDForSession(session) {
				return dnfepicwish.ErrCharacter
			}
			slots = dnfepicwish.Read(character)
		}
	}
	if err := s.sendGameUpperRawClass(session, currentEpicWishInfoMsgID, buildCurrentEpicWishInfoBody(slots), 0); err != nil {
		return err
	}
	s.logGameEvent(session, "game-epic-wish-snapshot-sent", "source", source, "char_id", session.selectedCharacterID, "slot_count", len(slots))
	return nil
}

func (s *Service) handleCurrentEpicWishCommand(session *gameSession, msgID uint16, classification byte, body []byte) (bool, error) {
	if s.options.currentClientVersion != "official-24.9" || msgID != currentEpicWishUpdateMsgID {
		return false, nil
	}
	if classification != dnfproto.DefaultChannelClassification {
		return true, nil
	}
	reject := func(reason string) (bool, error) {
		s.logGameEvent(session, "game-epic-wish-update-rejected", "reason", reason, "body_len", len(body))
		return true, s.sendGameUpperRawClass(session, currentEpicWishUpdateMsgID, []byte{0}, 1)
	}
	slots, err := decodeCurrentEpicWishUpdate(body)
	if err != nil || session == nil || session.selectedCharacterID == 0 {
		return reject("request_invalid")
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return reject("repository_unavailable")
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return reject("pvf_unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	err = dnfepicwish.Save(ctx, repos, s.accountIDForSession(session), strconv.Itoa(int(session.selectedCharacterID)), slots, func(id int32) bool {
		item, resolveErr := catalog.ResolveItem(uint32(id))
		if resolveErr != nil || item.Kind != dungeonDropItemEquipment {
			return false
		}
		document, documentErr := parseDungeonCardPVFDocument(catalog.source, item.PVFPath)
		if documentErr != nil {
			return false
		}
		rarity := document.Ints("rarity")
		return len(rarity) == 1 && rarity[0] == hellPartyRewardRarityEpic && !currentDisjointDocumentIsAvatar(document)
	})
	if err != nil {
		return reject(fmt.Sprintf("save_failed: %v", err))
	}
	// ECE510 consumes the dispatch success byte only, no result payload.
	if err := s.sendGameUpperRawClass(session, currentEpicWishUpdateMsgID, []byte{1}, 1); err != nil {
		return true, err
	}
	s.logGameEvent(session, "game-epic-wish-update-committed", "char_id", session.selectedCharacterID)
	return true, s.sendCurrentEpicWishSnapshot(session, "wish_update_committed")
}
