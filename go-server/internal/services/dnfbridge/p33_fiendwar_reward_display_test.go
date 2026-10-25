package dnfbridge

import (
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestP33RealFiendWarRewardDisplayUsesCurrentGroupAfterRetirement(t *testing.T) {
	for _, change := range []string{"same_group_without_temporary_party", "departed", "changed_group", "rejoined_same_group"} {
		t.Run(change, func(t *testing.T) {
			f, peers := actualCurrentFiendWar(t, 2)
			b := f.state.Battle
			f.state.Members[1].GroupIndex = f.state.Members[0].GroupIndex
			b.MemberGroups[20] = b.MemberGroups[19]
			f.state.Started = true
			b.EnterAfter, b.Deadline = time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
			b.PhaseCompleted, b.FiendWar.Complete = true, true
			b.PhaseCompletedAt = time.Now()
			b.ClearedRuns = map[string]currentRaidClearedRun{}
			for i := uint32(0); i < b.AntonRewards.Phases[0].MinimumClears; i++ {
				b.ClearedRuns[strconv.Itoa(int(i))] = currentRaidClearedRun{DungeonID: 4978, Attempt: 1, Participants: []uint16{19, 20}}
			}
			if err := f.service.freezeCurrentFiendWarRewardFlowLocked(*f.state); err != nil {
				t.Fatal(err)
			}
			flow := b.RewardFlow[0]
			if _, errs := f.service.commitCurrentRaidWeeklyIntents(b.PhaseCompletedAt); len(errs) != 0 {
				t.Fatal(errs)
			}
			if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
				t.Fatal(errs)
			}
			if flow.SquadShownAt.IsZero() || len(flow.Recipients) != 2 {
				t.Fatal("missing committed two-owner fixture")
			}
			frozen := cloneCurrentAntonRewardFlow(flow)
			peer := peers[0]
			if err := f.service.sendCurrentAntonTerminalResult(peer, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			if err := f.service.sendSelectedCurrentContainerListsRefresh(peer, "p33_before_display"); err != nil {
				t.Fatal(err)
			}
			assets := r195PublicRewardAssetSnapshot(t, f.service, peer)
			currentPeer := f.state.Members[1]
			wantParty := []uint16{19, 20}
			switch change {
			case "departed":
				f.state.Members = f.state.Members[:1]
				wantParty = []uint16{19}
			case "changed_group":
				f.state.Members[1].GroupIndex++
				wantParty = []uint16{19}
			case "rejoined_same_group":
				f.state.Members = f.state.Members[:1]
				f.state.Members = append(f.state.Members, currentPeer)
			}
			conn := peer.conn.(*bufferConn)
			conn.write.Reset()
			if err := f.service.sendCurrentAntonRewardFlowProjection(peer, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			r195WantFiendRewardWire(t, r195ReadFiendRewardWire(t, conn.write.Bytes()), flow,
				map[byte][]uint16{1: wantParty, 3: {19, 20}}, "state4", "kind1", "kind3")
			if !reflect.DeepEqual(frozen.Party, flow.Party) || !reflect.DeepEqual(frozen.Squad, flow.Squad) || !reflect.DeepEqual(frozen.Recipients, flow.Recipients) {
				t.Fatal("current-group display changed frozen reward entitlement")
			}
			if after := r195PublicRewardAssetSnapshot(t, f.service, peer); !reflect.DeepEqual(assets, after) {
				t.Fatal("display changed settled assets")
			}
			conn.write.Reset()
			if err := f.service.sendCurrentAntonRewardFlowProjection(peer, cloneRuntimeRaidState(*f.state)); err != nil || conn.write.Len() != 0 {
				t.Fatalf("unchanged current-group projection replayed: err=%v bytes=%d", err, conn.write.Len())
			}
		})
	}
}
