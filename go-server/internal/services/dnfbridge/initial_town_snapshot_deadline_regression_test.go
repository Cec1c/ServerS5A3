package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type initialTownDeadlineCharacterStore struct {
	dnfrepo.CharacterRepository
	firstContext context.Context
}

func (s *initialTownDeadlineCharacterStore) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	if s.firstContext == nil {
		s.firstContext = ctx
	}
	return s.CharacterRepository.Load(ctx, id)
}

type initialTownDeadlineInventoryStore struct {
	dnfrepo.InventoryRepository
	delay   time.Duration
	delayed bool
	elapsed time.Duration
}

func (s *initialTownDeadlineInventoryStore) Load(ctx context.Context, id string) (dnfrepo.InventoryRecord, bool, error) {
	// Delay the independent inventory snapshot, not an earlier short-budget
	// cleanup read. Its synchronous work still consumes the enclosing budget.
	if deadline, ok := ctx.Deadline(); ok && !s.delayed && s.delay > 0 && time.Until(deadline) > 4*time.Second {
		s.delayed = true
		started := time.Now()
		time.Sleep(s.delay)
		s.elapsed = time.Since(started)
	}
	if err := ctx.Err(); err != nil {
		return dnfrepo.InventoryRecord{}, false, err
	}
	return s.InventoryRepository.Load(ctx, id)
}

type initialTownDeadlineQuestStore struct {
	dnfrepo.QuestRepository
	character      *initialTownDeadlineCharacterStore
	failure        error
	outerCalls     int
	outerErr       error
	outerRemaining time.Duration
	outerOwner     string
}

func (s *initialTownDeadlineQuestStore) Load(ctx context.Context, id string) (dnfrepo.QuestRecord, bool, error) {
	if ctx == s.character.firstContext {
		s.outerCalls++
		s.outerErr = ctx.Err()
		s.outerOwner = id
		if deadline, ok := ctx.Deadline(); ok {
			s.outerRemaining = time.Until(deadline)
		}
		if s.outerErr != nil {
			return dnfrepo.QuestRecord{}, false, s.outerErr
		}
		if s.failure != nil {
			return dnfrepo.QuestRecord{}, false, s.failure
		}
	}
	return s.QuestRepository.Load(ctx, id)
}

