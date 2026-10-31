package repository

import (
	"context"
	"errors"
	"testing"
)

type identityFallbackCharacter struct {
	CharacterRepository
	err   error
	calls int
}

func (r *identityFallbackCharacter) Load(context.Context, string) (CharacterRecord, bool, error) {
	r.calls++
	return CharacterRecord{CharacterID: "c", AccountID: "a", Name: "preserve", Stats: map[string]int64{"gold": 123}}, r.err == nil, r.err
}

func TestCharacterIdentityFallbackPreservesFailureAndOwnership(t *testing.T) {
	for _, want := range []error{nil, errors.New("read failure")} {
		r := &identityFallbackCharacter{err: want}
		got, found, err := LoadCharacterIdentity(context.Background(), r, "c")
		if r.calls != 1 || !errors.Is(err, want) || found != (want == nil) || got.AccountID != "a" || got.CharacterID != "c" {
			t.Fatalf("identity=%+v found=%t err=%v calls=%d", got, found, err, r.calls)
		}
	}
}
