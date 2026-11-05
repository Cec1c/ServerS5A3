package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/db"
)

var errTownDisplayLazyUnexpectedMode1Read = errors.New("unexpected display-only mode1 dependency read")

type townDisplayLazyDependencyReads struct {
	accountLoads         int
	adventureRosterLoads int
	skillLoads           int
	legacyQueries        int
}

func (reads *townDisplayLazyDependencyReads) total() int {
	// Mode0/HP still read account status and the purchased skill-page flag.
	return reads.adventureRosterLoads + reads.legacyQueries
}

func (reads *townDisplayLazyDependencyReads) install(repositories repository.Group) repository.Group {
	repositories.Account = townDisplayLazyCountingStore[repository.AccountRecord]{
		Store: repositories.Account,
		loads: &reads.accountLoads,
	}
	repositories.Skill = townDisplayLazySkillStore{
		Store: repositories.Skill,
		loads: &reads.skillLoads,
	}
	repositories.Character = townDisplayLazyCharacterRepository{
		CharacterRepository: repositories.Character,
		rosterLoads:         &reads.adventureRosterLoads,
	}
	repositories.LegacyUserInfo = townDisplayLazyLegacyReader{queries: &reads.legacyQueries}
	return repositories
}

type townDisplayLazyCountingStore[T any] struct {
	db.Store[T]
	loads *int
}

func (store townDisplayLazyCountingStore[T]) Load(context.Context, string) (T, bool, error) {
	(*store.loads)++
	var zero T
	return zero, false, errTownDisplayLazyUnexpectedMode1Read
}

type townDisplayLazySkillStore struct {
	db.Store[repository.SkillRecord]
	loads *int
}

func (store townDisplayLazySkillStore) Load(ctx context.Context, id string) (repository.SkillRecord, bool, error) {
	(*store.loads)++
	return store.Store.Load(ctx, id)
}

type townDisplayLazyCharacterRepository struct {
	repository.CharacterRepository
	rosterLoads *int
}

func (repo townDisplayLazyCharacterRepository) ListByAccount(
	context.Context,
	string,
	int,
) ([]repository.CharacterRecord, error) {
	(*repo.rosterLoads)++
	return nil, errTownDisplayLazyUnexpectedMode1Read
}

type townDisplayLazyLegacyReader struct {
	queries *int
}

func (townDisplayLazyLegacyReader) Check(context.Context) error {
	return nil
}

func (reader townDisplayLazyLegacyReader) SelectRows(
	context.Context,
	string,
	string,
	[]string,
	[]string,
) ([]repository.LegacyUserInfoRow, error) {
	(*reader.queries)++
	return nil, errTownDisplayLazyUnexpectedMode1Read
}

func (reader townDisplayLazyLegacyReader) SelectOne(
	context.Context,
	string,
	string,
	[]string,
) (repository.LegacyUserInfoRow, bool, error) {
	(*reader.queries)++
	return nil, false, errTownDisplayLazyUnexpectedMode1Read
}

func newTownDisplayLazyFixture(
	t *testing.T,
	displayOnly bool,
	instrument bool,
) (*Service, *gameSession, *gameSession, *bufferConn, *bufferConn, *townDisplayLazyDependencyReads) {
	t.Helper()
	setting := "off"
	if displayOnly {
		setting = "on"
	}
	t.Setenv(townDisplayOnlyEnv, setting)
	t.Setenv(peerSkillPageEnv, "peer")
	t.Setenv(peerSkillPageTownEnv, "on")
	baseRepositories, service, observer, member, observerConn, memberConn := newTownCoPresenceTestService(t)
	service.options.currentClientVersion = "official-24.9"
	if !instrument {
		return service, observer, member, observerConn, memberConn, nil
	}
	reads := &townDisplayLazyDependencyReads{}
	repositories := reads.install(baseRepositories)
	service.repositoryProvider = func() (repository.Group, bool) { return repositories, true }
	return service, observer, member, observerConn, memberConn, reads
}

func runTownDisplayLazyLightProjection(
	t *testing.T,
	instrument bool,
) ([]upperFrame, *townDisplayLazyDependencyReads) {
	t.Helper()
	service, observer, member, observerConn, _, reads := newTownDisplayLazyFixture(t, true, instrument)
	actor, found := service.onlinePlayers.PlayerForCharacter(member.selectedCharacterID)
	if !found {
		t.Fatal("display actor presence missing")
	}
	const source = "town_copresence_actor_state_newcomer_to_existing"
	if !service.currentTownDisplayOnlyProjection(observer, actor, source) {
		t.Fatal("fixture must be display-only eligible")
	}
	if err := service.sendTownRemoteActorState(observer, actor, source); err != nil {
		t.Fatal(err)
	}
	frames := townDisplayFrames(t, observerConn.write.Bytes())
	requireTownPartyActorHPFrameIDs(t, frames, 2, 9)
	for _, frame := range frames {
		if frame.msgID == 2 && len(frame.body) != 0 && frame.body[0] == 1 {
			t.Fatal("display-only projection wrote mode1")
		}
	}
	return frames, reads
}

