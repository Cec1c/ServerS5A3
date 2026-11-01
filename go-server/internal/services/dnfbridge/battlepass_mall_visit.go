package dnfbridge

import (
	"bytes"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/protocol"
)

// Empty op63 also originates in character-login initialization. Only the
// cumulative client's accepted native shop-open transition adds this marker.
// Other Cera queries retain the existing ACK and never grant a visit.
func (s *Service) handleCurrentCeraQueryVisit(session *gameSession, classification byte, body []byte) error {
	if classification == protocol.DefaultChannelClassification && bytes.Equal(body, []byte("MSV1")) {
		if err := s.recordBattlePassAcceptedEvent(session, 11, -1); err != nil {
			s.logGameEvent(session, "game-battlepass-native-shop-visit-failed", "error", err)
		}
	}
	return s.sendGameUpperSuccess(session, uint16(dnfenum.UpperMsgFollowUpStatus), nil)
}
