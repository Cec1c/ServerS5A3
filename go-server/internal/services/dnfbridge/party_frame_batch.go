package dnfbridge

import (
	"context"

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

// loadCharacter preserves characterForEnter's repository semantics while
// reusing one result for the same current session binding. A reconnect or
// character-generation change invalidates that binding's cached row; the
// caller's existing current-binding checks still decide whether to send it.
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

	characterID, name, character, hasCharacter := batch.service.characterForEnter(
		ctx,
		identity.session,
		identity.character,
	)
	batch.entries[identity.character] = partyFrameCharacterBatchEntry{
		identity:     identity,
		characterID:  characterID,
		name:         name,
		character:    character,
		hasCharacter: hasCharacter,
	}
	return characterID, name, character, hasCharacter
}
