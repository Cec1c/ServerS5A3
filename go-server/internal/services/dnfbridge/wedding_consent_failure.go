package dnfbridge

import (
	"errors"

	"longheng.io/server/internal/modules/dnf/wedding"
)

// These are the current native consent-result codes, not CMD1032 codes.
func currentWeddingConsentFailureCode(operation byte, err error) int32 {
	if operation == 2 {
		switch {
		case errors.Is(err, wedding.ErrGold):
			return -16
		case errors.Is(err, wedding.ErrItem):
			return -14
		case errors.Is(err, wedding.ErrQuest):
			return -9
		}
	}
	if operation == 7 && errors.Is(err, wedding.ErrGold) {
		return -7
	}
	return -1
}

// A matched invitation has already been consumed. Both still-current parties
// must receive the result even if a socket write fails or the commit rejects;
// otherwise the initiator has no pending timer left to close its waiting UI.
func (s *Service) publishCurrentWeddingConsentFailure(inv *currentWeddingInvitation, response currentWeddingResponse, err error) error {
	code := currentWeddingConsentFailureCode(response.Operation, err)
	var published error
	if s.boundGameSessionCharacterCurrent(inv.to) {
		published = s.sendGameUpperRawClass(inv.to.session, currentWeddingResponseOpcode, buildCurrentWeddingResponseRecord(response, code, true), 1)
	}
	if s.boundGameSessionCharacterCurrent(inv.from) {
		notice := response
		notice.Target = uint32(inv.to.character)
		published = errors.Join(published, s.sendGameUpperRawClass(inv.from.session, currentWeddingResponseNotice, buildCurrentWeddingResponseRecord(notice, code, false), 0))
	}
	return published
}
