package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
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

func TestPartyProjectionSixRecoveryRoundsReuseOnlyCurrentActorConstruction(t *testing.T) {
	f, q := peerRecoveryFixture(t)
	loads := wrapCurrentCrossSceneProfileBatchCharacterLoads(t, f.service, nil)
	key, _ := f.service.currentDungeonPartyRunKey(f.leader)
	f.service.scheduleDungeonPeerActorRecovery(f.leader, f.member, key)
	hash := sha256.New()
	for i := 0; i < 6; i++ {
		q.fire(q.task(t, i), false)
		frames := peerRecoveryFrames(t, f.leaderConn.write.Bytes())
		created, bound, roster := r160UpperPeerActorCounts(frames, f.member.selectedCharacterID)
		if created != 1 || bound != 1 || roster != 1 {
			t.Fatal(i, created, bound, roster)
		}
		for _, frame := range frames {
			var header [6]byte
			binary.LittleEndian.PutUint16(header[:], frame.msgID)
			binary.LittleEndian.PutUint32(header[2:], uint32(len(frame.body)))
			hash.Write(header[:])
			hash.Write(frame.body)
		}
		f.leaderConn.write.Reset()
	}
	t.Logf("six-round-frame-body-sha256=%x character-loads=%v", hash.Sum(nil), loads)
	actorID := strconv.Itoa(int(f.member.selectedCharacterID))
	receiverID := strconv.Itoa(int(f.leader.selectedCharacterID))
	if loads[actorID] != 12 || loads[receiverID] != 6 {
		t.Fatal("redundant op9 source read", loads)
	}
}

func TestPartyProjectionRecoveryReloadsAfterDurableCharacterChange(t *testing.T) {
	f, q := peerRecoveryFixture(t)
	key, _ := f.service.currentDungeonPartyRunKey(f.leader)
	f.service.scheduleDungeonPeerActorRecovery(f.leader, f.member, key)
	q.fire(q.task(t, 0), false)
	peerRecoveryFrames(t, f.leaderConn.write.Bytes())
	f.leaderConn.write.Reset()
	repos, ok := f.service.repositoryGroup()
	if !ok {
		t.Fatal("missing repositories")
	}
	ctx := context.Background()
	id := fmt.Sprint(f.member.selectedCharacterID)
	character, found, err := repos.Character.Load(ctx, id)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	character.Name = "fresh-peer"
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	q.fire(q.task(t, 1), false)
	frames := peerRecoveryFrames(t, f.leaderConn.write.Bytes())
	seen := false
	for _, frame := range frames {
		if bytes.Contains(frame.body, []byte("fresh-peer")) {
			seen = true
		}
	}
	if !seen {
		t.Fatal("later recovery reused old character projection")
	}
}
