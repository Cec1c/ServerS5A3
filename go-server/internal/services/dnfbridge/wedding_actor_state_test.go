package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func weddingActorStateCharacter(stage, points int64) r.CharacterRecord {
	return r.CharacterRecord{CharacterID: "2", AccountID: "wedding:2", Name: "actor", Job: "0", Level: 90,
		Stats: map[string]int64{"wedding_v1_phase": 4, "wedding_v1_partner": 5, "wedding_v1_ring": stage, "wedding_v1_room_points": points,
			"wedding_v1_kind": 2, "wedding_v1_married_at": 1793814669, "wedding_v1_ceremony_at": 1793814609, "wedding_v1_ceremony_channel": 10, "wedding_v1_ceremony_area": 8}}
}

func TestWeddingActorRawStateFeedsEveryNativeRebuildCarrier(t *testing.T) {
	type testCase struct {
		name        string
		character   r.CharacterRecord
		present     bool
		phase, ring byte
		points      uint32
	}
	cases := []testCase{}
	for stage := int64(0); stage < 6; stage++ {
		for _, points := range []int64{0, 9, 10, 39, 40, 50, 99, 100, 199, 200, 499, 500, 799, 800, 999, 1000} {
			cases = append(cases, testCase{fmt.Sprintf("married-%d-%d", stage, points), weddingActorStateCharacter(stage, points), true, wedding.Married, byte(stage), uint32(points)})
		}
	}
	for phase := wedding.Single; phase < wedding.Married; phase++ {
		c := weddingActorStateCharacter(3, 50)
		c.Stats["wedding_v1_phase"] = int64(phase)
		if phase == wedding.Single {
			c.Stats["wedding_v1_partner"] = 0
		}
		cases = append(cases, testCase{fmt.Sprintf("phase-%d", phase), c, true, phase, 0, 0})
	}
	cases = append(cases, testCase{"unavailable", weddingActorStateCharacter(3, 50), false, 0, 0, 0})
	for _, bad := range []struct {
		key   string
		value int64
	}{{"wedding_v1_ring", 6}, {"wedding_v1_ring", 255}, {"wedding_v1_room_points", -1}, {"wedding_v1_room_points", 1 << 32}, {"wedding_v1_partner", 2}, {"wedding_v1_phase", 5}} {
		c := weddingActorStateCharacter(3, 50)
		c.Stats[bad.key] = bad.value
		cases = append(cases, testCase{fmt.Sprintf("invalid-%s-%d", bad.key, bad.value), c, true, 0, 0, 0})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, ok := buildCurrentSceneObjectRawState(tc.character, tc.present, tc.character.Name)
			if !ok || len(raw) != 0x47 || raw[0x17] != tc.phase || raw[0x18] != tc.ring || binary.LittleEndian.Uint32(raw[0x19:]) != tc.points {
				t.Fatalf("raw婚姻状态错误: %x", raw)
			}
			base := r.CloneCharacter(tc.character)
			for key := range base.Stats {
				if strings.HasPrefix(key, "wedding_v1_") {
					delete(base.Stats, key)
				}
			}
			plain, _ := buildCurrentSceneObjectRawState(base, tc.present, base.Name)
			for i := range raw {
				if i < 0x17 || i >= 0x1d {
					if raw[i] != plain[i] {
						t.Fatalf("unrelated raw byte%d changed", i)
					}
				}
			}
			mode0 := buildCurrentSceneObjectListBodyInContext(2, tc.character, tc.present, tc.character.Name, 10)
			if len(mode0) < 5+len(raw)+2 || !bytes.Equal(mode0[5:5+len(raw)], raw) || binary.LittleEndian.Uint16(mode0[5+len(raw):]) != 2 {
				t.Fatal("mode0 lost native raw carrier or actor key")
			}
			other := buildCurrentSceneObjectListBodyWithCreatureAndAdventureNameInContext(2, tc.character, tc.present, tc.character.Name, currentEquippedCreatureSnapshot{}, "", 10, 0)
			if !bytes.Equal(other[5:5+len(raw)], raw) {
				t.Fatal("appearance/peer mode0 carrier diverged")
			}
			proof, _ := json.Marshal(map[string]any{"name": tc.name, "mode0_hex": hex.EncodeToString(mode0), "raw_hex": hex.EncodeToString(raw), "phase": tc.phase, "ring": tc.ring, "room_points": tc.points})
			t.Logf("WEDDING_ACTOR_RAW_CASE %s", proof)
		})
	}
}

func TestWeddingRoomCloseAfterCharacterSelectionHasNoErrorAndCannotEvictEditor(t *testing.T) {
	for _, otherEditor := range []bool{false, true} {
		t.Run(fmt.Sprint(otherEditor), func(t *testing.T) {
			s, first, second, scope := weddingRoomLeaseFixture(t)
			editor, _ := s.boundGameSessionCharacterSnapshot(first)
			if otherEditor {
				editor, _ = s.boundGameSessionCharacterSnapshot(second)
			}
			s.wedding.roomEditors = map[string]boundGameSessionCharacter{scope: editor}
			before := map[string]boundGameSessionCharacter{scope: editor}
			first.selectedCharacterID = 0
			first.characterGeneration++
			if err := s.handleCurrentWeddingRoomOpen(first, weddingRoomEditorNativeBody(t, "close")); err != nil {
				t.Fatal(err)
			}
			if got := guildReply(t, first, 1045); !bytes.Equal(got, []byte{1, 0, 0, 0, 0, 1}) {
				t.Fatalf("teardown close displayed error ACK%x", got)
			} else {
				t.Logf("WEDDING_ROOM_CLOSE_ACK %s", hex.EncodeToString(got))
			}
			if !reflect.DeepEqual(before, s.wedding.roomEditors) {
				t.Fatal("unbound close evicted an editor")
			}
			first.conn.(*bufferConn).write.Reset()
			if err := s.handleCurrentWeddingRoomOpen(first, weddingRoomEditorNativeBody(t, "open-button")); err != nil {
				t.Fatal(err)
			}
			if got := guildReply(t, first, 1045); !bytes.Equal(got[1:5], []byte{255, 255, 255, 255}) {
				t.Fatal("unbound open was accepted")
			}
		})
	}
}
