package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
	"time"
)

// Exercise a retained session's complete rejoin handler after reward display,
// rather than replacing it with a new projection owner (the old restore case).
func TestR158LukeFinishedRejoinKeepsBreakClock(t *testing.T) {
	f, session, at := normalLukeFixture(t)
	// The reducer fixture predates the current directory's channel-encoded key.
	oldKey := f.state.RaidKey
	delete(f.service.raids, oldKey)
	f.state.RaidKey = uint32(f.service.channelAdvertiseID())<<24 | 32<<16 | oldKey&0xffff
	f.service.raids[f.state.RaidKey] = f.state
	peer := f.session()
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.channel, peer.loginPacketSeen, peer.accountFromToken = session.channel, true, true
	f.service.bindGameSessionCharacter(peer, 20)
	for _, id := range []uint32{5054, 5055, 5056} {
		for n := 0; n < 3; n++ {
			clearOwnedLuke(t, f, session, id, at)
			at = at.Add(time.Second)
		}
	}
	flow := f.state.Battle.RewardFlow[0]
	if flow == nil || flow.squadRequired() {
		t.Fatal("actual Luke first phase resource contract")
	}
	for _, now := range []time.Time{flow.AutoSelectAt, flow.AutoSelectAt.Add(65 * time.Second)} {
		if _, errs := f.service.advanceCurrentAntonRewardFlows(now, zeroAntonRoll); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
	if flow.FinishedAt.IsZero() || flow.BreakUntil.IsZero() {
		t.Fatal("real asset flow did not finish")
	}
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, state); err != nil {
		t.Fatal(err)
	}
	before := rejoinAssetSnapshot(t, f.service, session)
	f.conn.write.Reset()
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, state.RaidKey)
	dispatchAntonCardRequest(t, f, session, 668, body)
	stream := append([]byte(nil), f.conn.write.Bytes()...)
	var ends []uint32
	var ack, squad, transitions int
	var remainingKinds []byte
	for len(stream) > 0 {
		p, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if p.Header.MsgID == 668 && len(p.Body) == 1 && p.Body[0] == 1 {
			ack++
		}
		if p.Header.MsgID == 596 && len(p.Body) == 13 && p.Body[0] == 0 {
			ends = append(ends, binary.LittleEndian.Uint32(p.Body[5:9]))
		}
		if p.Header.MsgID == 600 && p.Body[0] == 3 {
			squad++
		}
		if p.Header.MsgID == 597 {
			remainingKinds = append(remainingKinds, p.Body[0])
		}
		if p.Header.MsgID == 587 {
			transitions++
		}
	}
	t.Logf("rejoin=%d active_end=%d break_end=%d emitted_clock_ends=%v", ack, state.Battle.Deadline.Unix(), flow.BreakUntil.Unix(), ends)
	if ack != 1 {
		t.Fatal("fixture did not reach legal rejoin")
	}
	if len(ends) == 0 || ends[len(ends)-1] != uint32(flow.BreakUntil.Unix()) {
		t.Error("completed owner restored old combat clock over current break clock")
	}
	if len(remainingKinds) == 0 || remainingKinds[len(remainingKinds)-1] != 1 {
		t.Error("rejoin restored combat remaining-time kind instead of break kind", remainingKinds)
	}
	if transitions != 0 || !reflect.DeepEqual(before, rejoinAssetSnapshot(t, f.service, session)) {
		t.Fatal("retained rejoin replayed screen or changed assets")
	}
	if squad != 0 || len(flow.Squad) != 0 || f.state.Battle.Phase != 0 {
		t.Fatal("restore invented squad reward/advanced phase")
	}
	// The clock projection is not authority to advance anyone's phase.
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 666, nil)
	p, _ := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if p.Header.MsgID != 666 || !bytes.Equal(p.Body, []byte{0, runtimeRaidFailureCode}) || f.state.Battle.Phase != 0 {
		t.Fatal("rejoin bypassed waiting-area gate")
	}
	enterRaidStartWaitingFixture(t, f, state.Battle.InstanceID)
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 666, nil)
	p, _ = splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if p.Header.MsgID != 666 || !bytes.Equal(p.Body, []byte{1}) || f.state.Battle.Phase != 1 {
		t.Fatal("legal leader next-phase handler blocked", p.Body)
	}
}

func TestR158ActiveLukeRejoinKeepsCombatClockAndAccountGuard(t *testing.T) {
	f, session, _ := normalLukeFixture(t)
	oldKey := f.state.RaidKey
	delete(f.service.raids, oldKey)
	f.state.RaidKey = uint32(f.service.channelAdvertiseID())<<24 | 32<<16 | oldKey&0xffff
	f.service.raids[f.state.RaidKey] = f.state
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, f.state.RaidKey)
	for _, foreign := range []bool{false, true} {
		name := "active"
		if foreign {
			name = "foreign_account"
			f.state.Battle.MemberAccounts[19] = "foreign"
		}
		t.Run(name, func(t *testing.T) {
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, session, 668, body)
			p, stream := splitGameServerUpperPacket(t, f.conn.write.Bytes())
			if foreign {
				if p.Header.MsgID != 668 || !bytes.Equal(p.Body, []byte{0, runtimeRaidFailureCode}) || len(stream) != 0 {
					t.Fatal("foreign account restored owned clock")
				}
				return
			}
			if p.Header.MsgID != 668 || !bytes.Equal(p.Body, []byte{1}) {
				t.Fatal("active rejoin fixture rejected")
			}
			clocks, remaining := 0, 0
			for len(stream) > 0 {
				q, rest := splitGameServerUpperPacket(t, stream)
				stream = rest
				if q.Header.MsgID == 596 && len(q.Body) == 13 && q.Body[0] == 0 {
					clocks++
					if binary.LittleEndian.Uint32(q.Body[5:9]) != uint32(f.state.Battle.Deadline.Unix()) {
						t.Fatal("active clock changed to break")
					}
				}
				if q.Header.MsgID == 597 {
					remaining++
					if q.Body[0] != 0 {
						t.Fatal("active remaining kind changed")
					}
				}
				if q.Header.MsgID == 601 || q.Header.MsgID == 600 || q.Header.MsgID == 587 {
					t.Fatal("active restore fabricated result/transition")
				}
			}
			if clocks != 1 || remaining != 1 {
				t.Fatal("active clock missing", clocks, remaining)
			}
		})
	}
}
