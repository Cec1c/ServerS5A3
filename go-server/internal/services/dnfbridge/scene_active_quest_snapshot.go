package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// currentActiveQuestSnapshotMsgID is registered by the current EXE at
// sub_1D632D0.  The older 86JP client used 0x023F for the analogous
// notification; that numeric ID is not registered by this client version.
const currentActiveQuestSnapshotMsgID = uint16(0x023E)

// errCurrentActiveQuestSnapshotUnavailable 表示这次活动任务快照根本没有发给客户端：
// 仓库不可用、记录读不出来或记录缺失。需要确认投影已下发的调用方据此停止后续动作。
var errCurrentActiveQuestSnapshotUnavailable = fmt.Errorf("dnf current active quest snapshot is unavailable")

func buildCurrentActiveQuestSnapshotBody(record dnfrepo.QuestRecord, hasRecord bool) []byte {
	rows := currentSelectAckQuestRows(record, hasRecord)
	var writer packetWriter
	writer.writeUint32(uint32(len(rows)))
	for _, row := range rows {
		writer.writeUint16(row.questID)
		writer.writeUint32(row.triggerValue)
	}
	return writer.bytes()
}

func (s *Service) sendCurrentActiveQuestSnapshotFromCommittedQuest(
	session *gameSession,
	record dnfrepo.QuestRecord,
	source string,
) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	if record.CharacterID != characterID {
		return fmt.Errorf("committed active quest snapshot owner mismatch: selected=%s record=%q", characterID, record.CharacterID)
	}
	body := buildCurrentActiveQuestSnapshotBody(record, true)
	rows := currentSelectAckQuestRows(record, true)
	s.logGameEvent(session, "game-upper-current-active-quest-snapshot-send",
		"char_id", session.selectedCharacterID,
		"msg_id", currentActiveQuestSnapshotMsgID,
		"classification", 0,
		"source", source,
		"quest_record_found", true,
		"active_count", len(rows),
		"body_len", len(body),
		"body_source", "current_exe_sub_1D632D0_u32_count_u16_quest_u32_trigger")
	return s.sendCurrentActiveQuestSnapshotWithEmptyCompletedRestore(session, record, true, body, source)
}

func (s *Service) sendCurrentActiveQuestSnapshotForSession(session *gameSession, source string) error {
	return s.sendCurrentActiveQuestSnapshot(session, source, false)
}

// sendCurrentActiveQuestSnapshotChecked 是给「必须确认客户端真的收到投影」的调用方：
// 仓库不可用、记录读取失败或记录缺失同样返回错误，而不是只记录后继续，从而避免出现
// 「DB 里任务已经 active、客户端却没有任务」。真实发包失败在两种模式下都返回错误。
func (s *Service) sendCurrentActiveQuestSnapshotChecked(session *gameSession, source string) error {
	return s.sendCurrentActiveQuestSnapshot(session, source, true)
}

