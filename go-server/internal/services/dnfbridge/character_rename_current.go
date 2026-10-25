package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/characterdata"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// These two flags are read by 022c17e0 immediately after the row's DSTR.
// 036d6500 indexes the first vector by selector and opens window 1b0 only for
// value 1. The second value selects a callback; a paid pending rename uses 0.
func currentCharacterRenamePending(character dnfrepo.CharacterRecord) bool {
	return character.Stats[characterdata.RenameEntitlementStat] == 1
}

func strictCurrentRenameName(raw []byte) (string, error) {
	if len(raw) > 30 || bytes.IndexByte(raw, 0) >= 0 {
		return "", characterdata.ErrRenameUnavailable
	}
	name, err := decodeCharacterName(raw)
	if err != nil {
		return "", err
	}
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(name))
	if err != nil || !bytes.Equal(raw, encoded) {
		return "", characterdata.ErrRenameUnavailable
	}
	if err := characterdata.ValidateRenameName(name); err != nil {
		return "", err
	}
	return name, nil
}

func (s *Service) handleCurrentCharacterRename(session *gameSession, body []byte) error {
	op := uint16(dnfenum.CmdPacketChangeCharacName)
	fail := func(err error) error {
		s.logGameEvent(session, "game-character-rename-rejected", "error", err)
		code := byte(2)
		if errors.Is(err, dnfrepo.ErrCharacterNameExists) {
			// 当前EXE的op135用0x14提示重名；0x02会误报数据库问题。
			code = 0x14
		}
		return s.sendGameUpperFailure(session, op, code)
	}
	request, err := decodeCharacterRenameRequest(body)
	if err != nil {
		return fail(err)
	}
	// Modal builders use tail 1 or 2. Neither byte grants a rename; only the
	// stored entitlement and the authenticated account can authorize it.
	if request.Tail != 1 && request.Tail != 2 && request.Tail != 4 {
		return fail(characterdata.ErrRenameUnavailable)
	}
	oldName, err := strictCurrentRenameName(request.OldNameRaw)
	if err != nil {
		return fail(err)
	}
	newName, err := strictCurrentRenameName(request.NewNameRaw)
	if err != nil {
		return fail(err)
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return fail(errCharacterRepositoryMissing)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if session == nil {
		return fail(characterdata.ErrRenameUnavailable)
	}
	s.mu.Lock()
	accountID := strings.TrimSpace(session.accountID)
	selected := session.selectedCharacterID
	s.mu.Unlock()
	if accountID == "" || !session.loginPacketSeen || selected != 0 {
		return fail(characterdata.ErrRenameUnavailable)
	}
	// The modal indexes the compact client row, not the SQL slot. Resolve
	// against the exact successfully sent roster, then recheck SQL slot/owner
	// inside the mutation transaction. Holes in a roster must not rename a
	// different character after deletion/reordering.
	selector := int(request.Flag)
	if selector >= len(session.currentRenameRoster) {
		return fail(characterdata.ErrRenameUnavailable)
	}
	row := session.currentRenameRoster[selector]
	if row.AccountID != accountID || row.Name != oldName {
		return fail(characterdata.ErrRenameUnavailable)
	}
	characterID, err := strconv.ParseUint(row.CharacterID, 10, 16)
	if err != nil || characterID == 0 {
		return fail(characterdata.ErrRenameUnavailable)
	}
	reservation, err := s.reserveCurrentSelectorRename(ctx, session, uint16(characterID), accountID)
	if err != nil {
		return fail(err)
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	err = characterdata.Rename(ctx, repos, characterdata.RenameRequest{AccountID: accountID, CharacterID: row.CharacterID, Slot: row.Slot, OldName: oldName, NewName: newName, Now: s.gameplayNow().UTC()})
	if err != nil {
		return fail(err)
	}
	s.logGameEvent(session, "game-character-rename-committed", "character_id", row.CharacterID, "selector", selector, "slot", row.Slot)
	return s.sendGameUpperSuccess(session, op, nil)
}

// Freeze an unselected selector owner through SQL commit and ACK. Canonical
// bind/promote/reset writers already wait for this reservation, preventing a
// second connection from entering the same character with a stale name.
func (s *Service) reserveCurrentSelectorRename(ctx context.Context, session *gameSession, character uint16, account string) (*currentCharacterAssetReservation, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		if session == nil || session.selectedCharacterID != 0 || session.accountID != account || s.gameSessions[character] != nil {
			s.mu.Unlock()
			return nil, characterdata.ErrRenameUnavailable
		}
		if prior := s.characterAssetReservations[character]; prior != nil {
			done := prior.done
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if s.characterAssetReservations == nil {
			s.characterAssetReservations = make(map[uint16]*currentCharacterAssetReservation)
		}
		r := &currentCharacterAssetReservation{identity: boundGameSessionCharacter{session: session, character: character, generation: session.characterGeneration}, account: account, done: make(chan struct{})}
		s.characterAssetReservations[character] = r
		s.mu.Unlock()
		return r, nil
	}
}

func (s *Service) rememberCurrentRenameRoster(session *gameSession, characters []dnfrepo.CharacterRecord) {
	if s.options.currentClientVersion != "official-24.9" {
		return
	}
	session.currentRenameRoster = append([]dnfrepo.CharacterRecord(nil), characters[:rosterCount(characters)]...)
}

// removeCurrentSelectorRosterCharacter applies the same compaction the current
// client performs when op6 succeeds. SQL rows keep their original slots; only
// the immutable row-to-character binding sent to this session is compacted.
func (s *Service) removeCurrentSelectorRosterCharacter(session *gameSession, characterID string) {
	if session == nil {
		return
	}
	for index, row := range session.currentRenameRoster {
		if row.CharacterID != characterID {
			continue
		}
		copy(session.currentRenameRoster[index:], session.currentRenameRoster[index+1:])
		session.currentRenameRoster = session.currentRenameRoster[:len(session.currentRenameRoster)-1]
		return
	}
}

// Check the canonical SQL slot and reject enter-game while its rename is
// pending. A forged op4 must not bypass the selector modal and use the old name.
func currentCharacterRenameSelectionAllowed(character dnfrepo.CharacterRecord) error {
	if currentCharacterRenamePending(character) {
		return fmt.Errorf("%w: character %s must choose a name", characterdata.ErrRenameUnavailable, character.CharacterID)
	}
	return nil
}

// A select request may have resolved its row before waiting for a paid rename
// reservation. Re-read after bind, before any selected-character snapshots or
// bootstrap writes, so it cannot enter with the pre-rename name or pending bit.
func (s *Service) currentRenameSelectionStillCurrent(ctx context.Context, session *gameSession, selected dnfrepo.CharacterRecord) bool {
	if s.options.currentClientVersion != "official-24.9" {
		return true
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return false
	}
	current, found, err := repos.Character.Load(ctx, selected.CharacterID)
	if err != nil {
		reason := "repository_read_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "snapshot_deadline_exceeded"
		} else if errors.Is(err, context.Canceled) {
			reason = "snapshot_canceled"
		}
		s.logWarn("dnfbridge character selection revalidation failed", "character_id", selected.CharacterID, "reason", reason)
		return false
	}
	return found && current.AccountID == selected.AccountID && current.Slot == selected.Slot && current.Name == selected.Name && !currentCharacterRenamePending(current)
}
