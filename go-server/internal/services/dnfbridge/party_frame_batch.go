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
