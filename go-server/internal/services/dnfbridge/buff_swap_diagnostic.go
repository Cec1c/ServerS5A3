package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/buffswap"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const buffSwapDiagnosticKind = "game-buff-swap-native-diagnostic"

// Keep repository failures actionable without logging arbitrary SQL, account
// values or registration contents from an error string.
func buffSwapProjectionFailureReason(err error) string {
	var syntax *json.SyntaxError
	var field *json.UnmarshalTypeError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "owned_projection_deadline"
	case errors.Is(err, context.Canceled):
		return "owned_projection_cancelled"
	case errors.Is(err, dnfrepo.ErrBuffLegacyOwnerMismatch):
		return "owned_projection_owner_mismatch"
	case errors.Is(err, dnfrepo.ErrBuffOwnedSnapshotUnavailable):
		return "owned_projection_transaction_unavailable"
	case errors.Is(err, dnfrepo.ErrBuffLegacyAmbiguous):
		return "owned_projection_legacy_ambiguous"
	case errors.Is(err, buffswap.ErrInvalid):
		return "owned_projection_registration_invalid"
	case errors.As(err, &syntax), errors.As(err, &field):
		return "owned_projection_registration_malformed"
	default:
		return "owned_projection_load_failed"
	}
}

// buffSwapDiagnosticBodyDigest is a short digest of the canonical full 1368
// body. The same digest is recorded for row-only 1367 sends, so successive
// diagnostics can be compared without depending on the packet log.
func buffSwapDiagnosticBodyDigest(body []byte) string {
	if len(body) == 0 {
		return "none"
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:8])
}

// buffSwapDiagnosticSlots records only the slot to list/index projection. It
// deliberately excludes the persistent instance token and item identifier.
func buffSwapDiagnosticSlots(state buffswap.State) string {
	if len(state.Slots) == 0 {
		return "none"
	}
	slots := make([]int, 0, len(state.Slots))
	for slot := range state.Slots {
		slots = append(slots, int(slot))
	}
	sort.Ints(slots)
	var out strings.Builder
	for i, slotValue := range slots {
		if i > 0 {
			out.WriteByte(',')
		}
		slot := byte(slotValue)
		binding := state.Slots[slot]
		fmt.Fprintf(&out, "%d:%d/%d", slot, binding.List, binding.Index)
	}
	return out.String()
}

// logBuffSwapDiagnostic emits one bounded, fixed-shape application log line for
// explicit registration and committed equipment Buff sends. It never reads the
// packet logger, persistent instance values, account credentials or raw packet
// bytes.
func (s *Service) logBuffSwapDiagnostic(
	session *gameSession,
	outcome string,
	reason string,
	source string,
	identity buffOwnedSendIdentity,
	msgID uint16,
	body []byte,
	wireBodyLen int,
	state buffswap.State,
	omitted int,
) {
	if s == nil || session == nil {
		return
	}
	characterID := identity.character
	if characterID == 0 {
		characterID = session.selectedCharacterID
	}
	generation := identity.generation
	if identity.session == nil {
		generation = session.characterGeneration
	}
	s.logInfo("dnfbridge buff swap diagnostic",
		"kind", buffSwapDiagnosticKind,
		"conn_id", session.connID,
		"char_id", characterID,
		"generation", generation,
		"source", source,
		"outcome", outcome,
		"reason", reason,
		"msg_id", msgID,
		"skill", state.Skill,
		"canonical_body_len", len(body),
		"wire_body_len", wireBodyLen,
		"body_digest", buffSwapDiagnosticBodyDigest(body),
		"slot_count", len(state.Slots),
		"slots", buffSwapDiagnosticSlots(state),
		"omitted", omitted,
	)
}
