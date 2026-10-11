package dnfbridge

import (
	"os"
	"strings"

	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

// op19 body[26] identifies two client request origins. Historical module 8
// writes 1 using a heuristic and retains its existing 400ms quiet handling.
// The inspected current-image UI producer patch writes 2 only for bounded
// direct callers; eligible normal equipment/avatar moves commit immediately.
// Marker 0 and unknown values preserve the existing login/occurrence/mirror
// rules. Unresolved indirect first-empty-helper callers remain unmarked.
//
// User reports on 2026-10-09 proved the old 15-second login and 4-second mirror
// timing rules also absorb intentional quick manual operations. Marker 2
// supplies source identity rather than weakening those timing protections.
// It grants no item or inventory authority: the owner still validates actual
// identity, current PVF placement, capacity and transaction persistence.
//
// DNFBRIDGE_OP19_PLAYER_FLAG=off/0/false/no/legacy/disable/disabled/none
// ignores both markers. Other values enable the existing compatibility reader.
const op19PlayerFlagEnv = "DNFBRIDGE_OP19_PLAYER_FLAG"

// The current-image UI patch marks the four inspected direct calls from
// 02499C00 to the real 025F9A70 producer with 2. Unlike historical module 8's
// heuristic 1, this marker has a bounded caller identity and does not require
// a quiet window. Unresolved first-empty-helper callers still write zero.
const op19CurrentUIPlayerFlag byte = 2

func op19PlayerFlagEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(op19PlayerFlagEnv))) {
	case "off", "0", "false", "no", "legacy", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// op19PlayerRequestFlagged accepts legacy 1 or scoped current UI 2.
// Other marker values have no manual-request meaning.
func (s *Service) op19PlayerRequestFlagged(body []byte) bool {
	if !op19PlayerFlagEnabled() {
		return false
	}
	request, err := dnfinventory.DecodeMoveItemspaceRequest(body)
	if err != nil {
		return false
	}
	return request.TrailingState0 == 1 || currentUIPlayerMove(request)
}

func (s *Service) op19CurrentUIPlayerRequest(body []byte) bool {
	if !op19PlayerFlagEnabled() {
		return false
	}
	request, err := dnfinventory.DecodeMoveItemspaceRequest(body)
	return err == nil && currentUIPlayerMove(request)
}

// Scope the new marker to one normal equipment/avatar move. It never changes
// money, pet/artifact, cargo, stack splitting, or malformed-tail interpretation.
// Actual item identity and placement remain the repository owner's decision.
func currentUIPlayerMove(request dnfinventory.MoveItemspaceRequest) bool {
	if request.TrailingState0 != op19CurrentUIPlayerFlag || request.TrailingState1 != 0 ||
		request.MoveCount != 1 || request.DestinationStack != 0 {
		return false
	}
	bag := func(list byte, slot int16) bool {
		return (list == 0 && slot > 0) || (list == 1 && slot >= 0)
	}
	worn := func(list byte, slot int16) bool { return list == currentSocketListEquipment && slot >= 0 && slot < 26 }
	return bag(request.SourceListType, request.SourceSlotIndex) && worn(request.DestinationListType, request.DestinationSlotIndex) ||
		worn(request.SourceListType, request.SourceSlotIndex) && bag(request.DestinationListType, request.DestinationSlotIndex)
}
