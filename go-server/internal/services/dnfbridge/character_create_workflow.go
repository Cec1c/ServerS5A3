package dnfbridge

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	"strconv"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) createCharacter(session *gameSession, req createCharacterRequest, upper bool) error {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		s.logGameEvent(session, "game-create-repository-missing", "upper", upper)
		return s.sendCreateFailure(session, upper, createCodeServerError)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()

	accountID := s.accountIDForSession(session)
	name, err := decodeCharacterName(req.nameRaw)
	if err != nil {
		s.logGameEvent(session, "game-create-name-decode-failed", "account_id", accountID, "name_len", len(req.nameRaw), "error", err)
		return s.sendCreateFailure(session, upper, createCodeServerError)
	}
	existing, err := s.listCharacters(ctx, repos, accountID)
	if err != nil {
		s.logGameEvent(session, "game-create-list-failed", "account_id", accountID, "error", err)
		return s.sendCreateFailure(session, upper, createCodeServerError)
	}
	if len(existing) >= defaultCharacterSlots {
		return s.sendCreateFailure(session, upper, createCodeSlotFull)
	}
	if duplicated, err := s.characterNameExists(ctx, repos, existing, name); err != nil {
		s.logGameEvent(session, "game-create-name-check-failed", "account_id", accountID, "name", name, "error", err)
		return s.sendCreateFailure(session, upper, createCodeServerError)
	} else if duplicated {
		return s.sendCreateFailure(session, upper, createCodeDuplicated)
	}
	characterID, err := s.nextCharacterID(ctx, repos, existing)
	if err != nil {
		s.logGameEvent(session, "game-create-id-failed", "account_id", accountID, "error", err)
		return s.sendCreateFailure(session, upper, createCodeServerError)
	}
	if upper && characterID > upperMaxCharacter {
		s.logGameEvent(session, "game-upper-create-id-too-large", "account_id", accountID, "character_id", characterID)
		return s.sendUpperCreateError(session, createCodeServerError)
	}
	slot := nextCharacterSlot(existing)
	now := time.Now().UTC()
	level := newCharacterInitialLevel
	stats := defaultCreatedCharacterStatsFromRequest(req)
	grow, private := currentPrivateCreationGrow(req.options)
	if !private && session.pvpPrivateCreate != nil && time.Now().Before(session.pvpPrivateCreate.expires) && session.pvpPrivateCreate.job == req.job && session.pvpPrivateCreate.grow == 0 && len(req.options) == 8 {
		zero := true
		for _, b := range req.options {
			zero = zero && b == 0
		}
		grow, private = 0, zero
	}
	session.pvpPrivateCreate = nil
	if private {
		plan, err := s.loadPVPPrivateCreation(req.job, grow)
		if err != nil {
			return s.sendCreateFailure(session, upper, createCodeServerError)
		}
		if _, err := plan.Check(ctx, repos.Character, accountID, now); err != nil {
			var rejection dnfpvp.PrivateCreateError
			if errors.As(err, &rejection) {
				return s.sendCreateFailure(session, upper, byte(rejection))
			}
			return s.sendCreateFailure(session, upper, createCodeServerError)
		}
		s.initialEquipmentMu.Lock()
		archive, err := s.initialEquipmentArchiveLocked()
		s.initialEquipmentMu.Unlock()
		if err != nil {
			return s.sendCreateFailure(session, upper, createCodeServerError)
		}
		levels, err := progression.Load(ctx, archive)
		if err != nil {
			return s.sendCreateFailure(session, upper, createCodeServerError)
		}
		exp, err := levels.ThresholdToNext(plan.Level - 1)
		if err != nil {
			return s.sendCreateFailure(session, upper, createCodeServerError)
		}
		level = plan.Level
		stats[dnfpvp.PrivateCharacterKey], stats["grow_type"] = 1, int64(grow)
		stats["exp"], stats["tutorial_completed"] = int64(exp), 1
	}
	record := dnfrepo.CharacterRecord{
		CharacterID: strconv.Itoa(characterID),
		AccountID:   accountID,
		Slot:        slot,
		Name:        name,
		Job:         strconv.Itoa(int(req.job)),
		Level:       level,
		Stats:       stats,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.saveNewCharacter(ctx, repos, record); err != nil {
		s.logGameEvent(session, "game-create-save-failed", "account_id", accountID, "character_id", characterID, "error", err)
		if errors.Is(err, dnfrepo.ErrCharacterNameExists) {
			return s.sendCreateFailure(session, upper, createCodeDuplicated)
		}
		return s.sendCreateFailure(session, upper, createCodeServerError)
	}
	updated, err := s.listCharacters(ctx, repos, accountID)
	if err != nil {
		updated = append(existing, record)
	}
	if err := s.sendCreateSuccess(session, upper, record, updated); err != nil {
		return err
	}
	s.logGameEvent(session, "game-create-success",
		"upper", upper,
		"account_id", accountID,
		"character_id", characterID,
		"slot", slot,
		"job", req.job,
		"name", name,
		"option_len", len(req.options))
	return nil
}

func currentPrivateCreationGrow(options []byte) (byte, bool) {
	if len(options) != 8 || options[6] < 1 || options[6] > 5 || options[7] != 0 {
		return 0, false
	}
	for _, b := range options[:6] {
		if b != 0 {
			return 0, false
		}
	}
	return options[6], true
}
