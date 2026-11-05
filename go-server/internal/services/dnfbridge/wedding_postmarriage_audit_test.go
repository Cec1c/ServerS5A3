package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingCeremonyUnchangedRecoveryDoesNotClearCongratulationsAgain(t *testing.T) {
	f := newWeddingChurchLoadingFixture(t)
	ctx := context.Background()
	a, _, _ := f.repos.Character.Load(ctx, "29")
	b, _, _ := f.repos.Character.Load(ctx, "30")
	for range 3 {
		if err := f.s.writeCurrentWeddingCeremony(f.first, a, b); err != nil {
			t.Fatal(err)
		}
	}
	frames := weddingChurchLoadingFrames(t, f.first)
	if len(frames) != 1 || frames[0].op != 1004 {
		t.Fatalf("unchanged reservation was replayed: %+v", frames)
	}
	f.first.characterGeneration++
	if err := f.s.writeCurrentWeddingCeremony(f.first, a, b); err != nil {
		t.Fatal(err)
	}
	frames = weddingChurchLoadingFrames(t, f.first)
	if len(frames) != 1 || frames[0].op != 1004 {
		t.Fatal("new binding did not restore reservation")
	}
	a.Stats["wedding_v1_ceremony_at"]++
	b.Stats["wedding_v1_ceremony_at"]++
	if err := f.s.writeCurrentWeddingCeremony(f.first, a, b); err != nil {
		t.Fatal(err)
	}
	frames = weddingChurchLoadingFrames(t, f.first)
	if len(frames) != 1 || frames[0].op != 1004 {
		t.Fatal("changed reservation was suppressed")
	}
}

func weddingBenefitsAuditFixture(t *testing.T, phase byte) *weddingChurchLoadingFixture {
	t.Helper()
	f := newWeddingChurchLoadingFixture(t)
	f.s.options.currentClientVersion = "official-24.9"
	f.s.wedding.rules.Furniture = map[int64]wedding.Furniture{9000: {ItemID: 9000, Points: 10, Positions: map[uint16]bool{1: true}}}
	for _, id := range []string{"29", "30"} {
		c, _, _ := f.repos.Character.Load(context.Background(), id)
		if phase == wedding.Single {
			for key := range c.Stats {
				if strings.HasPrefix(key, "wedding_v1_") {
					delete(c.Stats, key)
				}
			}
		} else {
			c.Stats["wedding_v1_phase"] = int64(phase)
			c.Stats["wedding_v1_relation_token"] = 777
			if phase == wedding.Married {
				c.Stats["wedding_v1_married_at"] = f.now.Unix() - 100
				c.Stats["wedding_v1_room_points"] = 10
			}
		}
		if err := r.SaveCharacterFields(context.Background(), f.repos.Character, c, r.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
	}
	if phase == wedding.Married {
		room := wedding.Room{Version: 1, Bag: map[uint16]r.ItemStack{}, Placed: map[uint16]r.ItemStack{1: {ItemID: 9000, Count: 1}}}
		raw, _ := json.Marshal(room)
		scope, _ := r.WeddingPairScope([]string{"29", "30"})
		if err := f.repos.Settings.Save(context.Background(), r.SettingsRecord{Scope: scope, Values: map[string]string{"room": string(raw)}}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestWeddingBenefitsReplayRestoresRealMarriageAndClearsUnmarriedWithoutCustomBuffs(t *testing.T) {
	for _, phase := range []byte{wedding.Single, wedding.Engaged, wedding.Married} {
		f := weddingBenefitsAuditFixture(t, phase)
		id, _ := f.s.boundGameSessionCharacterSnapshot(f.first)
		if err := f.s.replayCurrentWeddingBenefits(id, "scene_reset"); err != nil {
			t.Fatal(err)
		}
		info, room := -1, -1
		for i, p := range weddingChurchLoadingFrames(t, f.first) {
			if p.op == 1000 {
				info = i
				want := byte(255)
				if phase == wedding.Married {
					want = 0
				}
				if p.body[4] != phase || p.body[14] != want {
					t.Fatalf("phase %d ring/sentinel %x", phase, p.body)
				}
			}
			if p.op == 1008 {
				room = i
				want := uint32(0)
				if phase == wedding.Married {
					want = 1
				}
				if binary.LittleEndian.Uint32(p.body) != want {
					t.Fatalf("phase %d projected false furniture %x", phase, p.body)
				}
			}
			if p.op == 998 || p.op == 1033 || p.op == 1004 {
				t.Fatal("benefit replay opened/refreshed a ceremony result")
			}
		}
		if info < 0 || room <= info {
			t.Fatalf("phase %d lost Info -> Room ordering", phase)
		}
		f.first.characterGeneration++
		if err := f.s.replayCurrentWeddingBenefits(id, "old_generation"); err != nil {
			t.Fatal(err)
		}
		if len(weddingChurchLoadingFrames(t, f.first)) != 0 {
			t.Fatal("stale binding received benefits")
		}
	}
}

func TestWeddingCommittedItemProjectionEndsWithMarriageAndRoomForBothHosts(t *testing.T) {
	f := weddingBenefitsAuditFixture(t, wedding.Married)
	a, _, _ := f.repos.Character.Load(context.Background(), "29")
	b, _, _ := f.repos.Character.Load(context.Background(), "30")
	result := wedding.PairResult{Actor: a, Partner: b, ChangedSlots: map[string][]string{"29": {"0:3"}, "30": {"0:4"}}}
	f.s.wedding.mu.Lock()
	err := f.s.publishCurrentWeddingPair(result)
	f.s.wedding.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []*gameSession{f.first, f.second} {
		bag, info, room := -1, -1, -1
		for i, p := range weddingChurchLoadingFrames(t, session) {
			switch p.op {
			case 14:
				bag = i
			case 1000:
				info = i
			case 1008:
				room = i
			}
		}
		if bag < 0 || info <= bag || room <= info {
			t.Fatalf("character %d lost final benefits: bag=%d info=%d room=%d", session.selectedCharacterID, bag, info, room)
		}
	}
}

func TestWeddingBenefitsQueuedAfterSceneReadsLatestMarriageInsteadOfCapturedBonuses(t *testing.T) {
	f := weddingBenefitsAuditFixture(t, wedding.Married)
	if err := f.s.startGameSessionEvents(context.Background(), f.first); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, f.s, f.first)
	unblock, done := blockWeddingCancellationTransport(t, f.s, f.first, nil)
	defer unblock()
	if err := f.s.queueCurrentWeddingBenefitsReplay(f.first, "scene_committed"); err != nil {
		t.Fatal(err)
	}
	// The result is queued while this character is still married. A committed
	// removal before the scene tail runs must send the clear, not old bonuses.
	c, _, _ := f.repos.Character.Load(context.Background(), "29")
	for key := range c.Stats {
		if strings.HasPrefix(key, "wedding_v1_") {
			delete(c.Stats, key)
		}
	}
	if err := r.SaveCharacterFields(context.Background(), f.repos.Character, c, r.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := f.s.callGameSession(context.Background(), f.first, "after_benefits", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	info := guildReply(t, f.first, 1000)
	if len(info) != 15 || info[4] != wedding.Single || info[14] != 255 {
		t.Fatalf("queued replay restored stale marriage %x", info)
	}
	room := guildReply(t, f.first, 1008)
	if binary.LittleEndian.Uint32(room) != 0 {
		t.Fatal("queued replay restored old furniture")
	}
}
