package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"

	dnfonlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
)

const currentLegacyLevelEventID uint16 = 116
const currentLegacyLevelReserveID uint16 = 567
const currentLegacyLevelClaimID uint16 = 1122

func (s *Service) currentLegacyLevelCatalog(ctx context.Context) (*dnfonlineevent.LegacyLevelCatalog, error) {
	s.legacyLevelMu.Lock()
	defer s.legacyLevelMu.Unlock()
	if s.legacyLevelCatalog != nil {
		return s.legacyLevelCatalog, nil
	}
	s.initialEquipmentMu.Lock()
	archive := s.initialEquipmentArchive
	s.initialEquipmentMu.Unlock()
	if archive == nil {
		return nil, nil
	}
	catalog, err := dnfonlineevent.LoadLegacyLevelCatalog(ctx, archive)
	if err != nil {
		return nil, err
	}
	s.legacyLevelCatalog = catalog
	return catalog, nil
}

func (s *Service) sendCurrentLegacyLevel(session *gameSession, target uint32, claim bool) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	catalog, err := s.currentLegacyLevelCatalog(ctx)
	if err != nil || catalog == nil {
		if err == nil && (target != 0 || claim) {
			return dnfonlineevent.ErrOwnerUnavailable
		}
		return err
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return dnfonlineevent.ErrOwnerUnavailable
	}

	now := s.gameplayNow()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	result, err := dnfonlineevent.SettleLegacyLevel(ctx, repos, catalog, dnfonlineevent.LegacyLevelCommand{
		AccountID: s.accountIDForSession(session), CharacterID: characterID, Target: target, Claim: claim, Now: now,
		Validate: s.currentPortedRequestValidator(session, session.selectedCharacterID, false),
		Deliver:  s.currentPortedMailDelivery(ctx, session, characterID, "全职Lv预约奖励", "预约升级奖励已发放，请查收附件。", "legacy_levelup_116"),
	})
	if err != nil {
		return err
	}
	if target != 0 {
		var writer packetWriter
		writer.writeByte(1)
		writer.writeUint32(result.Target)
		if err := s.sendGameUpperRawClass(session, currentLegacyLevelReserveID, writer.bytes(), 1); err != nil {
			return err
		}
	}
	var writer packetWriter
	writer.writeUint32(result.Target)
	if result.Claimed {
		writer.writeByte(1)
	} else {
		writer.writeByte(0)
	}
	if err := s.sendGameUpperRawClass(session, 496, writer.bytes(), 0); err != nil {
		return err
	}
	if claim && result.Claimed {
		if err := s.sendGameUpperRawClass(session, 497, nil, 0); err != nil {
			return err
		}
	}
	if len(result.MailIDs) != 0 {
		return s.sendMailboxAlarmForSession(session, session.selectedCharacterID, "legacy_levelup")
	}
	return nil
}

func (s *Service) handleCurrentLegacyLevel(session *gameSession, opcode uint16, body []byte) error {
	valid := session != nil && session.selectedCharacterID != 0 && ((opcode == currentLegacyLevelReserveID && len(body) == 4) || (opcode == currentLegacyLevelClaimID && len(body) == 0))
	var target uint32
	if valid && opcode == currentLegacyLevelReserveID {
		target = binary.LittleEndian.Uint32(body)
		valid = target != 0
	}
	if valid {
		if err := s.sendCurrentLegacyLevel(session, target, opcode == currentLegacyLevelClaimID); err == nil {
			return nil
		} else {
			s.logGameEvent(session, "旧预约升级请求拒绝", "opcode", opcode, "error", err)
		}
	}
	return s.sendGameUpperRawClass(session, opcode, []byte{0, 1}, 1)
}
