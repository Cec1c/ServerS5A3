package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// MySQL honors an expired context; the memory fixture normally does not.
func raidPreparationCharacterReader(t *testing.T, s *Service, load func(context.Context, string) error) {
	t.Helper()
	repos, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("repositories unavailable")
	}
	previous := repos.Character
	repos.Character = crossSceneProfileCharacterReader{CharacterRepository: previous,
		load: func(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
			if err := load(ctx, id); err != nil {
				return dnfrepo.CharacterRecord{}, false, err
			}
			return previous.Load(ctx, id)
		}}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
}

func TestRaidPreparationTransientReadAndSlowPeerDoNotDisconnectHealthyMember(t *testing.T) {
	s, slow, healthy, _, healthyConn, state := prepareApprovedCurrentRaid(t)
	failedRead := false
	raidPreparationCharacterReader(t, s, func(ctx context.Context, id string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if id == strconv.Itoa(int(healthy.selectedCharacterID)) && !failedRead {
			failedRead = true
			return errors.New("transient compact-profile read failure")
		}
		return nil
	})
	left, unread := net.Pipe()
	t.Cleanup(func() { _ = left.Close(); _ = unread.Close() })
	slow.conn = left
	// Previously a missing read was ignored, allowing an incomplete profile
	// to reach an unread socket. Its timeout then exhausted the shared budget
	// before the uncached member's owner read. Failed preparation must instead
	// stop before encoding and let the later member obtain a complete snapshot.
	if err := s.broadcastRuntimeRaidRefresh(state); err != nil {
		t.Fatal(err)
	}
	if healthy.gameTransportContext().Err() != nil {
		t.Fatal("another member's expired preparation disconnected a healthy stream")
	}
	if !failedRead || healthyConn.write.Len() == 0 {
		t.Fatal("fresh recipient did not recover the incomplete preparation")
	}
	profile, _ := splitGameServerUpperPacket(t, healthyConn.write.Bytes())
	if profile.Header.MsgID != raid.CurrentRaidProfilesMsgID || len(profile.Body) != 4+57*len(state.Members) {
		t.Fatal("roster was not preceded by all required compact profiles")
	}
	// A fresh bounded refresh must recover without changing committed members.
	if err := s.broadcastRuntimeRaidRefresh(state); err != nil {
		t.Fatal(err)
	}
	if healthyConn.write.Len() == 0 || healthy.gameTransportContext().Err() != nil {
		t.Fatal("deferred recipient did not recover on the next refresh")
	}
	live, ok := s.runtimeRaidForMember(healthy.selectedCharacterID)
	if !ok || len(live.Members) != len(state.Members) {
		t.Fatal("preparation failure changed committed membership")
	}
}

func TestRaidPreparationCancelledBeforeWriteKeepsConnectionAndSequence(t *testing.T) {
	s, peer, _, conn, _, state := prepareApprovedCurrentRaid(t)
	raidPreparationCharacterReader(t, s, func(ctx context.Context, _ string) error { return ctx.Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sequence := peer.upperSeq
	err := s.sendCurrentRuntimeRaidProfilesWithBatch(peer, state, newCurrentRaidProfileBatch(s, ctx))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("preparation lost cancellation: %v", err)
	}
	s.failRuntimeRaidRecipient(peer, "profiles", err)
	if peer.gameTransportContext().Err() != nil || conn.write.Len() != 0 || peer.upperSeq != sequence {
		t.Fatal("cancelled preparation retired or encoded an otherwise healthy stream")
	}
}

func TestRaidPreparationInfoReadFailureIsRetryableWithoutPartialProfiles(t *testing.T) {
	for _, failFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "owner_read", false: "compact_profile_read"}[failFirst], func(t *testing.T) {
			s, peer, _, conn, _, state := prepareApprovedCurrentRaid(t)
			calls := 0
			broken := true
			raidPreparationCharacterReader(t, s, func(ctx context.Context, _ string) error {
				calls++
				threshold := 4
				if failFirst {
					threshold = 3
				}
				if broken && calls >= threshold {
					return errors.New("temporary character read unavailable")
				}
				return ctx.Err()
			})
			request := make([]byte, 5)
			request[0] = 2
			binary.LittleEndian.PutUint32(request[1:], state.RaidKey)
			sequence := peer.upperSeq
			if err := s.handleOnlineRequestRaidInfo(peer, request); err != nil {
				t.Fatalf("read failure escaped into command-loop disconnect: %v", err)
			}
			if conn.write.Len() != 0 || peer.upperSeq != sequence || peer.gameTransportContext().Err() != nil {
				t.Fatal("failed preparation published incomplete profiles or retired the stream")
			}
			broken = false
			if err := s.handleOnlineRequestRaidInfo(peer, request); err != nil || conn.write.Len() == 0 {
				t.Fatalf("fresh request could not retry: %v", err)
			}
		})
	}
}

func TestRealPVFRaidPreparationRestoreReadFailureKeepsEntireDependentTailPending(t *testing.T) {
	for _, selector := range []bool{true, false} {
		t.Run(map[bool]string{true: "selector", false: "finish_loading"}[selector], func(t *testing.T) {
			f := newRaidSelectorRestoreR195Fixture(t)
			if _, err := f.service.initialCharacterSkills(context.Background(), 0); err != nil {
				t.Fatal(err)
			}
			broken := true
			raidPreparationCharacterReader(t, f.service, func(ctx context.Context, _ string) error {
				if broken && repositoryMemoFromContext(ctx) != nil {
					return errors.New("temporary restore profile read failure")
				}
				return ctx.Err()
			})
			request := func() error {
				if selector {
					return r195DispatchRealUpper15(t, f.service, f.session)
				}
				return f.service.sendCurrentFinishLoadingCharacterState(f.session, "preparation_failure_test")
			}
			if err := request(); err != nil {
				t.Fatalf("preparation failure escaped outer request boundary: %v", err)
			}
			pending, initialized, failed := r195RaidSessionRestoreFlags(f.session)
			if !pending || initialized || failed || f.conn.write.Len() != 0 || f.session.gameTransportContext().Err() != nil {
				t.Fatal("incomplete restore allowed dependent selector/loading/progress packets")
			}
			broken = false
			if err := request(); err != nil {
				t.Fatalf("restore retry failed: %v", err)
			}
			pending, _, failed = r195RaidSessionRestoreFlags(f.session)
			if pending || failed || f.conn.write.Len() == 0 {
				t.Fatal("fresh restore did not finish after repository recovery")
			}
		})
	}
}
