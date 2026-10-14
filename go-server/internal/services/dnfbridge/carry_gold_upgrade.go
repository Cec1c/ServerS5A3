package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/carrygold"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentCarryGoldStageMsgID = uint16(947)

// Current EXE evidence: 0x01D93000 sends 14 bytes with only byte 13
// initialized. 0x01FD4B00 reads u8 stage/u32 result after the ACK flag;
// flag=0/result=1 displays DSTR 72016 (insufficient gold), result=2..4
// displays the generic rejection. NOTI 947 at 0x0203AE60 reads one stage byte.
// All paths share 0x057B7008. Stages 1..4 select the client cap tables
// 0x04CF88D8 / 0x04D13FFC (500M, 600M, 700M, 800M).
func (s *Service) sendCurrentCarryGoldStage(session *gameSession, character dnfrepo.CharacterRecord) error {
	stage, err := carrygold.Stage(character)
	if err != nil {
		return err
	}
	return s.sendCurrentSceneFixedClass0Packet(session, currentCarryGoldStageMsgID, []byte{stage}, "carry_gold_durable_stage")
}

func (s *Service) sendCarryGoldUpgradeReply(session *gameSession, stage byte, result uint32) error {
	body := make([]byte, 6)
	if result == 0 {
		body[0] = 1
	}
	body[1] = stage
	binary.LittleEndian.PutUint32(body[2:], result)
	return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUpgradeCarryGold), body, dnfproto.DefaultChannelClassification)
}

func (s *Service) sendCarryGoldWallet(session *gameSession, gold int64) error {
	return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember),
		buildCurrentItemUpdateBody(dnfrepo.MainInventoryListType, []currentItemListEntry{currentGoldWalletItemListEntry(gold)}), 0)
}

func (s *Service) handleCurrentUpgradeCarryGold(session *gameSession, body []byte) error {
	if session == nil || session.conn == nil {
		return nil
	}
	if session.selectedCharacterID == 0 || len(body) != 14 {
		return s.sendCarryGoldUpgradeReply(session, 0, 2)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return s.sendCarryGoldUpgradeReply(session, 0, 2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := carrygold.Upgrade(ctx, repos, s.accountIDForSession(session), strconv.Itoa(int(session.selectedCharacterID)), body[13])
	if err != nil {
		code := uint32(2)
		if errors.Is(err, carrygold.ErrGold) {
			code = 1
		}
		if replyErr := s.sendCarryGoldUpgradeReply(session, result.Stage, code); replyErr != nil {
			return replyErr
		}
		if errors.Is(err, carrygold.ErrStage) && result.Known {
			// The previous ACK may have been lost after commit. Repair both
			// cached stage and wallet while refusing the duplicate purchase.
			if err := s.sendCurrentSceneFixedClass0Packet(session, currentCarryGoldStageMsgID, []byte{result.Stage}, "carry_gold_stale_request"); err != nil {
				return err
			}
			return s.sendCarryGoldWallet(session, result.Gold)
		}
		if errors.Is(err, carrygold.ErrGold) || errors.Is(err, carrygold.ErrLevel) || errors.Is(err, carrygold.ErrStage) || errors.Is(err, carrygold.ErrOwnership) || errors.Is(err, carrygold.ErrUnavailable) {
			return nil
		}
		return err
	}
	s.logGameEvent(session, "game-carry-gold-upgrade-committed", "char_id", session.selectedCharacterID, "stage", result.Stage, "cost", carrygold.StageCost)
	if err := s.sendCarryGoldUpgradeReply(session, result.Stage, 0); err != nil {
		return err
	}
	return s.sendCarryGoldWallet(session, result.Gold)
}

func carryGoldGameplayModule() gameplayModuleDefinition {
	opcode := uint16(dnfenum.CmdPacketUpgradeCarryGold)
	return gameplayModuleDefinition{
		Name: "carry-gold",
		LegacyHandlers: map[uint16]gameplayHandler{opcode: func(s *Service, session *gameSession, request gameplayRequest) error {
			return s.handleCurrentUpgradeCarryGold(session, request.Body)
		}},
		UpperHandlers: map[uint16]gameplayHandler{opcode: defaultClassGameplayHandler("game-carry-gold-upgrade-blocked", "command_class_mismatch", func(s *Service, session *gameSession, body []byte) error {
			return s.handleCurrentUpgradeCarryGold(session, body)
		})},
	}
}
