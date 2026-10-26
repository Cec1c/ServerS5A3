package dnfbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/raid"
)

const currentRaidRewardWireDiagnosticKind = "game-raid-reward-wire"

func currentProjectionBodyDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:8])
}

// logCurrentRaidRewardWire records only fixed, bounded wire facts. It is
// deliberately independent of packetLog: the current incident has packet
// logging disabled, while these fields remain available through slog.
func (s *Service) logCurrentRaidRewardWire(
	session *gameSession,
	packet currentAntonRewardPacket,
	instanceID string,
	mode, phase byte,
	written bool,
	reason string,
) {
	if s == nil || session == nil || packet.opcode != 587 && packet.opcode != 600 {
		return
	}
	digest := ""
	if packet.body != nil {
		digest = currentProjectionBodyDigest(packet.body)
	}
	fields := []any{
		"kind", currentRaidRewardWireDiagnosticKind,
		"char_id", session.selectedCharacterID,
		"instance_id", instanceID,
		"mode", mode,
		"phase", phase,
		"opcode", packet.opcode,
		"class", packet.class,
		"body_len", len(packet.body),
		"body_digest", digest,
		"written", written,
	}
	if reason != "" {
		fields = append(fields, "reason", reason)
	}
	switch packet.opcode {
	case 587:
		state := -1
		layoutOK := len(packet.body) == 2
		if len(packet.body) > 0 {
			state = int(packet.body[0])
		}
		fields = append(fields, "layout_ok", layoutOK, "state", state)
	case 600:
		kind, cardCount, itemCount, owners, layoutOK := currentRaidRewardWire600(packet.body)
		fields = append(fields,
			"layout_ok", layoutOK,
			"card_kind", kind,
			"card_count", cardCount,
			"item_count", itemCount,
			"owner_ids", currentRaidRewardWireOwners(owners),
			"owner_count", len(owners),
		)
	}
	s.logInfo("dnfbridge raid reward wire diagnostic", fields...)
}

// currentRaidRewardWire600 validates the native NOTI600 card layout before
// exposing counts. It never returns or logs item payloads.
func currentRaidRewardWire600(body []byte) (kind byte, cardCount, itemCount int, owners []uint16, ok bool) {
	if len(body) < 2 {
		return 0, 0, 0, nil, false
	}
	kind = body[0]
	cardCount = int(body[1])
	if cardCount > raid.MaxAttackPartyMembers {
		return kind, cardCount, 0, nil, false
	}
	pos := 2
	owners = make([]uint16, 0, cardCount)
	for i := 0; i < cardCount; i++ {
		if pos+4 > len(body) {
			return kind, cardCount, itemCount, owners, false
		}
		owner := uint16(body[pos]) | uint16(body[pos+1])<<8
		itemCards := int(body[pos+3])
		pos += 4
		if itemCards > 0 && pos+itemCards*8 > len(body) {
			return kind, cardCount, itemCount, owners, false
		}
		pos += itemCards * 8
		itemCount += itemCards
		owners = append(owners, owner)
	}
	return kind, cardCount, itemCount, owners, pos == len(body)
}

func currentRaidRewardWireOwners(owners []uint16) string {
	if len(owners) == 0 {
		return ""
	}
	if len(owners) > raid.MaxAttackPartyMembers {
		owners = owners[:raid.MaxAttackPartyMembers]
	}
	var b strings.Builder
	for i, owner := range owners {
		if i != 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatUint(uint64(owner), 10))
	}
	return b.String()
}
