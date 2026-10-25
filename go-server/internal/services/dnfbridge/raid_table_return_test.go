package dnfbridge

import (
	"context"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Uses the existing real-resource admission fixture: op4/143/666, then real
// op16/37. No clear, reward, run or participation owner is fabricated.
func TestRealPVFRaidTableReturnActive(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	a := catalog.source.(*platformpvf.Archive)
	for _, kind := range []string{"op42_control", "table72"} {
		t.Run(kind, func(t *testing.T) {
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
			before, _, err := f.repos.Character.Load(context.Background(), strconv.Itoa(int(owners[0].selectedCharacterID)))
			if err != nil {
				t.Fatal(err)
			}
			op := uint16(42)
			var body []byte
			if kind == "table72" {
				op = 72
				body = []byte{1, 2}
			}
			dispatch(owners[0], op, body)
			after, _, err := f.repos.Character.Load(context.Background(), strconv.Itoa(int(owners[0].selectedCharacterID)))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("post request session_runtime_same=%v session_runtime_nil=%v old_run_status=%v old_pending=%v old_typed24=%v persistent_before=%+v persistent_after=%+v", owners[0].dungeon.runtime == runs[0], owners[0].dungeon.runtime == nil, runs[0].Session.Snapshot().Run.Status, runs[0].townReturnPending, runs[0].townReturnOp24Sent, before.Stats, after.Stats)
			packets := 0
			for stream := conns[0].bytes(); len(stream) > 0; {
				p, rest := splitGameServerUpperPacket(t, stream)
				stream = rest
				t.Logf("response class=%d op=%d body=%x", p.Header.Classification, p.Header.MsgID, p.Body)
				if p.Header.Classification == 0 && p.Header.MsgID == 24 {
					packets++
				}
			}
			r := runs[0]
			if !r.townReturnPending || !r.townReturnOp24Sent || packets != 1 || r.townReturnRequestMsgID != op {
				t.Fatalf("native table/active return lost: pending=%v typed24=%v count24=%d owner=%d want=%d", r.townReturnPending, r.townReturnOp24Sent, packets, r.townReturnRequestMsgID, op)
			}
			dispatch(owners[0], op, body)
			count := 0
			for stream := conns[0].bytes(); len(stream) > 0; {
				p, rest := splitGameServerUpperPacket(t, stream)
				stream = rest
				if p.Header.Classification == 0 && p.Header.MsgID == 24 {
					count++
				}
			}
			if count != 1 {
				t.Fatal("replay repeated typed transition")
			}
			if r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || owners[0].dungeon.runtime != r {
				t.Fatal("socket write retired run before scene evidence")
			}
			deadlineAuditV10Event(t, s, owners[0], func() error { return s.sendEnterSelectDungeonState(owners[0], "upper_15", false, true) })
			if owners[0].dungeon.runtime != nil || r.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
				t.Fatal("positive town callback did not retire own run")
			}
			if owners[1].dungeon.runtime != runs[1] || runs[1].Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
				t.Fatal("retired peer run")
			}
			if len(b.ClearedRuns) != 0 || len(b.MemberClears) != 0 || b.PhaseCompleted {
				t.Fatal("manufactured clear/reward")
			}
		})
	}
}
