package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"strconv"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentDimensionCountMsgID uint16 = 812

func buildCurrentDimensionCountBody(count dnfdungeon.DimensionEntranceCount) []byte {
	body := make([]byte, 9)
	binary.LittleEndian.PutUint32(body, count.BaseUsed)
	binary.LittleEndian.PutUint32(body[4:], count.ExtraAvailable)
	body[8] = count.WeeklyAppearances
	return body
}

func (s *Service) currentDimensionInfoDocument() (*dnfpvf.Document, error) {
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	if archive == nil {
		return nil, dnfdungeon.ErrOwnerUnavailable
	}
	text, _, err := readInitialPVFText(archive, currentDimensionDungeonInfoPath)
	if err != nil {
		return nil, err
	}
	return dnfpvf.Parse(currentDimensionDungeonInfoPath, text)
}

func currentDimensionPolicyScalar(doc *dnfpvf.Document, key string) (int64, error) {
	count := 0
	for _, section := range doc.Sections {
		if section.Name == key {
			count++
		}
	}
	values, ok := doc.Section(key)
	if count != 1 || !ok || len(values) != 1 || values[0].Kind != dnfpvf.TokenInt || values[0].Int < 0 {
		return 0, fmt.Errorf("invalid dimension policy field %q", key)
	}
	return values[0].Int, nil
}

func (s *Service) sendCurrentDimensionCount(session *gameSession, character dnfrepo.CharacterRecord) error {
	if s == nil || s.options.currentClientVersion != "official-24.9" || s.initialEquipmentArchive == nil || !currentDimensionGateEntranceEnabled() {
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || character.CharacterID != strconv.Itoa(int(identity.character)) || character.AccountID != s.accountIDForSession(session) {
		return dnfdungeon.ErrOwnerUnavailable
	}
	base, _, err := s.currentDimensionGateDailyEntryLimit()
	if err != nil {
		return err
	}
	doc, err := s.currentDimensionInfoDocument()
	if err != nil {
		return err
	}
	extra, err := currentDimensionPolicyScalar(doc, "max extra enter count")
	if err != nil {
		return err
	}
	maximum, err := currentDimensionPolicyScalar(doc, "max daily enter count")
	if err != nil {
		return err
	}
	weekly, err := currentDimensionPolicyScalar(doc, "hidden boss appear count on week")
	if err != nil {
		return err
	}
	if maximum-extra != base {
		return fmt.Errorf("dimension client/server base mismatch: %d-%d != %d", maximum, extra, base)
	}
	count, err := dnfdungeon.DimensionGateEntranceProjection(character, s.gameplayNow(), base, extra, weekly)
	if err != nil {
		return err
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return dnfdungeon.ErrOwnerUnavailable
	}
	return s.sendGameUpperRawClass(session, currentDimensionCountMsgID, buildCurrentDimensionCountBody(count), 0)
}