func TestTownDisplayLazyBuildSkipsMode1Dependencies(t *testing.T) {
	baseline, _ := runTownDisplayLazyLightProjection(t, false)
	instrumented, reads := runTownDisplayLazyLightProjection(t, true)
	if len(baseline) != len(instrumented) {
		t.Fatalf("display-only frame count changed: baseline=%d instrumented=%d", len(baseline), len(instrumented))
	}
	for index := range baseline {
		if baseline[index].msgID != instrumented[index].msgID || !bytes.Equal(baseline[index].body, instrumented[index].body) {
			t.Fatalf("display-only output changed at frame %d", index)
		}
	}
	if reads.total() != 0 {
		t.Fatalf(
			"display-only construction read mode1 dependencies: account=%d adventure_roster=%d skill=%d legacy=%d",
			reads.accountLoads,
			reads.adventureRosterLoads,
			reads.skillLoads,
			reads.legacyQueries,
		)
	}
	if reads.accountLoads != 1 || reads.skillLoads != 3 {
		t.Fatalf("display-only account/skill loads=%d/%d, want existing mode0/HP reads 1/3", reads.accountLoads, reads.skillLoads)
	}
}

func TestTownDisplayLazyBuildFullProjectionRetainsMode1Dependencies(t *testing.T) {
	service, observer, member, observerConn, _, reads := newTownDisplayLazyFixture(t, false, true)
	actor, found := service.onlinePlayers.PlayerForCharacter(member.selectedCharacterID)
	if !found {
		t.Fatal("full actor presence missing")
	}
	const source = "town_copresence_actor_state_newcomer_to_existing"
	if service.currentTownDisplayOnlyProjection(observer, actor, source) {
		t.Fatal("full fixture unexpectedly display-only eligible")
	}
	if err := service.sendTownRemoteActorState(observer, actor, source); err != nil {
		t.Fatal(err)
	}
	frames := townDisplayFrames(t, observerConn.write.Bytes())
	requireTownPartyActorHPFrameIDs(t, frames, 2, 2, 9)
	if len(frames[1].body) == 0 || frames[1].body[0] != 1 {
		t.Fatal("full projection did not emit mode1")
	}
	if reads.adventureRosterLoads == 0 || reads.skillLoads == 0 || reads.legacyQueries == 0 {
		t.Fatalf(
			"full projection skipped mode1 dependencies: account=%d adventure_roster=%d skill=%d legacy=%d",
			reads.accountLoads,
			reads.adventureRosterLoads,
			reads.skillLoads,
			reads.legacyQueries,
		)
	}
}

func TestTownDisplayLazyBuildPartyPromotionEmitsFullMode1First(t *testing.T) {
	service, observer, member, observerConn, _, reads := newTownDisplayLazyFixture(t, true, true)
	startTownPeerTest(t, service, observer, nil)
	startTownPeerTest(t, service, member, nil)
	drainTownPeerTest(t, service, observer)
	drainTownPeerTest(t, service, member)
	observerConn.write.Reset()
	reads.accountLoads = 0
	reads.adventureRosterLoads = 0
	reads.skillLoads = 0
	reads.legacyQueries = 0
	actor, found := service.onlinePlayers.PlayerForCharacter(member.selectedCharacterID)
	if !found {
		t.Fatal("promotion actor presence missing")
	}
	const source = "town_copresence_actor_state_newcomer_to_existing"
	if err := service.sendTownRemoteActorState(observer, actor, source); err != nil {
		t.Fatal(err)
	}
	if reads.total() != 0 {
		t.Fatalf("initial display-only actor read mode1 dependencies: %+v", reads)
	}
	observerConn.write.Reset()
	if ok, _, reason, members := service.forceManagedRuntimeParty(1001, 1002); !ok || members != 2 {
		t.Fatalf("force managed party: members=%d reason=%s", members, reason)
	}
	drainTownPeerTest(t, service, observer)
	drainTownPeerTest(t, service, member)
	frames := townDisplayFrames(t, observerConn.write.Bytes())
	mode1, roster := townDisplayLazyProjectionOrder(t, frames, member.selectedCharacterID)
	if mode1 < 0 || roster < 0 || mode1 >= roster {
		t.Fatalf("party promotion lacks full mode1 before roster: mode1=%d roster=%d frames=%v", mode1, roster, townPartyActorHPFrameIDs(frames))
	}
	if reads.adventureRosterLoads == 0 || reads.skillLoads == 0 || reads.legacyQueries == 0 {
		t.Fatalf(
			"promotion skipped full mode1 dependencies: account=%d adventure_roster=%d skill=%d legacy=%d",
			reads.accountLoads,
			reads.adventureRosterLoads,
			reads.skillLoads,
			reads.legacyQueries,
		)
	}
}

