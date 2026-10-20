package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
)

// Current-client op33 is not an authority for raid clear objectives, including
// imported states without the modern quest_type metadata. Echo once without
// changing the repository or feeding another active-quest snapshot back.
func (s *Service) echoCurrentRaidQuestTrigger(ctx context.Context, session *gameSession, owner *dnfquest.Owner, request dnfquest.SetTriggerRequest) (bool, error) {
	if s.options.currentClientVersion != "official-24.9" {
		return false, nil
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		s.logGameEvent(session, "game-raid-quest-client-trigger-blocked", "quest_id", request.QuestID, "reason", "definition_authority_unavailable")
		return true, nil
	}
	d, found := catalog.Find(int64(request.QuestID))
	if !found || strings.ToLower(strings.Trim(d.Type, " []\t\r\n")) != "raid phase clear" {
		return false, nil
	}
	result, err := owner.Plan(ctx, dnfquest.NewTriggerCommand(alignedcmd.Request{AccountID: s.accountIDForSession(session), SelectedCharacterID: session.selectedCharacterID}, request))
	if err != nil {
		return true, err
	}
	if !result.Known || result.Status != "active" || result.ProgressValue < 0 || result.ProgressValue > (1<<32)-1 {
		return true, nil
	}
	if err := s.sendCurrentSetQuestTriggerSuccess(session, request, result, "authoritative_raid_quest_echo_without_client_progress"); err != nil {
		return true, err
	}
	session.suppressCurrentSetQuestTriggerReplay(newCurrentSetQuestTriggerReplayKey(session.selectedCharacterID, request))
	return true, nil
}

// Called in the authenticated recipient loop after the exact final weekly
// receipt gate, while only the recipient projection mutex is held. Failed,
// single, Luke and Fiend results never advance the original Anton objective.
func (s *Service) applyCurrentAntonPrerequisiteQuestResult(session *gameSession, state runtimeRaidState) error {
	b := state.Battle
	if b == nil || b.Mode != 1 || b.Single || b.TimedOut || !b.PhaseCompleted || session.channel.Type != dnfenum.RaidChannelType {
		return nil
	}
	if b.Phase > 1 || b.PhaseCompletedAt.IsZero() || b.PhaseCompletedAt.After(b.Deadline) || !s.currentRaidAuthenticated(session) || b.MemberAccounts[session.selectedCharacterID] != s.accountIDForSession(session) {
		return fmt.Errorf("Anton prerequisite phase result owner or clock unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	s.initialEquipmentMu.Lock()
	source, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return err
	}
	rule, err := dnfquest.LoadLukeEntryRequirement(source)
	if err != nil {
		return err
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return err
	}
	repos, found := s.repositoryGroupFor(ctx)
	if !found {
		return dnfquest.ErrOwnerUnavailable
	}
	owner, err := dnfquest.NewOwner(repos)
	if err != nil {
		return err
	}
	result, err := owner.ApplyAntonPrerequisitePhaseClear(ctx, catalog, rule.AntonQuestID, dnfquest.AntonPrerequisiteClear{
		CharacterID: strconv.FormatUint(uint64(session.selectedCharacterID), 10), AccountID: b.MemberAccounts[session.selectedCharacterID], InstanceID: b.InstanceID,
		Phase: b.Phase, CompletedAt: b.PhaseCompletedAt, ResetHour: 6,
	})
	if err != nil || !result.Changed {
		return err
	}
	session.clearCurrentSetQuestTriggerReplayForQuest(session.selectedCharacterID, uint16(result.QuestID))
	request := dnfquest.SetTriggerRequest{QuestID: uint16(result.QuestID), TriggerType: byte(16 << b.Phase)}
	if err := s.sendCurrentSetQuestTriggerSuccess(session, request, dnfquest.PlanResult{QuestID: result.QuestID, Known: true, StateChanged: true, Status: "active", ProgressValue: result.ProgressValue}, "owned_normal_anton_phase_clear_after_daily_commit"); err != nil {
		session.raidProjection.failed = true
		if session.conn != nil {
			_ = session.conn.Close()
		}
		return err
	}
	if err := s.sendCurrentActiveQuestSnapshotForSession(session, "owned_normal_anton_phase_clear_after_daily_commit"); err != nil {
		session.raidProjection.failed = true
		if session.conn != nil {
			_ = session.conn.Close()
		}
		return err
	}
	return nil
}
