package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func raidTableLoaded(t *testing.T, a *platformpvf.Archive) (*raidStartGuardFixture, []*gameSession, []*deadlineAuditV10Conn, []*runtimeDungeonState) {
	t.Helper()
	f, owners, conns, _ := deadlineAuditV10Admit(t, a, false)
	s := f.service
	b := f.state.Battle
	dispatch := func(p *gameSession, op uint16, body []byte) {
		frame, err := proto.BuildChannelPacket(op, body, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		deadlineAuditV10Event(t, s, p, func() error {
			if op == 42 {
				return s.handleGameUpper(p, frame)
			}
			return s.handleLegacyGamePacket(p, frame)
		})
	}
	dispatch(owners[0], 16, encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: 210, Difficulty: 2}))
	runs := make([]*runtimeDungeonState, 2)
	for i := 0; i < 2; i++ {
		runs[i] = owners[i].dungeon.runtime
		if runs[i] == nil || runs[i].raidEntry == nil || runs[i].Dungeon.ID != 210 {
			t.Fatal("real op16 admission failed")
		}
		dispatch(owners[i], 37, make([]byte, 8))
	}
	until := time.Now().Add(createWriteTimeout)
	for time.Now().Before(until) {
		ready := true
		s.raidMu.Lock()
		for i := 0; i < 2; i++ {
			e := b.Participation[owners[i].selectedCharacterID]
			if e.Runtime != runs[i] || e.Session != owners[i] {
				ready = false
			}
		}
		s.raidMu.Unlock()
		if ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 2; i++ {
		s.raidMu.Lock()
		e := b.Participation[owners[i].selectedCharacterID]
		s.raidMu.Unlock()
		if e.Runtime != runs[i] || e.Session != owners[i] {
			t.Fatal("op37 actual placement not committed")
		}
		if err := s.validateCurrentRaidDungeonEntry(owners[i], runs[i]); err != nil {
			t.Fatal(err)
		}
		if runs[i].Session.Snapshot().Run.Status != worldmap.DungeonRunActive || runs[i].settlementClearRewardSent {
			t.Fatal("fixture is not active pre-result raid")
		}
		deadlineAuditV10Event(t, s, owners[i], func() error { return nil })
	}
	for i := 0; i < 2; i++ {
		conns[i].reset()
	}

	return f, owners, conns, runs
}

// A transport-only failure. All admission, persistence and runtime owners remain real.
type raidTableFailAckConn struct {
	*deadlineAuditV10Conn
	fail bool
}

