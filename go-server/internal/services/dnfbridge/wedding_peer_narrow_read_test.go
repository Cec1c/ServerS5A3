package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

type peerWeddingNarrowReader struct {
	r.CharacterRepository
	rows             map[string]r.CharacterWeddingSnapshot
	reads, fullLoads int
	err              error
}

func (s *peerWeddingNarrowReader) Load(context.Context, string) (r.CharacterRecord, bool, error) {
	s.fullLoads++
	return r.CharacterRecord{}, false, errors.New("unexpected full character load")
}
func (s *peerWeddingNarrowReader) LoadCharacterWedding(ctx context.Context, id string) (r.CharacterWeddingSnapshot, bool, error) {
	s.reads++
	if err := ctx.Err(); err != nil {
		return r.CharacterWeddingSnapshot{}, false, err
	}
	if s.err != nil {
		return r.CharacterWeddingSnapshot{}, false, s.err
	}
	row, ok := s.rows[id]
	return row, ok, nil
}

func TestWeddingPeerNarrowReaderMatchesFullFallback(t *testing.T) {
	for _, married := range []bool{false, true} {
		t.Run(map[bool]string{false: "reserved", true: "married"}[married], func(t *testing.T) {
			s, _, _, _ := weddingRoomLeaseFixture(t)
			repos, _ := s.repositoryGroup()
			ctx := context.Background()
			if married {
				for _, id := range []string{"29", "30"} {
					c, _, _ := repos.Character.Load(ctx, id)
					c.Stats["wedding_v1_phase"] = int64(wedding.Married)
					if err := r.SaveCharacterFields(ctx, repos.Character, c, r.CharacterFieldWedding); err != nil {
						t.Fatal(err)
					}
				}
			}
			actor, _, _ := repos.Character.Load(ctx, "29")
			want, err := s.currentWeddingPeerInfo(ctx, actor)
			if err != nil || len(want) != 14 {
				t.Fatal("baseline", want, err)
			}
			reader := &peerWeddingNarrowReader{CharacterRepository: repos.Character, rows: map[string]r.CharacterWeddingSnapshot{}}
			for _, id := range []string{"29", "30"} {
				row, found, err := r.LoadCharacterWedding(ctx, repos.Character, id)
				if err != nil || !found {
					t.Fatal(err)
				}
				reader.rows[id] = row
			}
			repos.Character = reader
			s.repositoryProvider = func() (r.Group, bool) { return repos, true }
			actor.Stats = nil // stale appearance data cannot determine current marriage.
			got, err := s.currentWeddingPeerInfo(ctx, actor)
			if err != nil || !bytes.Equal(got, want) || reader.reads != 2 || reader.fullLoads != 0 {
				t.Fatalf("body=%x want=%x reads=%d full=%d err=%v", got, want, reader.reads, reader.fullLoads, err)
			}
			reader.err = errors.New("narrow read failed")
			if _, err := s.currentWeddingPeerInfo(ctx, actor); !errors.Is(err, reader.err) || reader.fullLoads != 0 {
				t.Fatal("narrow error was hidden", err)
			}
		})
	}
}
