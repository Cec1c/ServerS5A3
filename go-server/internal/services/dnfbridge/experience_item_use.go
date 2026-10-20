package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfexperienceitem "longheng.io/server/internal/modules/dnf/experienceitem"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrevivecoin "longheng.io/server/internal/modules/dnf/revivecoin"
)

const (
	// Current NoPack registers ENUM_CMDPACKET_INCREASE_STATUS as id 30 (the
	// generated name table in dnfenum/cmdpacket.go). It is the character
	// progression consumable command: the request carries only the source main
	// inventory slot, and the absolute level/EXP/SP/TP snapshot follows as the
	// established class0/op37 character state.
	currentExperienceItemUseBodySize = 2
	// The legacy transport appends its four-byte trailer to some commands. The
	// semantic body stays the leading two bytes, exactly like the op44
	// normalizer, so accept both boundaries and reject anything else.
	currentExperienceItemUseBodySizeWithLegacyTrailer = 6

	// ServerS4A21 IncreaseStatusAckBuilder / InventoryHandler.Experience error
	// codes. The success body itself is built by successExperienceItemBody.
	currentExperienceItemUseMissingSourceCode = 0x11
	currentExperienceItemUseUnknownCode       = 0x01

	// DNFBRIDGE_EXPERIENCE_ITEM_USE gates the whole route. `off` (and the usual
	// false spellings) is the explicit rollback to the pre-change behaviour: the
	// op30 request is ignored, exactly as it was when the command had no route.
	// Every other value, including unset, serves the fixed route.
	currentExperienceItemUseEnv = "DNFBRIDGE_EXPERIENCE_ITEM_USE"
)

func currentExperienceItemUseDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(currentExperienceItemUseEnv))) {
	case "off", "0", "false", "no", "disabled":
		return true
	default:
		return false
	}
}

// successExperienceItemBody is the 11-byte payload that sendGameUpperSuccess
// prefixes with its success byte, i.e. the 12-byte class1/op30 success record:
//
//	+0x00 [u8]  0x01 success (written by sendGameUpperSuccess)
//	+0x01 [u16] target user id
//	+0x03 [u8]  0xFF no status change (the absolute EXP/SP state travels on op37)
//	+0x04 [u32] 0
//	+0x08 [u16] 0
//	+0x0A [u16] 0
//
// The record deliberately reports "no status change": ordinary EXP and the
// SP/TP ledger are published by the following class0/op37 character state.
func successExperienceItemBody(userID uint16) []byte {
	var writer packetWriter
	writer.writeUint16(userID)
	writer.writeByte(0xff)
	writer.writeUint32(0)
	writer.writeUint16(0)
	writer.writeUint16(0)
	return writer.bytes()
}

// handleCurrentExperienceItemUse consumes one SP/TP skill book through
// ENUM_CMDPACKET_INCREASE_STATUS (op30).
//
// Root cause of the operator report "SP书无法使用": op44 (use stackable)
// requires a PVF-proven `[waste]` row and every skill book is `[etc]`, so the
// book was rejected before any effect, while op30 had no server route at all.
// Recognition is PVF-derived (see resolveCurrentSkillBook); the decrement, the
// persisted bonus SP/TP, and the PVF-based ledger recomputation commit inside
// one character-settlement transaction (modules/dnf/experienceitem).
func (s *Service) handleCurrentExperienceItemUse(session *gameSession, body []byte) error {
	if currentExperienceItemUseDisabled() {
		return nil
	}
	opcode := uint16(dnfenum.CmdPacketIncreaseStatus)
	if session == nil || session.selectedCharacterID == 0 {
		// No session means there is nothing to answer; the gameplay route never
		// dispatches without one.
		return errors.New("increase-status request has no selected character")
	}
	if len(body) != currentExperienceItemUseBodySize && len(body) != currentExperienceItemUseBodySizeWithLegacyTrailer {
		s.logGameEvent(session, "game-increase-status-malformed",
			"msg_id", opcode,
			"body_len", len(body),
			"expected_body_len", currentExperienceItemUseBodySize)
		return s.sendGameUpperFailure(session, opcode, currentExperienceItemUseUnknownCode)
	}
	slotIndex := int16(binary.LittleEndian.Uint16(body[0:2]))
	if slotIndex < 0 {
		s.logGameEvent(session, "game-increase-status-malformed",
			"msg_id", opcode,
			"slot_index", slotIndex,
			"reason", "negative_source_slot")
		return s.sendGameUpperFailure(session, opcode, currentExperienceItemUseUnknownCode)
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Inventory == nil || repositories.Character == nil {
		s.logGameEvent(session, "game-increase-status-blocked",
			"msg_id", opcode,
			"slot_index", slotIndex,
			"reason", "repositories_unavailable")
		return s.sendGameUpperFailure(session, opcode, currentExperienceItemUseUnknownCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(session.selectedCharacterID))

	inventory, found, err := repositories.Inventory.Load(ctx, characterID)
	if err != nil {
		return fmt.Errorf("load increase-status inventory: %w", err)
	}
	if !found {
		return s.answerRejectedExperienceItem(session, slotIndex, 0, "inventory_missing", nil)
	}
	stack, stackFound := inventory.Slots[experienceItemMainSlotKey(slotIndex)]
	if !stackFound || stack.Count <= 0 || stack.ItemID <= 0 {
		return s.answerRejectedExperienceItem(session, slotIndex, 0, "source_slot_empty", nil)
	}
	if stack.ItemID == dnfrevivecoin.ConsumableItemID {
		return s.handleCurrentReviveCoinItemUse(ctx, repositories, session, slotIndex)
	}

	book, err := s.resolveCurrentSkillBook(stack.ItemID)
	if err != nil {
		return s.answerRejectedExperienceItem(session, slotIndex, stack.ItemID, "not_a_skill_book", err)
	}

	character, characterFound, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		return fmt.Errorf("load increase-status character: %w", err)
	}
	if !characterFound {
		return s.answerRejectedExperienceItem(session, slotIndex, stack.ItemID, "character_missing", nil)
	}
	if strings.TrimSpace(character.AccountID) != strings.TrimSpace(s.accountIDForSession(session)) {
		return s.answerRejectedExperienceItem(session, slotIndex, stack.ItemID, "character_owner_mismatch", nil)
	}
	pvfPoints, err := s.initialSkillPoints(ctx, character.Level)
	if err != nil {
		return s.answerRejectedExperienceItem(session, slotIndex, stack.ItemID, "pvf_skill_point_table_unavailable", err)
	}

	owner, err := dnfexperienceitem.NewOwner(repositories)
	if err != nil {
		return s.answerRejectedExperienceItem(session, slotIndex, stack.ItemID, "owner_unavailable", err)
	}
	result, err := owner.UseSkillBook(ctx, dnfexperienceitem.Command{
		AccountID:       s.accountIDForSession(session),
		CharacterID:     characterID,
		SourceListType:  dnfrepo.MainInventoryListType,
		SourceSlotIndex: slotIndex,
		ItemID:          stack.ItemID,
		PVFTotalSP:      pvfPoints.TotalSP,
		PVFTotalTP:      pvfPoints.TotalTP,
		Book:            book,
		Now:             s.gameplayNow().UTC(),
	})
	if err != nil {
		return s.answerRejectedExperienceItem(session, slotIndex, stack.ItemID, "domain_rejected", err)
	}

	if err := s.sendGameUpperSuccess(session, opcode, successExperienceItemBody(session.selectedCharacterID)); err != nil {
		return err
	}
	s.logGameEvent(session, "game-increase-status-committed",
		"msg_id", opcode,
		"slot_index", result.SlotIndex,
		"item_id", result.ItemID,
		"book_kind", result.Book.Kind,
		"book_amount", result.Book.Amount,
		"book_name", result.Book.Name,
		"remaining", result.RemainingCount,
		"bonus_sp", result.BonusSP,
		"bonus_tp", result.BonusTP,
		"total_sp", result.Points.TotalSP,
		"remaining_sp", result.Points.RemainingSP,
		"total_tp", result.Points.TotalTP,
		"remaining_tp", result.Points.RemainingTP,
		"state_source", "runtime_pvf_skill_book_atomic_item_bonus_and_ledger")
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "increase_status_skill_book_use", []alignedcmd.ItemSlotRefresh{{
		ListType:  dnfrepo.MainInventoryListType,
		SlotIndex: slotIndex,
	}}); err != nil {
		return err
	}
	return s.sendCurrentExperienceItemCharacterState(session, characterID, result)
}

