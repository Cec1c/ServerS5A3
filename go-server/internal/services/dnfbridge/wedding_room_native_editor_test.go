package dnfbridge

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func weddingRoomEditorNativeBody(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/wedding_room_native_editor.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SHA   string `json:"client_sha256"`
		Cases []struct {
			Name    string `json:"name"`
			Opcode  uint16 `json:"opcode"`
			Flag    byte   `json:"flag"`
			Payload string `json:"payload_hex"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SHA != "a59db943c5f44c82e7db0e02305b3403aac3a16d0ec025b396e97da491a42231" || len(fixture.Cases) != 3 {
		t.Fatal("wrong native editor fixture")
	}
	for _, row := range fixture.Cases {
		if row.Name == name {
			body, err := hex.DecodeString(row.Payload)
			if err != nil || row.Opcode != 1045 || len(body) != 14 || body[13] != row.Flag {
				t.Fatal("invalid native editor request", err)
			}
			return body
		}
	}
	t.Fatal("missing native editor case", name)
	return nil
}

func TestWeddingRoomNativeEditorOpeningUsesZeroAndClosingUsesOne(t *testing.T) {
	s, first, _, scope := weddingRoomLeaseFixture(t)
	for _, name := range []string{"open-button", "open-generic"} {
		body := weddingRoomEditorNativeBody(t, name)
		handled, err := currentGameplayModules.DispatchUpper(s, first, 1045, 1, body)
		if err != nil || !handled {
			t.Fatal("native open dispatch", handled, err)
		}
		actor, _ := s.boundGameSessionCharacterSnapshot(first)
		if s.wedding.roomEditors[scope] != actor {
			t.Fatalf("native %s flag=%d did not acquire editor", name, body[13])
		}
		if got := guildReply(t, first, 1045); !bytes.Equal(got, []byte{1, 0, 0, 0, 0, 0}) {
			t.Fatalf("native open ACK %x", got)
		}
		first.conn.(*bufferConn).write.Reset()
	}
	handled, err := currentGameplayModules.DispatchUpper(s, first, 1045, 1, weddingRoomEditorNativeBody(t, "close"))
	if err != nil || !handled {
		t.Fatal("native close dispatch", handled, err)
	}
	if _, exists := s.wedding.roomEditors[scope]; exists {
		t.Fatal("native close retained editor")
	}
	if got := guildReply(t, first, 1045); !bytes.Equal(got, []byte{1, 0, 0, 0, 0, 1}) {
		t.Fatalf("native close ACK %x", got)
	}
}

func TestWeddingRoomNativeEditorCannotEvictActiveSpouse(t *testing.T) {
	s, first, second, scope := weddingRoomLeaseFixture(t)
	if err := s.handleCurrentWeddingRoomOpen(first, weddingRoomEditorNativeBody(t, "open-button")); err != nil {
		t.Fatal(err)
	}
	actor, _ := s.boundGameSessionCharacterSnapshot(first)
	if err := s.handleCurrentWeddingRoomOpen(second, weddingRoomEditorNativeBody(t, "open-generic")); err != nil {
		t.Fatal(err)
	}
	if got := guildReply(t, second, 1045); !bytes.Equal(got, []byte{1, 0xff, 0xff, 0xff, 0xff, 0}) || s.wedding.roomEditors[scope] != actor {
		t.Fatalf("spouse open evicted active editor: ACK %x", got)
	}
	if err := s.handleCurrentWeddingRoomOpen(second, weddingRoomEditorNativeBody(t, "close")); err != nil {
		t.Fatal(err)
	}
	if s.wedding.roomEditors[scope] != actor {
		t.Fatal("spouse close evicted active editor")
	}
	if err := s.handleCurrentWeddingRoomOpen(first, weddingRoomEditorNativeBody(t, "close")); err != nil {
		t.Fatal(err)
	}
	if err := s.handleCurrentWeddingRoomOpen(second, weddingRoomEditorNativeBody(t, "open-button")); err != nil {
		t.Fatal(err)
	}
	peer, _ := s.boundGameSessionCharacterSnapshot(second)
	if s.wedding.roomEditors[scope] != peer {
		t.Fatal("spouse could not open after editor closed")
	}
}

func TestWeddingRoomNativeEditorRejectsMalformedAndForeignScene(t *testing.T) {
	for _, scenario := range []string{"short", "long", "flag", "outside", "foreign_instance"} {
		t.Run(scenario, func(t *testing.T) {
			s, first, _, scope := weddingRoomLeaseFixture(t)
			body := weddingRoomEditorNativeBody(t, "open-button")
			switch scenario {
			case "short":
				body = body[:13]
			case "long":
				body = append(body, 0)
			case "flag":
				body[13] = 2
			case "outside", "foreign_instance":
				p, ok := s.onlinePlayers.PlayerForCharacter(29)
				if !ok {
					t.Fatal("missing player")
				}
				if scenario == "outside" {
					p.TownID = 38
				} else {
					p.Instance++
				}
				s.onlinePlayers.EnterArea(&p)
			}
			if err := s.handleCurrentWeddingRoomOpen(first, body); err != nil {
				t.Fatal(err)
			}
			if _, exists := s.wedding.roomEditors[scope]; exists {
				t.Fatal("invalid request acquired room editor")
			}
			if got := guildReply(t, first, 1045); len(got) != 6 || !bytes.Equal(got[1:5], bytes.Repeat([]byte{0xff}, 4)) {
				t.Fatalf("invalid request ACK %x", got)
			}
		})
	}
}
