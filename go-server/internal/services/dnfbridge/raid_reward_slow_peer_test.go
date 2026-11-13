package dnfbridge

import (
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

type raidRewardGatedConn struct {
	net.Conn
	writing chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *raidRewardGatedConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.writing) })
	// Hold the production projection/transport locks deterministically. The
	// real pipe applies its write deadline when this test gate is released.
	<-c.release
	return c.Conn.Write(p)
}

// Hold one owner's write while the other completes authentic 659/660 requests
// and reward transactions. The gate proves independence without requiring PVF
// parsing and in-memory transactions to finish within a three-second VM budget.
// After release, the real unread pipe must fail by its own deadline or closure.
func TestRealPVFRaidRewardBlockedPeerDoesNotHoldHealthyOwner(t *testing.T) {
	for _, phase := range []string{"anton_phase0", "anton_phase1"} {
		for _, completed := range []byte{0, 1} {
			for _, disconnect := range []bool{false, true} {
				name := phase + "/movie_prompt"
				if completed == 1 {
					name = phase + "/party_masks"
				}
				if disconnect {
					name += "/disconnect"
				} else {
					name += "/write_deadline"
				}
				t.Run(name, func(t *testing.T) {
					f, sessions := r195MovieStageFixture(t, phase)
					warmAntonRewardQuestFixture(t, f)
					ensureRaidRewardAccount(t, f, "account-20")
					slow, fast := sessions[0], sessions[1]
					flow := f.state.Battle.RewardFlow[f.state.Battle.Phase]
					flow.AutoSelectAt = time.Now().Add(5 * time.Minute)
					flow.PartySelectAfter = nil
					left, right := net.Pipe()
					t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
					blocked := &raidRewardGatedConn{Conn: left, writing: make(chan struct{}), release: make(chan struct{})}
					var releaseOnce sync.Once
					release := func() { releaseOnce.Do(func() { close(blocked.release) }) }
					t.Cleanup(release)
					slow.conn = blocked
					frame, err := dnfproto.BuildChannelPacket(659, []byte{completed}, 1, 1)
					if err != nil {
						t.Fatal(err)
					}
					done := make(chan error, 1)
					go func() { done <- f.service.handleGameUpper(slow, frame) }()
					select {
					case <-blocked.writing:
					case <-time.After(time.Second):
						t.Fatal("slow peer never entered its transport write")
					}
					conn := fast.conn.(*bufferConn)
					conn.write.Reset()
					dispatchAntonCardRequest(t, f, fast, 659, []byte{1})
					wire := r195ReadFiendRewardWire(t, conn.write.Bytes())
					for _, kind := range []byte{0, 1} {
						if !reflect.DeepEqual(wire.owners[kind], []uint16{19, 20}) {
							t.Fatalf("blocked peer prevented healthy party masks: %+v", wire)
						}
					}
					conn.write.Reset()
					dispatchAntonCardRequest(t, f, fast, 660, []byte{1, 0})
					if _, errs := f.service.advanceCurrentAntonRewardFlows(time.Now(), zeroAntonRoll); len(errs) != 0 {
						t.Fatal(errs)
					}
					if !currentRaidOwnerRewardsCommitted(flow, fast.selectedCharacterID) || currentRaidOwnerRewardsCommitted(flow, slow.selectedCharacterID) || !flow.FinishedAt.IsZero() {
						t.Fatal("healthy completion depends on blocked peer or fabricates its party grant")
					}
					if err := f.service.sendCurrentAntonRewardFlowProjection(fast, cloneRuntimeRaidState(*f.state)); err != nil {
						t.Fatal(err)
					}
					wire = r195ReadFiendRewardWire(t, conn.write.Bytes())
					if !reflect.DeepEqual(wire.owners[3], []uint16{19, 20}) {
						t.Fatalf("blocked peer prevented healthy team cards: %+v", wire)
					}
					select {
					case err := <-done:
						t.Fatalf("healthy owner only completed after blocked write ended: %v", err)
					default:
					}
					before := r195PublicRewardAssetSnapshot(t, f.service, fast)
					if disconnect {
						_ = right.Close()
					}
					release()
					select {
					case err := <-done:
						if err == nil || !slow.raidProjection.failed {
							t.Fatal("faulted transport was accepted as a successful projection", err)
						}
					case <-time.After(createWriteTimeout + time.Second):
						t.Fatal("blocked write did not finish within its own deadline")
					}
					dispatchAntonCardRequest(t, f, fast, 660, []byte{1, 3})
					if after := r195PublicRewardAssetSnapshot(t, f.service, fast); !reflect.DeepEqual(before, after) || fast.raidProjection.failed {
						t.Fatal("peer disconnect changed healthy rewards or failed the healthy stream")
					}
				})
			}
		}
	}
}