// sendCurrentExperienceItemCharacterState republishes the absolute character
// state (level, EXP, remaining SP/TP) with the already-proven class0/op37 body
// used by quest finishes, dungeon settlement, and the finish-loading bootstrap.
func (s *Service) sendCurrentExperienceItemCharacterState(
	session *gameSession,
	characterID string,
	result dnfexperienceitem.Result,
) error {
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		return fmt.Errorf("increase-status character state requires character repository")
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		return fmt.Errorf("load increase-status post-commit character: %w", err)
	}
	if !found {
		return fmt.Errorf("increase-status post-commit character %s is missing", characterID)
	}
	body := buildCurrentFinishLoadingCharacterStateBody(character, result.Points)
	if len(body) != currentFinishLoadingCharacterStateBodySize {
		return fmt.Errorf("increase-status character state body length %d, want %d", len(body), currentFinishLoadingCharacterStateBodySize)
	}
	s.logGameEvent(session, "game-increase-status-character-state-send",
		"msg_id", currentDungeonCharacterStateMsgID,
		"classification", 0,
		"body_len", len(body),
		"remaining_sp", result.Points.RemainingSP,
		"remaining_tp", result.Points.RemainingTP,
		"sequence", "op30_ack_then_op14_source_slot_then_class0_op37_absolute_character_state")
	return s.sendGameUpperRawClass(session, currentDungeonCharacterStateMsgID, body, 0)
}

// experienceItemMainSlotKey is the durable list-0 slot key for the request's
// source slot; op30 carries no list type, exactly like the reference parser.
func experienceItemMainSlotKey(slot int16) string {
	return strconv.Itoa(int(dnfrepo.MainInventoryListType)) + ":" + strconv.FormatInt(int64(slot), 10)
}

// answerRejectedExperienceItem logs the precise rejection and answers with the
// reference failure record so the client never waits for a reply.
func (s *Service) answerRejectedExperienceItem(
	session *gameSession,
	slotIndex int16,
	itemID int64,
	reason string,
	cause error,
) error {
	opcode := uint16(dnfenum.CmdPacketIncreaseStatus)
	code := byte(currentExperienceItemUseUnknownCode)
	if reason == "source_slot_empty" || reason == "inventory_missing" {
		code = currentExperienceItemUseMissingSourceCode
	}
	fields := []any{
		"msg_id", opcode,
		"slot_index", slotIndex,
		"item_id", itemID,
		"reason", reason,
	}
	if cause != nil {
		fields = append(fields, "error", cause)
	}
	if errors.Is(cause, dnfexperienceitem.ErrLedgerRegression) {
		fields = append(fields, "error_kind", "ledger_regression")
	}
	s.logGameEvent(session, "game-increase-status-blocked", fields...)
	return s.sendGameUpperFailure(session, opcode, code)
}
