package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"time"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Current abd5054f EXE: 3331022 calls 1C94700(mode=1), which sends
// class1/1427 with selector zero. The boolean ACK callback 1FC0100 invokes
// 1CAECE0; only success emits the bodyless 1454. These are separate from
// the older profile's 1429/13-byte transaction response.
const (
	currentQuestManualCheckOpcode   = uint16(1427)
	currentQuestManualCommitOpcode  = uint16(1454)
	currentQuestManualReadyLifetime = time.Minute
)

func currentQuestManualCheckHandler(s *Service, session *gameSession, request gameplayRequest) error {
	if s.options.currentClientVersion != "official-24.9" || !requireDefaultGameplayClassification(s, session, request, "game-current-quest-manual-blocked", "preflight_class_mismatch") {
		return nil
	}
	return s.handleCurrentQuestManualCheck(session, request.Body)
}

func currentQuestManualCommitHandler(s *Service, session *gameSession, request gameplayRequest) error {
	if s.options.currentClientVersion != "official-24.9" || !requireDefaultGameplayClassification(s, session, request, "game-current-quest-manual-blocked", "commit_class_mismatch") {
		return nil
	}
	return s.handleCurrentQuestManualCommit(session, request.Body)
}

func (s *Service) currentQuestManualOwner(ctx context.Context, session *gameSession) (dnfrepo.Group, dnfrepo.CharacterRecord, boundGameSessionCharacter, error) {
	if session == nil || !session.loginPacketSeen || !session.accountFromToken || strings.TrimSpace(session.accountID) == "" {
		return dnfrepo.Group{}, dnfrepo.CharacterRecord{}, boundGameSessionCharacter{}, errors.New("authenticated account required")
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	repos, available := s.repositoryGroup()
	if !bound || !available || repos.Character == nil || repos.Quest == nil || repos.CharacterSettlement == nil {
		return repos, dnfrepo.CharacterRecord{}, identity, errors.New("bound quest owner unavailable")
	}
	character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(identity.character)))
	if err != nil || !found || character.AccountID != s.accountIDForSession(session) || !s.boundGameSessionCharacterCurrent(identity) {
		return repos, character, identity, errors.New("quest owner mismatch or stale generation")
	}
	return repos, character, identity, nil
}

func currentQuestManualEligibility(character dnfrepo.CharacterRecord) (dnfquest.CharacterEligibility, int, bool) {
	job, ok := characterJobByte(character)
	cutoff := character.Level - 1
	if cutoff > currentAutoCompleteMainQuestMaxLevel {
		cutoff = currentAutoCompleteMainQuestMaxLevel
	}
	return dnfquest.CharacterEligibility{Level: character.Level, Job: int(job), GrowType: characterGrowTypeInt(character)}, cutoff, ok && cutoff > 0
}

func (s *Service) handleCurrentQuestManualCheck(session *gameSession, body []byte) error {
	if session == nil {
		return nil
	}
	session.questManualClearReadyAt = time.Time{}
	session.questManualClearIdentity = boundGameSessionCharacter{}
	if len(body) != 4 || binary.LittleEndian.Uint32(body) != 0 {
		return s.sendGameUpperFailure(session, currentQuestManualCheckOpcode, 3)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repos, character, identity, err := s.currentQuestManualOwner(ctx, session)
	if err != nil {
		s.logGameEvent(session, "game-current-quest-manual-check-rejected", "reason", err)
		return nil
	}
	eligibility, cutoff, valid := currentQuestManualEligibility(character)
	catalog, err := s.loadQuestCatalog(ctx)
	if !valid || err != nil {
		return s.sendGameUpperFailure(session, currentQuestManualCheckOpcode, 1)
	}
	record, _, err := repos.Quest.Load(ctx, character.CharacterID)
	if err == nil {
		_, err = catalog.PlanAutoCompleteMainQuests(record, eligibility, cutoff, 0, s.questClearTicketScope(), s.gameplayNow())
	}
	if err != nil || !s.boundGameSessionCharacterCurrent(identity) {
		return s.sendGameUpperFailure(session, currentQuestManualCheckOpcode, 1)
	}
	session.questManualClearIdentity = identity
	session.questManualClearReadyAt = s.gameplayNow()
	s.logGameEvent(session, "game-current-quest-manual-check-ready", "char_id", identity.character, "cutoff_level", cutoff, "quest_mutation", "none")
	return s.sendGameUpperSuccess(session, currentQuestManualCheckOpcode, nil)
}

func (s *Service) handleCurrentQuestManualCommit(session *gameSession, body []byte) error {
	if session == nil || len(body) != 0 {
		return nil
	}
	identity, readyAt := session.questManualClearIdentity, session.questManualClearReadyAt
	session.questManualClearIdentity = boundGameSessionCharacter{}
	session.questManualClearReadyAt = time.Time{}
	now := s.gameplayNow()
	if readyAt.IsZero() || now.Before(readyAt) || now.Sub(readyAt) > currentQuestManualReadyLifetime || !s.boundGameSessionCharacterCurrent(identity) {
		s.logGameEvent(session, "game-current-quest-manual-commit-rejected", "reason", "missing_expired_or_stale_preflight")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repos, character, current, err := s.currentQuestManualOwner(ctx, session)
	if err != nil || current != identity {
		return nil
	}
	eligibility, cutoff, valid := currentQuestManualEligibility(character)
	catalog, err := s.loadQuestCatalog(ctx)
	if !valid || err != nil {
		return nil
	}
	owner, err := dnfquest.NewOwner(repos)
	if err != nil {
		return err
	}
	result, err := owner.ApplyAutoCompleteMain(ctx, catalog, dnfquest.AutoCompleteMainInput{
		CharacterID: character.CharacterID, Eligibility: eligibility, CutoffLevel: cutoff,
		Scope: s.questClearTicketScope(), CompletedAt: now,
	})
	if err != nil {
		s.logGameEvent(session, "game-current-quest-manual-commit-rejected", "reason", err)
		return nil
	}
	// Class0/1454 has an unrelated two-u32 UI notification (B646A0).
	// Do not invent its counters or reuse the legacy 13-byte ACK. The
	// committed quest bitmap and actual active/acceptable lists are the
	// authoritative client state for this bodyless completion command.
	s.logGameEvent(session, "game-current-quest-manual-committed", "char_id", identity.character,
		"cutoff_level", cutoff, "changed_main_count", len(result.ChangedQuestIDs),
		"changed_sub_count", len(result.ChangedLinkedSubIDs), "reward_granted", false, "item_consumed", false)
	// Native op574 with no active rows clears the completed-prerequisite
	// lookup. Publish it before op356 restores the committed bitmap.
	if err := s.sendCurrentActiveQuestSnapshotForSession(session, "current_manual_1454_committed"); err != nil {
		return err
	}
	if err := s.sendCurrentClearQuestListFromCommittedQuest(session, result.PostCommitQuest, "current_manual_1454_committed"); err != nil {
		return err
	}
	return s.sendCurrentAcceptableQuestListOnlyForSession(session, "current_manual_1454_committed")
}
