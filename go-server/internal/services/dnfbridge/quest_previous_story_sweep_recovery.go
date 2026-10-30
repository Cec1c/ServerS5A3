package dnfbridge

import (
	"context"
	"fmt"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Repair old completed sweeps before building the initial acceptable or completed lists,
// so the first town scene receives the committed gate state. This uses only the
// historical requested cutoff and never starts a sweep for a new character.
func (s *Service) recoverPreviousStorySweepForSnapshot(ctx context.Context, session *gameSession, record dnfrepo.QuestRecord) (dnfrepo.QuestRecord, error) {
	if s == nil || s.options.currentClientVersion != "official-24.9" || session == nil ||
		!session.loginPacketSeen || !session.accountFromToken || (len(record.States) == 0 && len(record.Progress) == 0) {
		return record, nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.generation == 0 {
		return record, nil
	}
	repositories, character, current, err := s.currentQuestManualOwner(ctx, session)
	if err != nil {
		return record, err
	}
	eligibility, cutoff, valid := currentQuestManualEligibility(character)
	if !valid || dnfquest.PreviousMainStorySweepCutoff(record, character.Level) == 0 {
		return record, nil
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return record, fmt.Errorf("load previous story sweep recovery catalog: %w", err)
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		return record, err
	}
	result, err := owner.RecoverPreviousMainStorySweep(ctx, catalog, dnfquest.AutoCompleteMainInput{
		AccountID: character.AccountID, CharacterID: character.CharacterID,
		Eligibility: eligibility, CutoffLevel: cutoff, CompletedAt: s.gameplayNow().UTC(),
		Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(current) },
	})
	if err != nil {
		return record, err
	}
	if !s.boundGameSessionCharacterCurrent(current) {
		return record, fmt.Errorf("previous story sweep recovery binding changed before projection")
	}
	if !result.Idempotent {
		s.logGameEvent(session, "game-previous-story-sweep-recovered",
			"char_id", current.character, "cutoff_level", result.CutoffLevel,
			"changed_quests", result.ChangedQuestIDs, "changed_sub_quests", result.ChangedLinkedSubIDs,
			"blocked_questions", result.BlockedQuestionIDs, "rewards", "none",
			"projection", "committed_quest_snapshots_before_town_scene")
	}
	return result.PostCommitQuest, nil
}