func TestTownDisplayLazyBuildRechecksEligibilityBeforeWire(t *testing.T) {
	service, observer, member, observerConn, _, _ := newTownDisplayLazyFixture(t, true, false)
	actor, found := service.onlinePlayers.PlayerForCharacter(member.selectedCharacterID)
	if !found {
		t.Fatal("display actor presence missing")
	}
	const source = "town_copresence_actor_state_newcomer_to_existing"
	changed := false
	err := service.sendTownRemoteActorStateAttempt(observer, actor, source, true, func() bool {
		changed = true
		observer.channel.ID++
		return true
	})
	if !changed {
		t.Fatal("construction-current callback was not called")
	}
	if !errors.Is(err, errTownDisplayEligibilityChanged) {
		t.Fatalf("eligibility change error = %v, want retryable", err)
	}
	if observerConn.write.Len() != 0 {
		t.Fatalf("eligibility change wrote a partial actor: %x", observerConn.write.Bytes())
	}
}

func TestTownDisplayLazyBuildRetriesChangedEligibilityOnDeliveryQueue(t *testing.T) {
	for _, initiallyLight := range []bool{true, false} {
		t.Run(map[bool]string{true: "light_to_full", false: "full_to_light"}[initiallyLight], func(t *testing.T) {
			service, observer, member, observerConn, _, _ := newTownDisplayLazyFixture(t, true, false)
			startTownPeerTest(t, service, observer, nil)
			drainTownPeerTest(t, service, observer)
			actor, _ := service.onlinePlayers.PlayerForCharacter(member.selectedCharacterID)
			target, _ := service.onlinePlayers.PlayerForCharacter(observer.selectedCharacterID)
			if !initiallyLight {
				observer.channel.ID++
			}
			observerConn.write.Reset()
			calls := 0
			service.queueTownPeerDelivery(target, "eligibility_retry_test", nil, func() error {
				return service.sendTownRemoteActorStateChecked(observer, actor,
					"town_copresence_actor_state_newcomer_to_existing", true, func() bool {
						calls++
						if calls == 1 {
							if initiallyLight {
								observer.channel.ID++
							} else {
								observer.channel.ID--
							}
						}
						return true
					})
			})
			drainTownPeerTest(t, service, observer)
			if observer.gameTransportContext().Err() != nil {
				t.Fatal("eligibility retry closed a healthy recipient")
			}
			frames := townDisplayFrames(t, observerConn.write.Bytes())
			if initiallyLight {
				requireTownPartyActorHPFrameIDs(t, frames, 2, 2, 9)
			} else {
				requireTownPartyActorHPFrameIDs(t, frames, 2, 9)
			}
			if calls < 2 {
				t.Fatal("eligibility change did not rebuild the body")
			}
		})
	}
}

func TestTownDisplayLazyBuildBoundsRetryWithoutClosingRecipient(t *testing.T) {
	service, observer, member, observerConn, _, _ := newTownDisplayLazyFixture(t, true, false)
	startTownPeerTest(t, service, observer, nil)
	drainTownPeerTest(t, service, observer)
	actor, _ := service.onlinePlayers.PlayerForCharacter(member.selectedCharacterID)
	target, _ := service.onlinePlayers.PlayerForCharacter(observer.selectedCharacterID)
	observerConn.write.Reset()
	calls := 0
	service.queueTownPeerDelivery(target, "eligibility_exhausted_test", nil, func() error {
		return service.sendTownRemoteActorStateChecked(observer, actor,
			"town_copresence_actor_state_newcomer_to_existing", true, func() bool {
				calls++
				observer.channel.ID ^= 1
				return true
			})
	})
	drainTownPeerTest(t, service, observer)
	if calls != 2 || observerConn.write.Len() != 0 {
		t.Fatalf("repeated eligibility change: calls=%d bytes=%d, want two attempts and no prefix", calls, observerConn.write.Len())
	}
	if observer.gameTransportContext().Err() != nil {
		t.Fatal("exhausted construction retry closed a healthy recipient")
	}
}

func townDisplayLazyProjectionOrder(t *testing.T, frames []upperFrame, actorID uint16) (mode1, roster int) {
	t.Helper()
	mode1, roster = -1, -1
	for index, frame := range frames {
		if frame.msgID == 2 && len(frame.body) >= 23 && frame.body[0] == 1 && binary.LittleEndian.Uint16(frame.body[21:]) == actorID {
			if mode1 >= 0 {
				t.Fatal("party promotion repeated full mode1")
			}
			mode1 = index
		}
		if roster < 0 && frame.msgID == 153 {
			for _, member := range parseRealtimeInfoRows(t, frame.body) {
				if member.uid == actorID {
					roster = index
				}
			}
		}
		if roster < 0 && frame.msgID == 9 && len(frame.body) > 9 && frame.body[9] == currentSceneOp9ActorDisplayKind {
			members, _ := parseOp9PartyFrameForProfile(t, frame.body, false, true)
			for _, member := range members {
				if member.uid == actorID {
					roster = index
				}
			}
		}
	}
	return mode1, roster
}
