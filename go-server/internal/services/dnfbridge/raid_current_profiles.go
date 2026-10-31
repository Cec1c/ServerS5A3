package dnfbridge

import (
	"context"
	"strconv"

	"longheng.io/server/internal/modules/dnf/raid"
)

// Raid UI actors live in the client's category1 cache, not its town actor
// manager. Supply that cache before any roster lookup, using real repository
// properties and the authorized directory object's advertised namespace.
func (s *Service) sendCurrentRuntimeRaidProfiles(session *gameSession, state runtimeRaidState) error {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	return s.sendCurrentRuntimeRaidProfilesWithBatch(session, state, newCurrentRaidProfileBatch(s, ctx))
}

func newCurrentRaidProfileBatch(s *Service, ctx context.Context) *currentCrossSceneProfileBatch {
	batch := newCurrentCrossSceneProfileBatch(s, ctx)
	batch.completeAdmittedWrite = true
	return batch
}

func (s *Service) sendCurrentRuntimeRaidProfilesWithBatch(session *gameSession, state runtimeRaidState, batch *currentCrossSceneProfileBatch) error {
	if !s.currentRaidInfoVisible(session, state) {
		return nil
	}
	repos, available := s.repositoryGroup()
	if !available || repos.Character == nil {
		return nil
	}
	rows := make([]raid.CurrentProfileRecord, 0, len(state.Members))
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	for _, member := range state.Members {
		if member.CharID == 0 {
			continue
		}
		character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(member.CharID)))
		if err != nil || !found || character.CharacterID != strconv.Itoa(int(member.CharID)) || character.Level <= 0 || character.Level > 65535 {
			continue
		}
		job, err := strconv.ParseUint(character.Job, 10, 8)
		if err != nil {
			continue
		}
		row := raid.CurrentProfileRecord{CharKey: member.CharID, Channel: byte(state.RaidKey >> 16), Server: byte(state.RaidKey >> 24), Level: uint16(character.Level), Job: byte(job), Grow: characterGrowTypeByte(character), Name: character.Name}
		// Missing/oversized names must not be replaced by a synthetic character.
		if _, err := raid.BuildCurrentRaidProfilesBody([]raid.CurrentProfileRecord{row}); err != nil {
			continue
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 || !s.currentRaidInfoVisible(session, state) {
		return nil
	}
	body, err := raid.BuildCurrentRaidProfilesBody(rows)
	if err != nil {
		return err
	}
	s.logGameEvent(session, "game-current-raid-profiles-sent", "raid_key", state.RaidKey, "profile_count", len(rows), "member_count", len(state.Members))
	if err := s.sendGameUpperRawClassCodec(session, raid.CurrentRaidProfilesMsgID, body, 0, true); err != nil {
		return err
	}
	s.sendCurrentRaidCrossSceneProfiles(batch, session, state)
	return nil
}
