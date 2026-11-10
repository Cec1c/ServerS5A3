package dnfbridge

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func weddingRoomLeaseFixture(t *testing.T) (*Service, *gameSession, *gameSession, string) {
	t.Helper()
	s, first, repos := newTownMoveTest(t)
	s.options.currentClientVersion, s.options.gameUpperBodyCodec = "official-24.9", gameUpperBodyCodecPlain
	ctx := context.Background()
	table, err := town.Load(ctx, townMoveTestSource{
		"town/town.lst":       "101 `coupleroom.twn` 38 `outside.twn`",
		"town/coupleroom.twn": "[area]\n0 `CoupleRoom/CoupleRoom.map` `[couple room]` 474 234\n[/area]",
		"town/outside.twn":    "[area]\n0 `outside.map` `[normal]`\n[/area]",
	}, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog, s.onlinePlayers = table, newOnlinePlayerManager()
	s.wedding.rules = &wedding.Rules{Rings: []wedding.Ring{{Stage: 0}}, Furniture: map[int64]wedding.Furniture{}}
	first.accountID, first.characterGeneration = "wedding:29", 29
	second := &gameSession{conn: &bufferConn{}, accountID: "wedding:30", selectedCharacterID: 30, characterGeneration: 30, channel: first.channel, residentChannel: first.channel}
	s.gameSessions = map[uint16]*gameSession{29: first, 30: second}
	for _, session := range []*gameSession{first, second} {
		id := strconv.Itoa(int(session.selectedCharacterID))
		partner := int64(29)
		if session == first {
			partner = 30
		}
		c := r.CharacterRecord{CharacterID: id, AccountID: session.accountID, Name: "wedding" + id, Job: "0", Level: 30, Stats: map[string]int64{
			"wedding_v1_partner": partner, "wedding_v1_phase": int64(wedding.Married), "town_id": 101, "area_id": 0,
		}}
		if err = r.SaveCharacterFields(ctx, repos.Character, c, append(r.AllCharacterFields(), r.CharacterFieldWedding)...); err != nil {
			t.Fatal(err)
		}
		if err = repos.Equipment.Save(ctx, r.EquipmentRecord{CharacterID: id, Entries: map[string]r.EquipmentEntry{}}); err != nil {
			t.Fatal(err)
		}
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: session.selectedCharacterID, TownID: 101, AreaID: 0, Instance: weddingPairInstance(29, 30), ChannelID: first.channel.ID, Session: session})
	}
	scope, err := r.WeddingPairScope([]string{"29", "30"})
	if err != nil {
		t.Fatal(err)
	}
	return s, first, second, scope
}

func TestWeddingRoomEditingLeaseCannotOutliveSceneOrSession(t *testing.T) {
	for _, scenario := range []string{"left_room", "replaced_session"} {
		t.Run(scenario, func(t *testing.T) {
			s, first, second, scope := weddingRoomLeaseFixture(t)
			if err := s.handleCurrentWeddingRoomOpen(first, weddingRoomEditorNativeBody(t, "open-button")); err != nil {
				t.Fatal(err)
			}
			if got := guildReply(t, first, 1045); !bytes.Equal(got, []byte{1, 0, 0, 0, 0, 0}) {
				t.Fatalf("first open ACK %x", got)
			}
			old := s.wedding.roomEditors[scope]
			if old.session != first {
				t.Fatal("first editor missing")
			}
			if scenario == "left_room" {
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 29, TownID: 38, AreaID: 0, ChannelID: first.channel.ID, Session: first})
				if !s.boundGameSessionCharacterCurrent(old) {
					t.Fatal("fixture should keep editor connected outside room")
				}
			} else {
				s.gameSessions[29] = &gameSession{conn: &bufferConn{}, accountID: first.accountID, selectedCharacterID: 29, characterGeneration: first.characterGeneration + 1}
			}
			if err := s.handleCurrentWeddingRoomOpen(second, weddingRoomEditorNativeBody(t, "open-button")); err != nil {
				t.Fatal(err)
			}
			if got := guildReply(t, second, 1045); !bytes.Equal(got, []byte{1, 0, 0, 0, 0, 0}) || s.wedding.roomEditors[scope].session != second {
				t.Fatalf("stale editor retained lease: ACK %x editor=%+v", got, s.wedding.roomEditors[scope])
			}
		})
	}
}

func TestWeddingRoomCloseOutsideRoomOnlyReleasesOwnGeneration(t *testing.T) {
	s, first, second, scope := weddingRoomLeaseFixture(t)
	actor, _ := s.boundGameSessionCharacterSnapshot(first)
	peer, _ := s.boundGameSessionCharacterSnapshot(second)
	s.wedding.roomEditors = map[string]boundGameSessionCharacter{scope: actor}
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 29, TownID: 38, AreaID: 0, ChannelID: first.channel.ID, Session: first})
	if err := s.handleCurrentWeddingRoomOpen(first, weddingRoomEditorNativeBody(t, "close")); err != nil {
		t.Fatal(err)
	}
	if _, exists := s.wedding.roomEditors[scope]; exists {
		t.Fatal("own close after area transition retained lease")
	}
	if got := guildReply(t, first, 1045); !bytes.Equal(got, []byte{1, 0, 0, 0, 0, 1}) {
		t.Fatalf("close ACK %x", got)
	}
	s.wedding.roomEditors[scope] = peer
	if err := s.handleCurrentWeddingRoomOpen(first, weddingRoomEditorNativeBody(t, "close")); err != nil {
		t.Fatal(err)
	}
	if s.wedding.roomEditors[scope] != peer {
		t.Fatal("nonowner close evicted partner editor")
	}
}

type weddingRoomWriteFailure struct {
	bufferConn
	failOn, writes int
}

func (c *weddingRoomWriteFailure) Write(data []byte) (int, error) {
	c.writes++
	if c.writes >= c.failOn {
		return 0, io.ErrClosedPipe
	}
	return c.bufferConn.Write(data)
}

func TestWeddingRoomOpeningWriteFailureReleasesEditor(t *testing.T) {
	for _, failOn := range []int{1, 2} {
		t.Run(strconv.Itoa(failOn), func(t *testing.T) {
			s, first, _, scope := weddingRoomLeaseFixture(t)
			first.conn = &weddingRoomWriteFailure{failOn: failOn}
			if err := s.handleCurrentWeddingRoomOpen(first, weddingRoomEditorNativeBody(t, "open-button")); err == nil {
				t.Fatal("socket write failure lost")
			}
			if _, exists := s.wedding.roomEditors[scope]; exists {
				t.Fatal("failed opening retained exclusive editor")
			}
		})
	}
}