func (s *Service) sendCurrentActiveQuestSnapshot(session *gameSession, source string, required bool) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Quest == nil {
		s.logGameEvent(session, "game-upper-current-active-quest-snapshot-skipped",
			"char_id", session.selectedCharacterID,
			"msg_id", currentActiveQuestSnapshotMsgID,
			"source", source,
			"reason", "quest_repository_unavailable")
		if required {
			return fmt.Errorf("%w: quest_repository_unavailable", errCurrentActiveQuestSnapshotUnavailable)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	record, found, err := repositories.Quest.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil {
		cancel()
		s.logGameEvent(session, "game-upper-current-active-quest-snapshot-skipped",
			"char_id", session.selectedCharacterID,
			"msg_id", currentActiveQuestSnapshotMsgID,
			"source", source,
			"reason", "quest_record_load_failed",
			"error", err)
		if required {
			return fmt.Errorf("%w: quest_record_load_failed: %v", errCurrentActiveQuestSnapshotUnavailable, err)
		}
		return nil
	}
	if !found {
		if required {
			cancel()
			s.logGameEvent(session, "game-upper-current-active-quest-snapshot-skipped",
				"char_id", session.selectedCharacterID,
				"msg_id", currentActiveQuestSnapshotMsgID,
				"source", source,
				"reason", "quest_record_missing")
			return fmt.Errorf("%w: quest_record_missing", errCurrentActiveQuestSnapshotUnavailable)
		}
		record = dnfrepo.QuestRecord{CharacterID: strconv.Itoa(int(session.selectedCharacterID))}
	} else {
		record = s.reconcileLegacySaturatedActiveQuestTriggers(ctx, session, repositories.Quest, record)
		recoveredRecord, recovered, recoveryErr := s.recoverCurrentQueenLetterForSnapshot(ctx, session, record)
		if recoveryErr != nil {
			s.logGameEvent(session, "game-upper-current-active-quest-queen-letter-recovery-skipped",
				"char_id", session.selectedCharacterID,
				"source", source,
				"reason", "source_recovery_failed",
				"error", recoveryErr)
			if errors.Is(recoveryErr, errCurrentQueenLetterRecoveryInventorySend) {
				cancel()
				return recoveryErr
			}
		} else {
			record = recoveredRecord
			if recovered {
				s.logGameEvent(session, "game-upper-current-active-quest-queen-letter-recovery-applied",
					"char_id", session.selectedCharacterID,
					"source", source,
					"sequence", "op14_recovery_row_then_op574_active_snapshot")
			}
		}
		recoveredRecord, recovered, recoveryErr = s.recoverCurrentBoxSourceForSnapshot(ctx, session, record)
		if recoveryErr != nil {
			s.logGameEvent(session, "game-upper-current-active-quest-box-source-recovery-skipped",
				"char_id", session.selectedCharacterID,
				"source", source,
				"reason", "source_recovery_failed",
				"error", recoveryErr)
			if errors.Is(recoveryErr, errCurrentBoxSourceRecoveryInventorySend) {
				cancel()
				return recoveryErr
			}
		} else {
			record = recoveredRecord
			if recovered {
				s.logGameEvent(session, "game-upper-current-active-quest-box-source-recovery-applied",
					"char_id", session.selectedCharacterID,
					"source", source,
					"sequence", "op14_recovery_row_then_op574_active_snapshot")
			}
		}
		recoveredRecord, recovered, recoveryErr = s.recoverCurrentMedicineSourceForSnapshot(ctx, session, record)
		if recoveryErr != nil {
			s.logGameEvent(session, "game-upper-current-active-quest-medicine-source-recovery-skipped",
				"char_id", session.selectedCharacterID,
				"source", source,
				"reason", "source_recovery_failed",
				"error", recoveryErr)
			if errors.Is(recoveryErr, errCurrentMedicineSourceRecoveryInventorySend) {
				cancel()
				return recoveryErr
			}
		} else {
			record = recoveredRecord
			if recovered {
				s.logGameEvent(session, "game-upper-current-active-quest-medicine-source-recovery-applied",
					"char_id", session.selectedCharacterID,
					"source", source,
					"sequence", "op14_recovery_row_then_op574_active_snapshot")
			}
		}
	}
	cancel()
	body := buildCurrentActiveQuestSnapshotBody(record, found)
	rows := currentSelectAckQuestRows(record, found)
	s.logGameEvent(session, "game-upper-current-active-quest-snapshot-send",
		"char_id", session.selectedCharacterID,
		"msg_id", currentActiveQuestSnapshotMsgID,
		"classification", 0,
		"source", source,
		"quest_record_found", found,
		"active_count", len(rows),
		"body_len", len(body),
		"body_source", "current_exe_sub_1D632D0_u32_count_u16_quest_u32_trigger")
	return s.sendCurrentActiveQuestSnapshotWithEmptyCompletedRestore(session, record, found, body, source)
}

// 本机official-24.9/v97的574消费者2014370在count=0时调用1CB7E40，
// 清空QuestManager+0xc4完成集合；普通完成表56BDE00仍保留。地图虚拟行走
// 判断1A828F9调用1CA8890读取前者，因此356先于空574会导致已完成的入口仍封闭。
// 空活动快照之后，用同一次读取/提交的真实任务记录恢复356；不重做任务事务，
// 不刷新地图、不添加任务、也不把其他客户端扩大为已验证的消费者。
func (s *Service) sendCurrentActiveQuestSnapshotWithEmptyCompletedRestore(
	session *gameSession,
	record dnfrepo.QuestRecord,
	found bool,
	body []byte,
	source string,
) error {
	var restore []byte
	if s.options.currentClientVersion == "official-24.9" && found && len(body) == 4 &&
		currentClearQuestCount(buildCurrentClearQuestListBody(record, true)) > 0 {
		var err error
		restore, err = s.buildCurrentClearQuestListTransportBodyForCommittedQuest(session, record,
			source+"_after_empty_active_restore_completed_gate_state")
		if err != nil {
			return err
		}
	}
	if err := s.sendGameUpperRawClass(session, currentActiveQuestSnapshotMsgID, body, 0); err != nil {
		return err
	}
	if len(restore) == 0 {
		return nil
	}
	s.logGameEvent(session, "game-upper-empty-active-quest-completed-state-restored",
		"说明", "空活动任务列表会清空客户端入口完成集合，随后恢复真实已完成任务；无需切换地图",
		"char_id", session.selectedCharacterID,
		"source", source,
		"packet_order", "op574_count0_then_op356_completed_state")
	return s.sendGameUpperFixed16Transport(session, currentClearQuestListMsgID, restore,
		0, 1, true, currentClearQuestListTransportCodec)
}
