package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

// Caller owns wedding.mu. The receiver is already a fresh owned character;
// resolve its reciprocal spouse even when that spouse has no online session.
func (s *Service) currentWeddingPartnerProfile(ctx context.Context, receiver r.CharacterRecord, token uint32) ([]byte, error) {
	state, err := wedding.Read(receiver)
	if err != nil || state.Phase == wedding.Single {
		return nil, wedding.ErrState
	}
	repos, available := s.repositoryGroup()
	if !available || repos.Character == nil || repos.Equipment == nil {
		return nil, wedding.ErrUnavailable
	}
	id := strconv.FormatInt(state.PartnerID, 10)
	partner, found, err := repos.Character.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found || partner.CharacterID != id {
		return nil, wedding.ErrOwnership
	}
	if _, _, err := wedding.ValidatePair(receiver, partner); err != nil {
		return nil, err
	}
	equipment, found, err := repos.Equipment.Load(ctx, partner.CharacterID)
	if err != nil {
		return nil, err
	}
	if !found || equipment.CharacterID != partner.CharacterID {
		return nil, wedding.ErrOwnership
	}
	return buildCurrentWeddingCharacter(partner, equipment, token)
}

// Only committed publication paths call this while holding wedding.mu. The
// initial town path deliberately keeps sendCurrentWeddingInfoRecord: it owns
// town/selection locks and the native form can recover by requesting target0.
func (s *Service) sendCurrentWeddingInfoAndPartnerLocked(session *gameSession, expected r.CharacterRecord) error {
	pin, valid := s.currentWeddingClearTransport(session, expected.AccountID)
	if !valid || expected.CharacterID != strconv.Itoa(int(pin.transport.character)) {
		return wedding.ErrOwnership
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	fresh, err := s.currentWeddingCharacter(ctx, pin.canonical)
	if err != nil {
		return err
	}
	info, err := s.currentWeddingInfoBody(fresh)
	if err != nil {
		return err
	}
	state, err := wedding.Read(fresh)
	if err != nil {
		return err
	}
	var profile []byte
	if state.Phase != wedding.Single {
		// Use exactly the token prepared for this receiver's Info1000. Do not
		// infer an unordered-pair algorithm or reuse another receiver's token.
		profile, err = s.currentWeddingPartnerProfile(ctx, fresh, binary.LittleEndian.Uint32(info))
		if err != nil {
			return err
		}
	}
	if !s.currentWeddingClearTransportCurrent(pin) {
		return wedding.ErrOwnership
	}
	// Both packets are completely prepared before Info1000 clears the preview.
	if err := s.sendGameUpperRawClass(session, currentWeddingInfoNotice, info, 0); err != nil {
		return err
	}
	if len(profile) != 0 {
		if !s.currentWeddingClearTransportCurrent(pin) {
			return wedding.ErrOwnership
		}
		return s.sendGameUpperRawClass(session, currentWeddingCharacterOpcode, profile, 1)
	}
	return nil
}
