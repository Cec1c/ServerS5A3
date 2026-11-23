package dnfbridge

import (
	"context"
	"strconv"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// partyFrameCharacterBatch owns repository results for one synchronous party
// frame build. It is deliberately not stored on Service: a dropped request
// must not seed a later roster with stale character rows.
type partyFrameCharacterBatch struct {
	service *Service
	entries map[uint16]partyFrameCharacterBatchEntry
}

type partyFrameCharacterBatchEntry struct {
	identity     boundGameSessionCharacter
	characterID  uint16
	name         string
	character    dnfrepo.CharacterRecord
	hasCharacter bool
}

func newPartyFrameCharacterBatch(service *Service) *partyFrameCharacterBatch {
	return &partyFrameCharacterBatch{
		service: service,
		entries: make(map[uint16]partyFrameCharacterBatchEntry),
	}
}

func currentPartyFrameCharacterBatch(service *Service, batches []*partyFrameCharacterBatch) *partyFrameCharacterBatch {
	if len(batches) > 0 && batches[0] != nil {
		return batches[0]
	}
	return newPartyFrameCharacterBatch(service)
}

// Creation, binding and the adjacent op9 record describe the same actor. Reuse
// that already loaded record only inside this synchronous construction. Later
// retries create a new batch; replacement sessions and foreign owners cannot
// seed it. Dynamic party/run/owner fences still execute before writing frames.
func (batch *partyFrameCharacterBatch) seedProjectionCharacter(identity boundGameSessionCharacter, name string, character dnfrepo.CharacterRecord, found bool) bool {
	if batch == nil || batch.service == nil || !found || identity.character == 0 || identity.session == nil ||
		!batch.service.boundGameSessionCharacterCurrent(identity) ||
		character.CharacterID != strconv.Itoa(int(identity.character)) || character.AccountID == "" ||
		character.AccountID != batch.service.accountIDForSession(identity.session) {
		return false
	}
	if batch.entries == nil {
		batch.entries = make(map[uint16]partyFrameCharacterBatchEntry)
	}
	batch.entries[identity.character] = partyFrameCharacterBatchEntry{
		identity: identity, characterID: identity.character, name: name,
		character: dnfrepo.CloneCharacter(character), hasCharacter: true,
	}
	return true
}

// The op9 table serializes only character identity, name, job, level and grow
// state. Loading an enter aggregate also reads equipment and probes/repairs
// unrelated initialization for every member. Keep the authoritative full
// character read, but leave that initialization to the actual enter path.
// Results belong only to this synchronous batch and exact live binding.
func (batch *partyFrameCharacterBatch) loadCharacter(
	ctx context.Context,
	identity boundGameSessionCharacter,
) (uint16, string, dnfrepo.CharacterRecord, bool) {
	if batch == nil || batch.service == nil {
		return identity.character, csharpSelectCharacterName(identity.character), dnfrepo.CharacterRecord{}, false
	}
	if entry, ok := batch.entries[identity.character]; ok && entry.identity == identity {
		return entry.characterID, entry.name, entry.character, entry.hasCharacter
	}

	characterID := identity.character
	name := csharpSelectCharacterName(characterID)
	var character dnfrepo.CharacterRecord
	var hasCharacter bool
	ctx = batch.service.withPartyLoadingSnapshot(ctx, identity.session)
	if repos, ok := batch.service.repositoryGroupFor(ctx); ok && repos.Character != nil && characterID != 0 {
		var err error
		character, hasCharacter, err = repos.Character.Load(ctx, strconv.Itoa(int(characterID)))
		if err != nil {
			batch.service.logGameEvent(identity.session, "game-party-frame-character-load-failed", "character_id", characterID, "error", err)
			hasCharacter = false
		}
		if hasCharacter && character.Name != "" {
			name = character.Name
		}
	}
	batch.entries[identity.character] = partyFrameCharacterBatchEntry{
		identity:     identity,
		characterID:  characterID,
		name:         name,
		character:    character,
		hasCharacter: hasCharacter,
	}
	return characterID, name, character, hasCharacter
}
