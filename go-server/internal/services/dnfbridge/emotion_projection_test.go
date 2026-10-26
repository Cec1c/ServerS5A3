package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type moodCharacterStore struct {
	dnfrepo.CharacterRepository
	onSave func()
	fail   bool
}

func (r moodCharacterStore) SaveFields(ctx context.Context, c dnfrepo.CharacterRecord, fields ...dnfrepo.CharacterField) error {
	if r.fail {
		return errors.New("mood fixture save failed")
	}
	if err := dnfrepo.SaveCharacterFields(ctx, r.CharacterRepository, c, fields...); err != nil {
		return err
	}
	if r.onSave != nil {
		r.onSave()
	}
	return nil
}

func TestMoodLegacyDurableValueWinsIncludingZero(t *testing.T) {
	for _, mood := range []int64{7, 1001, 0} {
		r := &csharpLegacyUserInfoReader{ctx: context.Background(), characterID: "19", repo: fakeLegacyUserInfoRepo{rows: map[string][]dnfrepo.LegacyUserInfoRow{
			"legacy_character_subtype0_fields": {{"emotion_index": "9"}},
		}}}
		b := r.buildCSharpUserInfoSubtype0(dnfrepo.CharacterRecord{CharacterID: "19", Stats: map[string]int64{"emotion_index": mood}}, true, 19, "mood")
		if got := binary.LittleEndian.Uint16(b[len(b)-20:]); got != uint16(mood) {
			t.Fatalf("durable %d became %d", mood, got)
		}
	}
}

func TestMoodStorageErrorAndRetiredSelection(t *testing.T) {
	for _, fail := range []bool{true, false} {
		repos := dnfrepomemory.NewMemoryGroup()
		if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
			t.Fatal(err)
		}
		s, session, conn := newGameplayDomainBridgeFixture(repos)
		s.bindGameSessionCharacter(session, 19)
		wrapped := repos
		wrapped.Character = moodCharacterStore{CharacterRepository: repos.Character, fail: fail, onSave: func() { s.bindGameSessionCharacter(session, 20) }}
		s.repositoryProvider = func() (dnfrepo.Group, bool) { return wrapped, true }
		if err := s.handleCurrentChangeEmotion(session, []byte{7, 0}); err != nil {
			t.Fatal(err)
		}
		if fail {
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Body[0] != 0 || len(rest) != 0 {
				t.Fatal("save error reported success")
			}
			c, _, _ := repos.Character.Load(context.Background(), "19")
			if c.Stats["emotion_index"] != 0 {
				t.Fatal("failed write mutated mood")
			}
		} else if conn.write.Len() != 0 {
			t.Fatal("retired selection received stale ACK/projection")
		}
	}
}

func TestMoodRefreshKeepsLoadingAndChannelBoundaries(t *testing.T) {
	for _, scenario := range []string{"source_loading", "peer_loading", "different_channel", "peer_replaced"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTownPartyActorHPFixture(t, false)
			switch scenario {
			case "source_loading":
				f.target.townSceneReadyCharacterID = 0
			case "peer_loading":
				f.actor.townSceneReadyCharacterID = 0
			case "different_channel":
				f.actor.channel.ID = 42
				f.actor.residentChannel.ID = 42
				info := f.actorInfo
				info.ChannelID = 42
				f.service.onlinePlayers.EnterArea(&info)
			case "peer_replaced":
				replacement := &gameSession{conn: &bufferConn{}}
				f.service.bindGameSessionCharacter(replacement, 17)
				f.service.promoteResidentGameSession(replacement, 17)
				if _, ok := f.service.boundGameSessionCharacterSnapshot(f.actor); ok {
					t.Fatal("fixture did not retire old peer")
				}
			}
			f.reset()
			if err := f.service.handleCurrentChangeEmotion(f.target, []byte{7, 0}); err != nil {
				t.Fatal(err)
			}
			if f.actorConn.write.Len() != 0 {
				t.Fatalf("%s received peer projection", scenario)
			}
			if scenario == "source_loading" {
				_, rest := splitGameServerUpperPacket(t, f.targetConn.write.Bytes())
				if len(rest) != 0 {
					t.Fatal("loading source received mode0")
				}
			}
		})
	}
}

