package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"sync"

	"longheng.io/server/internal/modules/dnf/raid"
)

// currentRaidProfileSendView is connection-local. A replacement connection
// starts empty and therefore receives the complete profile set once. Within
// one live connection it records only successfully written identities so an
// unchanged table reopen sends no profile packet, while a join can send only
// the new member to already primed viewers.
type currentRaidProfileSendView struct {
	mu          sync.Mutex
	characterID uint16
	generation  uint64
	account     string
	raidKey     uint32
	scene       uint64
	profiles    map[uint16]string
	appearances map[uint16]string
}

func currentRaidProfileMemberSignature(state runtimeRaidState, member runtimeRaidMemberState) string {
	// Admission owns this identity snapshot. The signature invalidates the
	// short-lived repository memo when the authoritative member row or its raid
	// namespace changes, while repeated opens/joins/exits reuse the same values.
	return fmt.Sprintf("%d|%d|%d|%d|%d|%q",
		state.RaidKey, member.CharID, member.Level, member.Job, member.GrowType, member.Name)
}

func (s *Service) currentRaidProfileAppearanceSignatures(state runtimeRaidState) map[uint16]string {
	signatures := make(map[uint16]string, len(state.Members))
	for _, member := range state.Members {
		signature := currentRaidProfileMemberSignature(state, member) + "|offline"
		if actor, ok := s.onlineGameSession(member.CharID); ok {
			signature = fmt.Sprintf("%s|%d", currentRaidProfileMemberSignature(state, member), currentCrossSceneAppearanceRevision(actor))
		}
		signatures[member.CharID] = signature
	}
	return signatures
}

func (v *currentRaidProfileSendView) resetLocked(
	characterID uint16,
	generation uint64,
	account string,
	raidKey uint32,
	scene uint64,
) {
	v.characterID = characterID
	v.generation = generation
	v.account = account
	v.raidKey = raidKey
	v.scene = scene
	v.profiles = nil
	v.appearances = nil
}

func (v *currentRaidProfileSendView) plan(
	state runtimeRaidState,
	characterID uint16,
	generation uint64,
	account string,
	scene uint64,
	appearanceSignatures map[uint16]string,
	actors map[uint16]bool,
) (map[uint16]string, map[uint16]bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.characterID != characterID || v.generation != generation ||
		v.account != account || v.raidKey != state.RaidKey || v.scene != scene {
		v.resetLocked(characterID, generation, account, state.RaidKey, scene)
	}
	first := v.profiles == nil
	profiles := make(map[uint16]string)
	appearances := make(map[uint16]bool)
	for _, member := range state.Members {
		if member.CharID == 0 {
			continue
		}
		signature := currentRaidProfileMemberSignature(state, member)
		requested := actors == nil
		if actors != nil {
			requested = actors[member.CharID]
		}
		// The first packet for this connection is complete even when the caller
		// requested an appearance subset. Later sends only include changed rows.
		if first || (requested && v.profiles[member.CharID] != signature) {
			profiles[member.CharID] = signature
		}
		appearanceSignature := appearanceSignatures[member.CharID]
		if member.CharID != characterID && requested &&
			(v.appearances == nil || v.appearances[member.CharID] != appearanceSignature) {
			appearances[member.CharID] = true
		}
	}
	return profiles, appearances
}

func (v *currentRaidProfileSendView) markProfiles(
	state runtimeRaidState,
	characterID uint16,
	generation uint64,
	account string,
	scene uint64,
	members map[uint16]struct{},
) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.characterID != characterID || v.generation != generation ||
		v.account != account || v.raidKey != state.RaidKey || v.scene != scene {
		return
	}
	if v.profiles == nil {
		v.profiles = make(map[uint16]string, len(state.Members))
	}
	for _, member := range state.Members {
		if _, sent := members[member.CharID]; !sent {
			continue
		}
		v.profiles[member.CharID] = currentRaidProfileMemberSignature(state, member)
	}
}

func (v *currentRaidProfileSendView) markAppearances(
	state runtimeRaidState,
	characterID uint16,
	generation uint64,
	account string,
	scene uint64,
	sent map[uint16]bool,
	appearanceSignatures map[uint16]string,
) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(sent) == 0 || v.characterID != characterID || v.generation != generation ||
		v.account != account || v.raidKey != state.RaidKey || v.scene != scene {
		return
	}
	if v.appearances == nil {
		v.appearances = make(map[uint16]string, len(state.Members))
	}
	for _, member := range state.Members {
		if sent[member.CharID] {
			v.appearances[member.CharID] = appearanceSignatures[member.CharID]
		}
	}
}

