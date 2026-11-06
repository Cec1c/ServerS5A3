package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

// 1AD7780 reads a packed 97-byte profile after the dispatch flag. The
// native preview compares the receiver's relationship token at 4, then reads
// name at 12, level/job/packed grow at 42..44 and thirteen appearance IDs at 45.
// No consumer was observed for fields 0 and 8; preserve their existing zeros.
func buildCurrentWeddingCharacter(c r.CharacterRecord, equipment r.EquipmentRecord, token uint32) ([]byte, error) {
	if _, err := wedding.NumericID(c.CharacterID); err != nil {
		return nil, err
	}
	job, ok := characterJobByte(c)
	if !ok || c.Level <= 0 || c.Level > 255 || strings.ContainsRune(c.Name, 0) {
		return nil, wedding.ErrState
	}
	name, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(c.Name))
	if err != nil || len(name) == 0 || len(name) >= 30 {
		return nil, wedding.ErrState
	}
	if equipment.CharacterID != c.CharacterID {
		return nil, wedding.ErrOwnership
	}
	rows, err := buildCurrentActorMode0AppearanceSummaryFromEquipment(equipment)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 98)
	b[0] = 1
	binary.LittleEndian.PutUint32(b[5:], token)
	copy(b[13:43], name)
	b[43], b[44], b[45] = byte(c.Level), job, characterGrowTypeByte(c)
	// The native actor preview uses -1 to select the class's default outfit.
	// Zero is an item lookup, not an empty appearance slot.
	for slot := 0; slot < 13; slot++ {
		binary.LittleEndian.PutUint32(b[46+slot*4:], currentActorMode0AppearanceEmptyItem)
	}
	for _, row := range rows {
		if row.Slot < 0 || row.Slot > 12 {
			continue
		}
		binary.LittleEndian.PutUint32(b[46+int(row.Slot)*4:], uint32(row.ItemIDOrIcon))
	}
	return b, nil
}

func (s *Service) handleCurrentWeddingCharacter(session *gameSession, body []byte) error {
	fail := func() error { return s.sendGameUpperRawClass(session, currentWeddingCharacterOpcode, []byte{0}, 1) }
	if len(body) != 17 {
		return fail()
	}
	target := binary.LittleEndian.Uint32(body[13:])
	self, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || target > 65535 {
		return fail()
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	// The request owner holds no town/asset lock here. Serialize its fresh
	// relationship snapshot with engagement, cancellation and publication.
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	character, err := s.currentWeddingCharacter(ctx, self)
	if err != nil {
		return fail()
	}
	state, err := wedding.Read(character)
	if err != nil {
		return fail()
	}
	if target == 0 {
		// The ceremony form asks for its real spouse with target zero while it
		// waits for the native partner name. It never means the selected self.
		info, err := s.currentWeddingInfoBody(character)
		if err != nil {
			return fail()
		}
		response, err := s.currentWeddingPartnerProfile(ctx, character, binary.LittleEndian.Uint32(info))
		if err != nil || !s.boundGameSessionCharacterCurrent(self) {
			return fail()
		}
		return s.sendGameUpperRawClass(session, currentWeddingCharacterOpcode, response, 1)
	}
	repos, available := s.repositoryGroup()
	if !available || repos.Character == nil || repos.Equipment == nil {
		return fail()
	}
	id := strconv.FormatUint(uint64(target), 10)
	other, found, err := repos.Character.Load(ctx, id)
	if err != nil {
		return err
	}
	if !found || other.CharacterID != id || other.AccountID == "" {
		return fail()
	}
	if target != uint32(self.character) {
		if int64(target) == state.PartnerID {
			if _, _, err = wedding.ValidatePair(character, other); err != nil {
				return fail()
			}
		} else {
			peer, online := s.onlineGameSessionCharacterSnapshot(uint16(target))
			if !online || !s.currentWeddingPriestScene(self, peer) {
				return fail()
			}
		}
	}
	equipment, found, err := repos.Equipment.Load(ctx, id)
	if err != nil {
		return err
	}
	if !found || equipment.CharacterID != id {
		return fail()
	}
	info, err := s.currentWeddingInfoBody(character)
	if err != nil {
		return fail()
	}
	response, err := buildCurrentWeddingCharacter(other, equipment, binary.LittleEndian.Uint32(info))
	if err != nil || !s.boundGameSessionCharacterCurrent(self) {
		return fail()
	}
	return s.sendGameUpperRawClass(session, currentWeddingCharacterOpcode, response, 1)
}