// Follow the matched mode0 reader through variable equipment and names, rather
// than measuring backwards from the padded packet end.
func moodOffsetInTypedTail(t *testing.T, b []byte) int {
	t.Helper()
	p := 33 + int(b[6])*19
	p += 4 + int(binary.LittleEndian.Uint32(b[p:])) // creature name
	p += 2 + 1 + 4
	p += 4 + int(binary.LittleEndian.Uint32(b[p:])) // organization name
	p += 4 + 2 + 4 + 1 + 2 + 5 + 7 + 4 + 12         // through HonorExpert
	p += 1 + 4 + 1 + 2 + 1 + 2 + 1
	if p+8 > len(b) {
		t.Fatalf("mood cursor %d outside %d", p, len(b))
	}
	return p
}

func TestMoodTypedProjection(t *testing.T) {
	for _, mood := range []uint16{7, 1001, 0} {
		for _, name := range []string{"", "冒险团"} {
			c := dnfrepo.CharacterRecord{Stats: map[string]int64{"emotion_index": int64(mood)}}
			var w packetWriter
			writeCurrentSceneObjectEntryTailWithCreatureAndAdventureName(&w, c, true, currentEquippedCreatureSnapshot{}, name)
			b := w.bytes()
			p := moodOffsetInTypedTail(t, b)
			if got := binary.LittleEndian.Uint16(b[p:]); got != mood {
				t.Fatalf("mood %d projected %d", mood, got)
			}
		}
	}
}

func TestMoodSaveFailureDoesNotAcknowledgeSuccess(t *testing.T) {
	for _, mode := range []string{"missing_character", "wrong_account", "no_repository"} {
		t.Run(mode, func(t *testing.T) {
			repos := dnfrepomemory.NewMemoryGroup()
			if mode == "wrong_account" {
				_ = repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "another"})
			}
			if mode == "no_repository" {
				repos.Character = nil
			}
			s, session, conn := newGameplayDomainBridgeFixture(repos)
			if err := s.handleCurrentChangeEmotion(session, []byte{7, 0}); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Body[0] != 0 || len(rest) != 0 {
				t.Fatalf("failure acknowledged or projected: %x / %d", ack.Body, len(rest))
			}
		})
	}
}

func TestMoodRefreshSelfPeerAndClear(t *testing.T) {
	f := newTownPartyActorHPFixture(t, false)
	for _, mood := range []uint16{7, 1001, 0} {
		f.reset()
		b := []byte{byte(mood), byte(mood >> 8)}
		if err := f.service.handleCurrentChangeEmotion(f.target, b); err != nil {
			t.Fatal(err)
		}
		for i, conn := range []*bufferConn{f.targetConn, f.actorConn} {
			data := conn.write.Bytes()
			found := false
			for len(data) > 0 {
				pkt, rest := splitGameServerUpperPacket(t, data)
				data = rest
				if pkt.Header.MsgID != 2 || len(pkt.Body) < 80 || pkt.Body[0] != 0 {
					continue
				}
				pos := 3 + 1 + 1 + 0x47 + 2
				pos += 4 + int(binary.LittleEndian.Uint32(pkt.Body[pos:]))
				offset := pos + moodOffsetInTypedTail(t, pkt.Body[pos:])
				if got := binary.LittleEndian.Uint16(pkt.Body[offset:]); got != mood {
					t.Fatalf("recipient %d mood %d want %d", i, got, mood)
				}
				if dir := os.Getenv("DNF_MOOD_NATIVE_EXPORT"); dir != "" {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("mood-%d-peer-%d.bin", mood, i)), pkt.Body[offset:offset+2], 0600); err != nil {
						t.Fatal(err)
					}
				}
				found = true
			}
			if !found {
				t.Fatalf("recipient %d got no mood mode0", i)
			}
		}
	}
}
