package dnfbridge

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"testing"
)

func TestPartyProjectionCharacterSeedIsDetachedAndBindingScoped(t *testing.T) {
	f := newPartyFrameBatchRosterFixture(t, 2)
	for _, session := range f.sessions {
		session.accountID = "batch-parties"
	}
	identity, found := f.service.boundGameSessionCharacterSnapshot(f.sessions[0])
	if !found {
		t.Fatal("missing canonical binding")
	}
	character := f.store.records[strconv.Itoa(int(identity.character))]
	character.Stats["grow_type"] = 7
	batch := newPartyFrameCharacterBatch(f.service)
	if !batch.seedProjectionCharacter(identity, character.Name, character, true) {
		t.Fatal("valid projection not seeded")
	}
	character.Stats["grow_type"] = 9
	_, name, got, ok := batch.loadCharacter(context.Background(), identity)
	if !ok || name != character.Name || got.Stats["grow_type"] != 7 || len(f.counter.loadIDs) != 0 {
		t.Fatal(name, got, ok, f.counter.loadIDs)
	}
	for _, mode := range []string{"absent", "foreign-account", "wrong-id"} {
		invalid := r.CloneCharacter(character)
		exists := true
		switch mode {
		case "absent":
			exists = false
		case "foreign-account":
			invalid.AccountID = "foreign"
		case "wrong-id":
			invalid.CharacterID = "missing"
		}
		if batch.seedProjectionCharacter(identity, invalid.Name, invalid, exists) {
			t.Fatal("invalid seed admitted", mode)
		}
	}
	// A later request owns a new batch and must observe the durable source.
	_, _, _, _ = newPartyFrameCharacterBatch(f.service).loadCharacter(context.Background(), identity)
	if len(f.counter.loadIDs) != 1 {
		t.Fatal("new request reused source", f.counter.loadIDs)
	}
	replacement := &gameSession{conn: &bufferConn{}, accountID: "batch-parties", selectedCharacterID: identity.character, characterGeneration: identity.generation + 1}
	f.service.gameSessions[identity.character] = replacement
	if batch.seedProjectionCharacter(identity, character.Name, character, true) {
		t.Fatal("retired generation seeded")
	}
	latest, found := f.service.boundGameSessionCharacterSnapshot(replacement)
	if !found {
		t.Fatal("missing replacement")
	}
	batch.loadCharacter(context.Background(), latest)
	if len(f.counter.loadIDs) != 2 {
		t.Fatal("new generation reused old record", f.counter.loadIDs)
	}
}