func (c *raidTableFailAckConn) Write(p []byte) (int, error) {
	if c.fail && len(p) >= 3 && p[0] == 1 && binary.LittleEndian.Uint16(p[1:3]) == 72 {
		return 0, errors.New("injected table ACK failure")
	}
	return c.deadlineAuditV10Conn.Write(p)
}
func raidTableDispatch(s *Service, p *gameSession, body []byte) error {
	raw, err := proto.BuildChannelPacket(72, body, 1, 1)
	if err != nil {
		return err
	}
	return s.handleLegacyGamePacket(p, raw)
}
func raidTableCounts(t *testing.T, c *deadlineAuditV10Conn) (ack, scene, giveup int) {
	t.Helper()
	for stream := c.bytes(); len(stream) > 0; {
		p, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if p.Header.MsgID == 72 && p.Header.Classification == 1 {
			ack++
			if !bytes.Equal(p.Body, []byte{1, 1, 2}) {
				t.Fatalf("wrong72ACK %x", p.Body)
			}
		}
		if p.Header.MsgID == 24 && p.Header.Classification == 0 {
			scene++
		}
		if p.Header.MsgID == 42 && p.Header.Classification == 1 {
			giveup++
		}
	}
	return
}
func TestRealPVFRaidTableReturnBoundaries(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	f, peers, conns, runs := raidTableLoaded(t, catalog.source.(*platformpvf.Archive))
	s := f.service
	b := f.state.Battle
	p := peers[1]
	r := runs[1]
	c := conns[1]
	id := p.selectedCharacterID
	e := r.raidEntry
	before, _, err := f.repos.Inventory.Load(context.Background(), strconv.Itoa(int(id)))
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{"no_raid", "wrong_account", "wrong_channel", "wrong_run", "cross_phase", "cross_attempt", "wrong_instance", "foreign_participant", "old_generation", "replaced_socket", "missing_placement", "cleared_journal", "inconsistent_luke_mode", "focus", "retry", "selector", "unknown", "short", "extra"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			c.reset()
			body := []byte{1, 2}
			undo := func() {}
			// Explicit negative faults after a real valid entry. Positive eligibility is never fabricated.
			switch name {
			case "no_raid":
				r.raidEntry = nil
				undo = func() { r.raidEntry = e }
			case "wrong_account":
				old := r.AccountID
				r.AccountID = "foreign"
				undo = func() { r.AccountID = old }
			case "wrong_channel":
				old := p.channel.ID
				p.channel.ID++
				undo = func() { p.channel.ID = old }
			case "wrong_run":
				old := e.RunID
				e.RunID = "foreign"
				undo = func() { e.RunID = old }
			case "cross_phase":
				old := b.Phase
				b.Phase++
				undo = func() { b.Phase = old }
			case "cross_attempt":
				old := e.Attempt
				e.Attempt++
				undo = func() { e.Attempt = old }
			case "wrong_instance":
				old := b.InstanceID
				b.InstanceID = "foreign"
				undo = func() { b.InstanceID = old }
			case "foreign_participant":
				old := e.Participants
				e.Participants = []uint16{999}
				undo = func() { e.Participants = old }
			case "old_generation":
				s.mu.Lock()
				old := p.characterGeneration
				p.characterGeneration++
				s.mu.Unlock()
				undo = func() { s.mu.Lock(); p.characterGeneration = old; s.mu.Unlock() }
			case "replaced_socket":
				s.mu.Lock()
				s.gameSessions[id] = peers[0]
				s.mu.Unlock()
				undo = func() { s.mu.Lock(); s.gameSessions[id] = p; s.mu.Unlock() }
			case "missing_placement":
				old := b.Participation[id]
				delete(b.Participation, id)
				undo = func() { b.Participation[id] = old }
			case "cleared_journal":
				if b.ClearedRuns == nil {
					b.ClearedRuns = map[string]currentRaidClearedRun{}
				}
				b.ClearedRuns[e.RunID] = currentRaidClearedRun{}
				undo = func() { delete(b.ClearedRuns, e.RunID) }
			case "inconsistent_luke_mode":
				old := b.Mode
				b.Mode = 2
				undo = func() { b.Mode = old }
			case "focus":
				body = []byte{2, 2}
			case "retry":
				body = []byte{1, 0}
			case "selector":
				body = []byte{1, 1}
			case "unknown":
				body = []byte{1, 3}
			case "short":
				body = []byte{1}
			case "extra":
				body = []byte{1, 2, 3}
			}
			defer undo()
			deadlineAuditV10Event(t, s, p, func() error { return raidTableDispatch(s, p, body) })
			if r.townReturnPending || r.townReturnAckSent || r.townReturnOp24Sent || p.dungeon.runtime != r || len(c.bytes()) != 0 {
				t.Fatal("invalid owner/action altered return or emitted packet")
			}
		})
	}
	t.Run("ack_failure_no_receipt", func(t *testing.T) {
		c.reset()
		bad := &raidTableFailAckConn{deadlineAuditV10Conn: c, fail: true}
		p.conn = bad
		var observed error
		deadlineAuditV10Event(t, s, p, func() error {
			observed = raidTableDispatch(s, p, []byte{1, 2})
			return nil
		})
		p.conn = c
		if observed == nil {
			t.Fatal("ACK failure not propagated")
		}
		if r.townReturnPending || r.townReturnAckSent || r.townReturnOp24Sent || len(c.bytes()) != 0 {
			t.Fatal("failed ACK consumed return")
		}
	})
	t.Run("typed24_failure_retry_once", func(t *testing.T) {
		c.reset()
		c.failTyped24(true)
		var observed error
		deadlineAuditV10Event(t, s, p, func() error {
			observed = raidTableDispatch(s, p, []byte{1, 2})
			return nil
		})
		if observed == nil {
			t.Fatal("24 failure not propagated")
		}
		ack, n, _ := raidTableCounts(t, c)
		if ack != 1 || n != 0 || !r.townReturnPending || !r.townReturnAckSent || r.townReturnOp24Sent || p.dungeon.runtime != r || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
			t.Fatal("failed24 lost owner or committed run")
		}
		c.failTyped24(false)
		deadlineAuditV10Event(t, s, p, func() error { return raidTableDispatch(s, p, []byte{1, 2}) })
		deadlineAuditV10Event(t, s, p, func() error { return raidTableDispatch(s, p, []byte{1, 2}) })
		ack, n, fortyTwo := raidTableCounts(t, c)
		if ack != 1 || n != 1 || fortyTwo != 0 || r.townReturnRequestMsgID != 72 {
			t.Fatalf("duplicate/wrong confirmation %d %d %d", ack, n, fortyTwo)
		}
	})
	t.Run("phase_changed_before_scene", func(t *testing.T) {
		old := b.Phase
		b.Phase++
		var observed error
		deadlineAuditV10Event(t, s, p, func() error {
			observed = s.commitPendingDungeonReturnForSceneRequest(p, "table-test")
			return nil
		})
		b.Phase = old
		if observed == nil {
			t.Fatal("cross-phase scene committed")
		}
		if p.dungeon.runtime != r || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
			t.Fatal("cross-phase retired run")
		}
	})
	t.Run("member_positive_scene", func(t *testing.T) {
		deadlineAuditV10Event(t, s, p, func() error { return s.sendEnterSelectDungeonState(p, "upper_15", false, true) })
		if p.dungeon.runtime != nil || r.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
			t.Fatal("member return did not commit")
		}
		if peers[0].dungeon.runtime != runs[0] || runs[0].Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
			t.Fatal("member retired leader run")
		}
		if _, ok := s.runtimePartyManagerForService().SnapshotByUser(id, 0); ok {
			t.Fatal("explicit member giveup retained ordinary party seat")
		}
		ack, n, g := raidTableCounts(t, c)
		if ack != 1 || n != 1 || g != 0 {
			t.Fatalf("scene replayed or fabricated ACK: %d %d %d", ack, n, g)
		}
	})
	after, _, err := f.repos.Inventory.Load(context.Background(), strconv.Itoa(int(id)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || len(b.ClearedRuns) != 0 || len(b.MemberClears) != 0 || b.PhaseCompleted {
		t.Fatal("return altered assets or completion")
	}
}
