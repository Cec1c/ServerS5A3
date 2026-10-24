package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// r195Op24FailureConn leaves the real fixture connection in place and rejects
// only the typed scene-transition write. An earlier failure would not exercise
// the point after the raid move was applied but before it was confirmed.
type r195Op24FailureConn struct {
	net.Conn
	err    error
	failed bool
}

func (c *r195Op24FailureConn) Write(data []byte) (int, error) {
	if !c.failed && len(data) >= 3 && binary.LittleEndian.Uint16(data[1:3]) == uint16(currentSceneTransitionMsgID) {
		c.failed = true
		return 0, c.err
	}
	return c.Conn.Write(data)
}

func TestR195FiendWarOp24FailureDoesNotRetainAreaLanding(t *testing.T) {
	f, peers := r134N1Fixture(t)
	s, b := f.service, f.state.Battle
	b.FiendWar.Symbols[b.FiendWar.Config.Symbols["top boss state"]] = 1
	b.FiendWar.Symbols[b.FiendWar.Config.Symbols["bot boss state"]] = 1
	syncCurrentFiendWarDungeons(b)

	// The second personal seat moves 29 -> 31 (white). The destination is a real
	// PVF area. This test isolates the server-side op24 failure boundary; it does
	// not claim to prove that the client has finished loading a successful move.
	p := peers[1]
	from := 29
	target := 31
	r134N1Presence(t, f, p, from)
	character, found, err := f.repos.Character.Load(context.Background(), fmt.Sprint(p.selectedCharacterID))
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Stats["area_state"] = 3
	if err := f.repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{
		CharacterID: character.CharacterID,
		States:      map[int64]dnfrepo.QuestState{8641: {Status: "completed"}},
	}); err != nil {
		t.Fatal(err)
	}

	p.townMu.Lock()
	before := p.townPositionSnapshot
	p.townMu.Unlock()
	wantErr := errors.New("r195 injected typed op24 write failure")
	p.conn = &r195Op24FailureConn{Conn: p.conn, err: wantErr}
	if err := s.handleTownSetUserArea(p, buildTownMoveRequest(98, byte(target), 733, 211, 5)); !errors.Is(err, wantErr) {
		t.Fatalf("op36 error=%v want=%v", err, wantErr)
	}

	p.townMu.Lock()
	after := p.townPositionSnapshot
	pending := p.fiendWarPendingMove
	p.townMu.Unlock()
	if after != before {
		t.Fatalf("failed op24 retained a landing: before=%+v after=%+v", before, after)
	}
	if pending != nil {
		t.Fatalf("failed op24 retained a pending move: %+v", pending)
	}
	group := b.MemberGroups[p.selectedCharacterID]
	if area := b.FiendWar.MemberArea(p.selectedCharacterID, group); area != from {
		t.Fatalf("failed op24 left raid area=%d want=%d", area, from)
	}
	p.dungeon.mu.Lock()
	runtimePresent := p.dungeon.runtime != nil
	p.dungeon.mu.Unlock()
	if runtimePresent {
		t.Fatal("failed op24 created a dungeon runtime")
	}
}