func (v *currentRaidProfileSendView) forget(state runtimeRaidState, characterID uint16) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.raidKey != state.RaidKey {
		return
	}
	delete(v.profiles, characterID)
	delete(v.appearances, characterID)
}

func (v *currentRaidProfileSendView) clear() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.characterID = 0
	v.generation = 0
	v.account = ""
	v.raidKey = 0
	v.scene = 0
	v.profiles = nil
	v.appearances = nil
}

// Raid UI actors live in the client's category1 cache, not its town actor
// manager. Supply that cache before any roster lookup, using real repository
// properties and the authorized directory object's advertised namespace.
func (s *Service) sendCurrentRuntimeRaidProfiles(session *gameSession, state runtimeRaidState) error {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	return s.sendCurrentRuntimeRaidProfilesIncrementalWithBatchForActors(session, state, newCurrentRaidProfileBatch(s, ctx), nil)
}

func newCurrentRaidProfileBatch(s *Service, ctx context.Context) *currentCrossSceneProfileBatch {
	batch := newCurrentCrossSceneProfileBatch(s, ctx)
	batch.completeAdmittedWrite = true
	return batch
}

func (s *Service) sendCurrentRuntimeRaidProfilesWithBatch(session *gameSession, state runtimeRaidState, batch *currentCrossSceneProfileBatch) error {
	return s.sendCurrentRuntimeRaidProfilesWithBatchForActors(session, state, batch, nil)
}

func (s *Service) sendCurrentRuntimeRaidProfilesWithBatchForActors(session *gameSession, state runtimeRaidState, batch *currentCrossSceneProfileBatch, actors map[uint16]bool) error {
	peer, err := batch.peer(session)
	if err != nil {
		return raidProfilePreparationError(err)
	}
	visible := func() bool {
		return peer != nil && peer.current(s) && session.loginPacketSeen && session.accountFromToken &&
			peer.account == s.accountIDForSession(session) && s.currentRaidInfoContextVisible(session, state)
	}
	if !visible() {
		return nil
	}
	if !s.currentRaidInfoContextVisible(session, state) {
		return nil
	}
	appearanceSignatures := s.currentRaidProfileAppearanceSignatures(state)
	// Full delivery is also the first observation for a newly bound connection.
	// Initialize the same per-connection view used by incremental delivery before
	// marking successful rows, otherwise the first reopen payload cannot be
	// suppressed until another incremental send happens.
	session.raidProfileView.plan(
		state, session.selectedCharacterID, session.characterGeneration, s.accountIDForSession(session),
		session.sceneBootstrapActorGeneration, appearanceSignatures, actors,
	)
	body, err := s.currentRuntimeRaidProfileBody(state, batch)
	if err != nil {
		return raidProfilePreparationError(err)
	}
	if len(body) > 0 && visible() {
		s.logGameEvent(session, "game-current-raid-profiles-sent", "raid_key", state.RaidKey, "profile_count", (len(body)-4)/57, "member_count", len(state.Members), "delivery", "full")
		if err := s.sendGameUpperRawClassCodec(session, raid.CurrentRaidProfilesMsgID, body, 0, true); err != nil {
			return err
		}
		session.raidProfileView.markProfiles(
			state, session.selectedCharacterID, session.characterGeneration, s.accountIDForSession(session), session.sceneBootstrapActorGeneration,
			currentRaidProfileBodyMemberSet(body),
		)
	}
	if visible() {
		sent := s.sendCurrentRaidCrossSceneProfilesForActors(batch, session, state, actors)
		session.raidProfileView.markAppearances(
			state, session.selectedCharacterID, session.characterGeneration, s.accountIDForSession(session),
			session.sceneBootstrapActorGeneration, sent, appearanceSignatures,
		)
	}
	return nil
}

