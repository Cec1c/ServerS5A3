package dnfbridge

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/party"
	"longheng.io/server/internal/modules/dnf/raid"
)

func TestRaidJoinSharedReadsProfilesAndAdmissionUseOneOwnerRead(t *testing.T) {
	f := newRaidEntryCostInventoryRefreshFixture(t)
	for _, id := range []string{"19", "20"} {
		character, _, err := f.repos.Character.Load(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		character.Name, character.Job, character.Level = "member"+id, "0", 90
		if err := f.repos.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}
	}
	loads := wrapCurrentCrossSceneProfileBatchCharacterLoads(t, f.service, nil)
	for round := 1; round <= 2; round++ {
		f.leaderConn.write.Reset()
		f.peerConn.write.Reset()
		f.setMaterial(t, f.leader.selectedCharacterID, int64(round-1))
		if err := f.service.broadcastRuntimeRaidProjectionWithAppearance(f.state, true, 0, false); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"19", "20"} {
			if loads[id] != round {
				t.Fatalf("broadcast %d reread owner %s: %d", round, id, loads[id])
			}
		}
		for _, conn := range []*bufferConn{f.leaderConn, f.peerConn} {
			seen := map[uint16]int{}
			for _, frame := range splitUpperFrames(t, conn.write.Bytes()) {
				seen[frame.msgID]++
				if frame.msgID == 598 {
					assertEntryCostMemberStatus(t, frame.body, 0, 19, byte(round-1), uint16(round-1))
				}
			}
			if seen[752] != 1 || seen[591] != 1 || seen[598] != 1 {
				t.Fatal("shared ownership lost current profiles/roster/inventory", seen)
			}
		}
	}
}

func TestRaidJoinSharedReadsApprovalReleasesRaidLockAndRevalidates(t *testing.T) {
	for _, mutation := range []string{"none", "replace_raid", "replace_request", "applicant_generation", "started"} {
		t.Run(mutation, func(t *testing.T) {
			s, leader, applicant, lc, ac, state := prepareCurrentRaidApplicationTest(t)
			if err := s.handleCurrentRaidApplication(applicant, 820, raid.CurrentOtherChannelJoinRequest{TargetCharKey: leader.selectedCharacterID, TargetChannel: 23}); err != nil {
				t.Fatal(err)
			}
			lc.write.Reset()
			ac.write.Reset()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			wrapCurrentCrossSceneProfileBatchCharacterLoads(t, s, func(id string) {
				if id == strconv.Itoa(int(applicant.selectedCharacterID)) {
					once.Do(func() { close(entered); <-release })
				}
			})
			done := make(chan error, 1)
			go func() {
				done <- s.handleCurrentRaidApproval(leader, 11, party.ResponsePeerRequest{TargetID: applicant.selectedCharacterID, Mode: 10})
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				close(release)
				t.Fatal("approval did not reach authoritative applicant read")
			}
			if !s.raidMu.TryLock() {
				close(release)
				<-done
				t.Fatal("slow applicant repository held the shared raid lock")
			}
			switch mutation {
			case "replace_raid":
				copy := cloneRuntimeRaidState(*s.raids[state.RaidKey])
				s.raids[state.RaidKey] = &copy
			case "replace_request":
				p := s.currentRaidApplications[applicant.selectedCharacterID]
				p.expires = p.expires.Add(time.Second)
				s.currentRaidApplications[applicant.selectedCharacterID] = p
			case "applicant_generation":
				advanceGameSessionCharacterGeneration(applicant)
			case "started":
				s.raids[state.RaidKey].Started = true
			}
			s.raidMu.Unlock()
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			_, joined := s.runtimeRaidForMember(applicant.selectedCharacterID)
			if joined != (mutation == "none") {
				t.Fatalf("stale approval commit: %s joined=%t", mutation, joined)
			}
		})
	}
}
