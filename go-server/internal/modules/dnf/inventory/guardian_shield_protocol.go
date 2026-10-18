package inventory

import (
	"encoding/binary"
	"fmt"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfguardianshield "longheng.io/server/internal/modules/dnf/guardianshield"
)

const currentGuardianShieldDeckNotificationMsgID uint16 = 580
const moveItemspaceRejectedErrorCode byte = 17

func buildMoveItemspaceFailureAck(cmd Command, errorCode byte) []byte {
	return []byte{0, errorCode, cmd.SourceListType, cmd.DestinationListType}
}

func decodeCurrentGuardianShieldDeckRequest(body []byte) (dnfguardianshield.Deck, error) {
	var deck dnfguardianshield.Deck
	if len(body) != dnfguardianshield.DeckSlotCount*4 {
		return deck, fmt.Errorf("current op657 guardian shield deck body length=%d want=%d", len(body), dnfguardianshield.DeckSlotCount*4)
	}
	for index := range deck {
		deck[index] = binary.LittleEndian.Uint32(body[index*4 : index*4+4])
	}
	return deck, nil
}

func buildCurrentGuardianShieldDeckBody(deck dnfguardianshield.Deck) []byte {
	body := make([]byte, dnfguardianshield.DeckSlotCount*4)
	for index, itemID := range deck {
		binary.LittleEndian.PutUint32(body[index*4:index*4+4], itemID)
	}
	return body
}

func buildCurrentGuardianShieldDeckSuccessBody(deck dnfguardianshield.Deck) []byte {
	body := make([]byte, 2+dnfguardianshield.DeckSlotCount*4)
	body[0] = 1
	copy(body[2:], buildCurrentGuardianShieldDeckBody(deck))
	return body
}

func ownerAppliedGuardianShieldMoveResult(opcode uint16, cmd Command, result dnfguardianshield.Result) alignedcmd.Result {
	postActions := []alignedcmd.PostAction(nil)
	if result.MainChanged {
		// The Guardian's visible main shield is persisted in actor equipment
		// slot 24. That slot is outside the ordinary fourteen-row mode0
		// appearance table, so the town actor must be reconciled through the
		// current client's native complete op342 equipment group followed by its
		// op357 same-state redraw. Backup-only deck edits use op657 and do not
		// touch the visible actor.
		postActions = append(postActions, alignedcmd.PostActionRefreshSelectedActorExpandedEquipment)
		// While the main deck slot stays occupied, additionally republish the
		// selected actor's mode0 scene-object appearance rows exactly like an
		// ordinary equipment change. The mode0 appearance summary carries the
		// actor-slot-24 deck projection as an extra row, so the client redraws
		// the shield immediately without a scene reselect. Equipping a shield
		// while the deck[0] slot was empty still reaches this branch (empty ->
		// item); only an unequip (deck[0] emptied) skips it and converges on
		// the next scene bootstrap, mirroring the 90server deck-move sequence
		// (op19 ACK -> op580 deck -> op342 expanded equipment -> mode0
		// appearance + op357 user-state redraw).
		if result.Deck[dnfguardianshield.MainDeckSlot] > 0 {
			postActions = append(postActions, alignedcmd.PostActionRefreshSelectedActorAppearance)
		}
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"guardian shield owner applied current op19 endpoints source=(%d,%d) destination=(%d,%d) changed=%t main_changed=%t; emitted native ACK then authoritative class0/op580 five-slot deck and, when the visible main changed, native op342/op357 town appearance refresh",
			cmd.SourceListType,
			cmd.SourceSlotIndex,
			cmd.DestinationListType,
			cmd.DestinationSlotIndex,
			result.Changed,
			result.MainChanged,
		),
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, buildMoveItemspaceSuccessAck(cmd, cmd.MoveCount)),
			class0Response(currentGuardianShieldDeckNotificationMsgID, buildCurrentGuardianShieldDeckBody(result.Deck)),
		},
		PostActions: postActions,
	}
}

func ownerRejectedGuardianShieldMoveResult(opcode uint16, cmd Command, err error) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason:          fmt.Sprintf("guardian shield move rejected without mutation: %s: %v", cmd.String(), err),
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, buildMoveItemspaceFailureAck(cmd, moveItemspaceRejectedErrorCode)),
		},
	}
}

func appliedGuardianShieldDeckResult(opcode uint16, result dnfguardianshield.Result) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       "change_guardian_shield_deck",
		Reason: fmt.Sprintf(
			"guardian shield owner atomically saved current op657 five-slot deck changed=%t main_changed=%t",
			result.Changed,
			result.MainChanged,
		),
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, buildCurrentGuardianShieldDeckSuccessBody(result.Deck)),
		},
	}
}

func rejectedGuardianShieldDeckResult(opcode uint16, err error) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       "change_guardian_shield_deck",
		Reason:          fmt.Sprintf("guardian shield op657 rejected without mutation: %v", err),
		// Current handler reads the remaining reserved/deck fields only when
		// status is nonzero. A one-byte zero is therefore the closed failure body.
		UpperResponses: []alignedcmd.UpperResponse{class1Response(opcode, []byte{0})},
	}
}