func (s *Service) sendCurrentRuntimeRaidProfilesIncrementalWithBatchForActors(session *gameSession, state runtimeRaidState, batch *currentCrossSceneProfileBatch, actors map[uint16]bool) error {
	peer, err := batch.peer(session)
	if err != nil {
		return raidProfilePreparationError(err)
	}
	visible := func() bool {
		return peer != nil && peer.current(s) && session.loginPacketSeen && session.accountFromToken &&
			peer.account == s.accountIDForSession(session) && s.currentRaidInfoContextVisible(session, state)
	}
	if !visible() {
		return nil
	}
	appearanceSignatures := s.currentRaidProfileAppearanceSignatures(state)
	profileMembers, appearanceMembers := session.raidProfileView.plan(
		state, session.selectedCharacterID, session.characterGeneration, s.accountIDForSession(session),
		session.sceneBootstrapActorGeneration, appearanceSignatures, actors,
	)
	if len(profileMembers) == 0 && len(appearanceMembers) == 0 {
		return nil
	}
	if len(profileMembers) > 0 {
		body, err := s.currentRuntimeRaidProfileBodyForMembers(state, batch, profileMembers)
		if err != nil {
			return raidProfilePreparationError(err)
		}
		if len(body) == 0 || !visible() {
			return nil
		}
		s.logGameEvent(session, "game-current-raid-profiles-sent", "raid_key", state.RaidKey, "profile_count", (len(body)-4)/57, "member_count", len(state.Members), "delivery", "incremental")
		if err := s.sendGameUpperRawClassCodec(session, raid.CurrentRaidProfilesMsgID, body, 0, true); err != nil {
			return err
		}
		session.raidProfileView.markProfiles(
			state, session.selectedCharacterID, session.characterGeneration, s.accountIDForSession(session), session.sceneBootstrapActorGeneration,
			currentRaidProfileBodyMemberSet(body),
		)
	}
	if len(appearanceMembers) > 0 && visible() {
		sent := s.sendCurrentRaidCrossSceneProfilesForActors(batch, session, state, appearanceMembers)
		session.raidProfileView.markAppearances(
			state, session.selectedCharacterID, session.characterGeneration, s.accountIDForSession(session),
			session.sceneBootstrapActorGeneration, sent, appearanceSignatures,
		)
	}
	return nil
}

func (s *Service) currentRuntimeRaidProfileBody(state runtimeRaidState, batch *currentCrossSceneProfileBatch) ([]byte, error) {
	return s.currentRuntimeRaidProfileBodyForMembers(state, batch, nil)
}

func (s *Service) currentRuntimeRaidProfileBodyForMembers(
	state runtimeRaidState,
	batch *currentCrossSceneProfileBatch,
	members map[uint16]string,
) ([]byte, error) {
	if members != nil && len(members) == 0 {
		return nil, nil
	}
	if batch.raidProfileRows == nil {
		batch.raidProfileRows = make(map[uint32]map[uint16]raid.CurrentProfileRecord)
	}
	rowsByMember := batch.raidProfileRows[state.RaidKey]
	if rowsByMember == nil {
		rowsByMember = make(map[uint16]raid.CurrentProfileRecord)
		batch.raidProfileRows[state.RaidKey] = rowsByMember
	}
	repos, available := s.repositoryGroupFor(batch.ctx)
	rows := make([]raid.CurrentProfileRecord, 0, len(state.Members))
	ctx, cancel := context.WithTimeout(batch.ctx, createWriteTimeout)
	defer cancel()
	for _, member := range state.Members {
		if member.CharID == 0 {
			continue
		}
		if members != nil {
			if _, wanted := members[member.CharID]; !wanted {
				continue
			}
		}
		if row, cached := rowsByMember[member.CharID]; cached {
			rows = append(rows, row)
			continue
		}
		if !available || repos.Character == nil {
			return nil, nil
		}
		character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(member.CharID)))
		if err != nil {
			return nil, err
		}
		if !found || character.CharacterID != strconv.Itoa(int(member.CharID)) || character.Level <= 0 || character.Level > 65535 {
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
		rowsByMember[member.CharID] = row
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	body, err := raid.BuildCurrentRaidProfilesBody(rows)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func currentRaidProfileBodyMemberSet(body []byte) map[uint16]struct{} {
	members := make(map[uint16]struct{})
	if len(body) < 4 {
		return members
	}
	count := int(binary.LittleEndian.Uint32(body[:4]))
	if len(body) != 4+57*count {
		return members
	}
	for index := 0; index < count; index++ {
		members[binary.LittleEndian.Uint16(body[4+57*index:])] = struct{}{}
	}
	return members
}
