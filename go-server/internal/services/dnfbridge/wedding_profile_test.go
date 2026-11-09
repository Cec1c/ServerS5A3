package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func weddingProfileFixture(t *testing.T) (*Service, []*gameSession, r.Group) {
	t.Helper()
	s, peers, owner, req := weddingCancellationProjectionFixture(t)
	if _, err := owner.Engage(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	return s, peers, owner.Repositories
}

func weddingProfileRequest(target uint32) []byte {
	body := make([]byte, 17)
	binary.LittleEndian.PutUint32(body[13:], target)
	return body
}

func TestWeddingProfileTargetZeroReturnsRealOfflineSpouse(t *testing.T) {
	s, peers, repos := weddingProfileFixture(t)
	delete(s.gameSessions, peers[1].selectedCharacterID)
	selfBefore, _, _ := repos.Character.Load(context.Background(), "1")
	spouseBefore, _, _ := repos.Character.Load(context.Background(), "2")
	if err := s.handleCurrentWeddingCharacter(peers[0], weddingProfileRequest(0)); err != nil {
		t.Fatal(err)
	}
	ops, bodies := cargoReplies(t, peers[0])
	if len(ops) != 1 || ops[0] != 1035 || len(bodies[0]) != 98 || bodies[0][0] != 1 ||
		binary.LittleEndian.Uint32(bodies[0][5:]) != 2 || bodies[0][43] != 90 || bodies[0][44] != 11 {
		t.Fatalf("real spouse profile %v %x", ops, bodies)
	}
	name, err := decodeCharacterName(bodies[0][13:43])
	spouse, _, _ := repos.Character.Load(context.Background(), "2")
	if err != nil || name != spouse.Name {
		t.Fatalf("profile name=%q want spouse=%q err=%v", name, spouse.Name, err)
	}
	selfAfter, _, _ := repos.Character.Load(context.Background(), "1")
	spouseAfter, _, _ := repos.Character.Load(context.Background(), "2")
	if !reflect.DeepEqual(selfBefore, selfAfter) || !reflect.DeepEqual(spouseBefore, spouseAfter) {
		t.Fatal("profile lookup changed either persisted character")
	}
}

type weddingProfileEquipmentProbe struct {
	r.EquipmentRepository
	missing bool
	foreign bool
	after   func()
}

func (p weddingProfileEquipmentProbe) Load(ctx context.Context, id string) (r.EquipmentRecord, bool, error) {
	v, found, err := p.EquipmentRepository.Load(ctx, id)
	if p.after != nil {
		p.after()
	}
	if p.foreign {
		v.CharacterID = "999"
	}
	return v, found && !p.missing, err
}

func TestWeddingProfileTargetZeroRejectsSingleBrokenPairAndForeignAssets(t *testing.T) {
	for _, scenario := range []string{"single", "wrong-account", "broken-reciprocal", "missing-equipment", "foreign-equipment", "retired-generation"} {
		t.Run(scenario, func(t *testing.T) {
			s, peers, repos := weddingProfileFixture(t)
			ctx := context.Background()
			if scenario == "single" || scenario == "broken-reciprocal" || scenario == "wrong-account" {
				c, _, _ := repos.Character.Load(ctx, "1")
				switch scenario {
				case "single":
					c.Stats["wedding_v1_phase"], c.Stats["wedding_v1_partner"] = 0, 0
				case "broken-reciprocal":
					c.Stats["wedding_v1_partner"] = 3
				case "wrong-account":
					c.AccountID = "foreign"
				}
				if err := r.SaveCharacterFields(ctx, repos.Character, c, r.CharacterFieldWedding, r.CharacterFieldBase); err != nil {
					t.Fatal(err)
				}
				stored, found, err := repos.Character.Load(ctx, c.CharacterID)
				if err != nil || !found || stored.AccountID != c.AccountID ||
					stored.Stats["wedding_v1_phase"] != c.Stats["wedding_v1_phase"] ||
					stored.Stats["wedding_v1_partner"] != c.Stats["wedding_v1_partner"] {
					t.Fatalf("rejection fixture was not persisted: found=%t err=%v record=%+v", found, err, stored)
				}
			} else {
				probe := weddingProfileEquipmentProbe{EquipmentRepository: repos.Equipment, missing: scenario == "missing-equipment", foreign: scenario == "foreign-equipment"}
				if scenario == "retired-generation" {
					probe.after = func() {
						s.mu.Lock()
						peers[0].characterGeneration++
						s.mu.Unlock()
					}
				}
				repos.Equipment = probe
				s.repositoryProvider = func() (r.Group, bool) { return repos, true }
			}
			if err := s.handleCurrentWeddingCharacter(peers[0], weddingProfileRequest(0)); err != nil {
				t.Fatal(err)
			}
			ops, bodies := cargoReplies(t, peers[0])
			if len(ops) != 1 || ops[0] != 1035 || len(bodies[0]) != 1 || bodies[0][0] != 0 {
				t.Fatalf("unsafe profile request accepted %v %x", ops, bodies)
			}
		})
	}
}

func TestWeddingProfileCommittedRestorePreparesBeforeInfoAndRejectsRetiredOwner(t *testing.T) {
	for _, scenario := range []string{"matching-pair", "missing-equipment", "foreign-equipment", "retired-generation"} {
		t.Run(scenario, func(t *testing.T) {
			s, peers, repos := weddingProfileFixture(t)
			c, _, _ := repos.Character.Load(context.Background(), "1")
			if scenario != "matching-pair" {
				probe := weddingProfileEquipmentProbe{EquipmentRepository: repos.Equipment, missing: scenario == "missing-equipment", foreign: scenario == "foreign-equipment"}
				if scenario == "retired-generation" {
					probe.after = func() {
						s.mu.Lock()
						peers[0].characterGeneration++
						s.mu.Unlock()
					}
				}
				repos.Equipment = probe
				s.repositoryProvider = func() (r.Group, bool) { return repos, true }
			}
			s.wedding.mu.Lock()
			err := s.sendCurrentWeddingInfoAndPartnerLocked(peers[0], c)
			s.wedding.mu.Unlock()
			ops, bodies := cargoReplies(t, peers[0])
			if scenario != "matching-pair" {
				if err == nil || len(ops) != 0 {
					t.Fatalf("preview cleared before complete safe preparation: err=%v ops=%v", err, ops)
				}
				return
			}
			if err != nil || len(ops) != 2 || ops[0] != 1000 || ops[1] != 1035 || len(bodies[0]) != 15 || len(bodies[1]) != 98 ||
				binary.LittleEndian.Uint32(bodies[0]) != binary.LittleEndian.Uint32(bodies[1][5:]) || bodies[0][14] != 255 || bodies[1][43] != 90 || bodies[1][44] != 11 {
				t.Fatalf("unmatched ordered state/profile err=%v ops=%v bodies=%x", err, ops, bodies)
			}
		})
	}
}

func TestWeddingProfileInitialInfoDoesNotTakeWeddingLockAndTargetZeroRecovers(t *testing.T) {
	s, peers, _ := weddingProfileFixture(t)
	// Match the rules used by weddingCancellationProjectionFixture to create
	// this pair. Initial projection now also reads its durable guest index.
	s.wedding.rules = &wedding.Rules{MinimumLevel: 30, EngagementCancellationGold: 500000, QuestIDs: []int64{20737, 20738}}
	// Selection calls this under its town/asset owner. Holding the global lock
	// here makes a recursive or reversed lock attempt fail this event barrier.
	s.wedding.mu.Lock()
	done := make(chan error, 1)
	go func() {
		peers[0].townMu.Lock()
		defer peers[0].townMu.Unlock()
		done <- s.sendCurrentInitialWeddingState(peers[0])
	}()
	select {
	case err := <-done:
		s.wedding.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		s.wedding.mu.Unlock()
		t.Fatal("initial Info1000 acquired the wedding mutex inside the scene owner")
	}
	ops, bodies := cargoReplies(t, peers[0])
	if len(ops) != 4 || ops[0] != 1000 || ops[1] != 1011 || ops[2] != 1008 || ops[3] != 1009 || len(bodies[0]) != 15 || bodies[0][14] != 255 || binary.LittleEndian.Uint32(bodies[2]) != 0 || binary.LittleEndian.Uint32(bodies[3]) != 0 {
		t.Fatalf("initial info order changed %v %x", ops, bodies)
	}
	if err := s.handleCurrentWeddingCharacter(peers[0], weddingProfileRequest(0)); err != nil {
		t.Fatal(err)
	}
	ops, profiles := cargoReplies(t, peers[0])
	if len(ops) != 1 || ops[0] != 1035 || binary.LittleEndian.Uint32(profiles[0][5:]) != binary.LittleEndian.Uint32(bodies[0]) {
		t.Fatalf("native target0 did not restore matching initial token %v %x", ops, profiles)
	}
}
