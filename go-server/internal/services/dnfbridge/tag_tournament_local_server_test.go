package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// V195's Empire UI obtains the local roster server from actor+0x3ac, populated
// by the mode0 finalizer. It does not use the mode0 prefix's advertised route.
func TestTagTournamentLocalRosterServerWithAdvertisedRouteOne(t *testing.T) {
	s, p, g := newTagRosterBridgeTest(t)
	s.options.channelAdvertiseID = 1
	if s.currentSceneActorRoute() != 1 {
		t.Fatal("fixture must advertise route 1")
	}
	// Selected noncurrent row first; the current character is the second member.
	extra := repo.CloneCharacter(p.currentRenameRoster[0])
	extra.CharacterID, extra.Slot, extra.Name = "61", 25, "another member"
	if err := g.Character.Save(context.Background(), extra); err != nil {
		t.Fatal(err)
	}
	rowsBefore := p.currentRenameRoster
	p.currentRenameRoster = []repo.CharacterRecord{rowsBefore[1], rowsBefore[0], extra, rowsBefore[2]}
	// Exact four-byte native serializer output for selectors 1 and 3.
	body := []byte{0, 1, 0, 3}
	for n := 0; n < 2; n++ {
		p.conn.(*bufferConn).write.Reset()
		if err := s.handleCurrentTagTournamentSetRoster(p, body); err != nil {
			t.Fatal(err)
		}
		ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
		if ack.Header.MsgID != 1457 || !bytes.Equal(ack.Body, append([]byte{1}, body...)) || len(rest) != 0 {
			t.Fatalf("local server 0 rejected under advertised route 1: body=%x rest=%x", ack.Body, rest)
		}
	}
	owner, err := s.currentTagTournamentOwner()
	if err != nil {
		t.Fatal(err)
	}
	view, err := owner.Lobby(context.Background(), p.accountID)
	if err != nil || !view.Registered || view.Characters[0].CharacterID != "60" || view.Characters[1].CharacterID != "29" || view.Day.Paid {
		t.Fatal("roster was not durably registered without a charge", view, err)
	}
	rows, server, err := s.currentTagTournamentSentRoster(p)
	if err != nil {
		t.Fatal(err)
	}
	p.conn.(*bufferConn).write.Reset()
	if err := s.sendCurrentTagTournamentSavedRoster(p, rows, server); err != nil {
		t.Fatal(err)
	}
	saved, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
	if saved.Header.MsgID != 1388 || !bytes.Equal(saved.Body, body) || len(rest) != 0 {
		t.Fatal("saved roster used advertised route instead of native roster server", saved, rest)
	}
	p.conn.(*bufferConn).write.Reset()
	if err := s.handleCurrentTagTournamentSetRoster(p, []byte{1, 1, 1, 3}); err != nil {
		t.Fatal(err)
	}
	bad, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
	if !bytes.Equal(bad.Body, []byte{0, 3}) || len(rest) != 0 {
		t.Fatal("advertised route was accepted as a roster server", bad, rest)
	}
	for _, id := range []string{"60", "29"} {
		ch, _, err := g.Character.Load(context.Background(), id)
		if err != nil || ch.Stats["gold"] != 1000000 {
			t.Fatal("setting roster changed assets", id, err)
		}
	}
}

func TestTagTournamentLocalRosterServerMatchesMode0Finalizer(t *testing.T) {
	for _, name := range []string{"", "peer", "角色"} {
		body := buildCurrentSceneObjectListBodyWithCreatureAndAdventureNameInContext(29,
			repo.CharacterRecord{Name: name, Level: 90}, true, name, currentEquippedCreatureSnapshot{}, "冒险团", 187, 1)
		// Native 22C6202 consumes the last 19-byte finalizer; its eighth byte
		// reaches actor+0x3ac. Prefix route must remain the advertised value.
		if body[3] != 1 || body[len(body)-12] != 0 {
			t.Fatalf("native roster/prefix identities conflated: %x", body)
		}
	}
}