func TestInitialTownSnapshotDeadlineSurvivesSlowBootstrap(t *testing.T) {
	wantFailure := errors.New("quest snapshot unavailable")
	for _, tc := range []struct {
		name    string
		delay   time.Duration
		failure error
	}{
		{name: "snapshot_after_3_seconds", delay: 3100 * time.Millisecond},
		{name: "repository_error_withholds_transition", failure: wantFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, session, repositories := newTownMoveTest(t)
			service.options.gameUpperHeader = gameUpperHeaderServer16
			service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			service.options.accountPrefix = defaultAccountPrefix
			service.questCatalog = buildQuestListTestCatalog(t)
			service.adventureGroupTable = loadAdventureGroupTestTables(t)
			session.accountID = "dnf:1"
			session.townSceneReadyCharacterID = 0
			ctx := context.Background()
			if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
				AccountID: "dnf:1",
				Metadata:  map[string]string{currentRentalPointMetadataKey: "0"},
			}); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "29"}); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
				CharacterID: "29",
				States: map[int64]dnfrepo.QuestState{
					3149: {Status: "completed"},
					3152: {Status: "active"},
				},
			}); err != nil {
				t.Fatal(err)
			}
			service.armCurrentInitialTownRoute(session, 29)
			charID, charName, character, found := service.selectedCharacterForEnter(ctx, session)
			if charID != 29 || !found {
				t.Fatalf("selected character=%d found=%t", charID, found)
			}
			objectBody := service.buildCurrentSceneObjectListBodyForSession(ctx, session, charID, charName, character, found)
			row := currentSceneTransitionRow{
				ObjectOrResourceKey: currentSceneActorObjectKey(charID),
				Value1:              450,
				Value2:              234,
				Value3:              5,
				Value4:              3,
			}
			transitionBody, err := buildCurrentSceneTransitionBody(38, 1, []currentSceneTransitionRow{row})
			if err != nil {
				t.Fatal(err)
			}
			characters := &initialTownDeadlineCharacterStore{CharacterRepository: repositories.Character}
			inventory := &initialTownDeadlineInventoryStore{InventoryRepository: repositories.Inventory, delay: tc.delay}
			quests := &initialTownDeadlineQuestStore{QuestRepository: repositories.Quest, character: characters, failure: tc.failure}
			repositories.Character = characters
			repositories.Inventory = inventory
			repositories.Quest = quests
			service.repositoryProvider = func() (dnfrepo.Group, bool) { return repositories, true }
			connection := session.conn.(*bufferConn)
			connection.write.Reset()
			started := time.Now()
			session.townMu.Lock()
			err = service.sendCurrentInitialTownActorRoutePacketsLocked(
				session, charID, 38, 1, row, objectBody,
				buildCurrentActorBindingMode1Body(row.ObjectOrResourceKey, 0), transitionBody,
			)
			session.townMu.Unlock()
			elapsed := time.Since(started)
			t.Logf("initialization=%s inventory_delay=%s outer_quest_calls=%d remaining=%s context_error=%v route_error=%v",
				elapsed, inventory.elapsed, quests.outerCalls, quests.outerRemaining, quests.outerErr, err)
			if tc.delay > 0 && (!inventory.delayed || inventory.elapsed <= 3*time.Second || elapsed >= 15*time.Second) {
				t.Fatalf("slow bootstrap not exercised in (3s,15s): delayed=%t delay=%s total=%s", inventory.delayed, inventory.elapsed, elapsed)
			}
			if quests.outerCalls == 0 || quests.outerOwner != "29" {
				t.Fatalf("final quest snapshot did not use selected character's enclosing context: calls=%d owner=%q", quests.outerCalls, quests.outerOwner)
			}
			if tc.failure != nil {
				if !errors.Is(err, tc.failure) || !strings.Contains(err.Error(), "load clear quest list for character 29") {
					t.Fatalf("quest error did not fail closed at clear-list boundary: %v", err)
				}
			} else if err != nil || quests.outerErr != nil || quests.outerRemaining <= 0 {
				t.Fatalf("slow healthy initialization lost snapshot budget: err=%v quest_context=%v remaining=%s", err, quests.outerErr, quests.outerRemaining)
			}

			clearIndex, skillIndex, acceptableIndex, activeIndex, transitionIndex := -1, -1, -1, -1, -1
			for stream, index := connection.write.Bytes(), 0; len(stream) > 0; index++ {
				wire := stream
				packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
				stream = rest
				switch packet.Header.MsgID {
				case currentSkillInfoMsgID:
					skillIndex = index
				case currentAcceptableQuestListMsgID:
					acceptableIndex = index
				case currentActiveQuestSnapshotMsgID:
					activeIndex = index
				case currentClearQuestListMsgID:
					plain, decodeErr := zlibDecompress(wire[16:packet.Header.Length])
					if decodeErr != nil || len(plain) != 4+currentClearQuestListPayloadSize || binary.LittleEndian.Uint32(plain[:4]) != currentClearQuestListPayloadSize {
						t.Fatalf("invalid clear-quest snapshot: len=%d err=%v", len(plain), decodeErr)
					}
					if plain[4+3149] != 1 || plain[4+3152] != 0 {
						t.Fatalf("clear-quest snapshot lost persisted state: completed=%d active=%d", plain[4+3149], plain[4+3152])
					}
					clearIndex = index
				case currentSceneTransitionMsgID:
					if packet.Header.Classification != 0 || !bytes.Equal(wire[16:packet.Header.Length], transitionBody) {
						t.Fatalf("unexpected typed town transition: %x", wire[16:packet.Header.Length])
					}
					transitionIndex = index
				case uint16(dnfenum.CmdPacketFinishLoading):
					t.Fatal("initialization fabricated a finish-loading packet")
				}
			}
			if tc.failure != nil {
				if clearIndex >= 0 || transitionIndex >= 0 || session.initialTownRouteStage >= currentInitialTownRouteTransitionSent {
					t.Fatalf("failed quest snapshot emitted clear-list or committed town: clear=%d transition=%d stage=%d", clearIndex, transitionIndex, session.initialTownRouteStage)
				}
				return
			}
			if skillIndex < 0 || acceptableIndex <= skillIndex || activeIndex <= acceptableIndex || clearIndex <= activeIndex || transitionIndex <= clearIndex {
				t.Fatalf("snapshot/commit order: skill=%d acceptable=%d active=%d clear=%d transition=%d", skillIndex, acceptableIndex, activeIndex, clearIndex, transitionIndex)
			}
			if session.initialTownRouteStage != currentInitialTownRoutePlayerStateSent || session.townSceneReadyCharacterID != 29 {
				t.Fatalf("healthy snapshots did not finalize selected actor: stage=%d ready_character=%d", session.initialTownRouteStage, session.townSceneReadyCharacterID)
			}
		})
	}
}
