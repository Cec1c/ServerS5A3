package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
)

func beforeMemberGeneration(snapshot party.RuntimePartySnapshot, characterID uint16) uint64 {
	for _, member := range snapshot.Members {
		if member.UserID == characterID {
			return member.SessionGeneration
		}
	}
	return 0
}

// Empty retirement is valid only after this receiver has left the manager.
// A surviving dungeon singleton supplies its exact retained membership.
// The run key is captured before waiting for the wire, without taking a peer's
// dungeon lock (the caller can already hold another member's dungeon lock).
func (s *Service) currentPartyRetirementGuard(receiver *gameSession, expected *alignedcmd.PartyState) func() bool {
	identity, bound := s.boundGameSessionCharacterSnapshot(receiver)
	if !bound {
		return func() bool { return false }
	}
	if expected == nil {
		empty := alignedcmd.PartyState{}
		expected = &empty
	}
	membership := s.currentPartyProjectionMembershipGuard(identity, expected)
	run, valid := s.currentDungeonPartyRunKey(receiver)
	return func() bool {
		if !s.boundGameSessionCharacterCurrent(identity) || !membership() {
			return false
		}
		current, found := s.currentDungeonPartyRunKey(receiver)
		return found == valid && (!valid || current == run)
	}
}

func (s *Service) sendRetiredPartyPacket(receiver *gameSession, msgID uint16, body []byte, expected *alignedcmd.PartyState, current func() bool, fixed bool) (bool, error) {
	if current == nil {
		current = s.currentPartyRetirementGuard(receiver, expected)
	}
	sent := false
	err := s.withGameWire(receiver, func() error {
		if !current() {
			return nil
		}
		var err error
		if fixed {
			var packet []byte
			packet, err = buildCurrentSceneClass0FixedUpperPacket(msgID, body)
			if err == nil {
				err = s.writeGameUpperPacketLocked(receiver, packet)
			}
		} else {
			err = s.sendGameUpperRawClassCodecLocked(receiver, msgID, body, 0, true)
		}
		sent = err == nil
		return err
	})
	return sent, err
}
