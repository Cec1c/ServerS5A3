package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestLukeSelectorProgressUsesPVFBitOrderAndBooleanClears(t *testing.T) {
	sequence := currentLukeNormalSequence{Dungeons: []uint32{5103, 5101, 5102, 5106}}
	record := dnfrepo.DungeonPermissionRecord{Entries: []dnfrepo.DungeonPermissionEntry{
		{DungeonID: 5101, ClearState: 3}, {DungeonID: 5102, ClearState: 0},
		{DungeonID: 5106, ClearState: 1}, {DungeonID: 999, ClearState: 4},
	}}
	if got := buildCurrentLukeSelectorProgress(sequence, record); !bytes.Equal(got, []byte{42, 0, 0, 0, 0, 10, 0, 0, 0}) {
		t.Fatalf("noncontiguous clears=%x", got)
	}
	record.Entries = append(record.Entries, dnfrepo.DungeonPermissionEntry{DungeonID: 5103, ClearState: 2})
	if got := buildCurrentLukeSelectorProgress(sequence, record); !bytes.Equal(got, []byte{42, 0, 0, 0, 2, 11, 0, 0, 0}) {
		t.Fatalf("prefix clears=%x", got)
	}
}

func TestLukeSelectorQueryDispatchIsReadOnlyAndCharacterScoped(t *testing.T) {
	for _, transport := range []string{"legacy", "upper"} {
		t.Run(transport, func(t *testing.T) {
			f := newSeekerLukeNextFixture(t, seekerLukeNextSource([]uint32{5101, 5102, 5103, 5104, 5105, 5106}, []uint32{5101, 5102, 5103, 5104, 5105, 5106}, true))
			ctx := context.Background()
			for _, id := range []uint32{5101, 5102, 5103, 5104, 5105} {
				if _, _, err := f.repos.DungeonPermission.UpsertMax(ctx, "99", id, 3); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := f.repos.DungeonPermission.UpsertMax(ctx, "98", 5106, 3); err != nil {
				t.Fatal(err)
			}
			before, _, _ := f.repos.DungeonPermission.Load(ctx, "99")
			for repeat := 0; repeat < 2; repeat++ {
				req := gameplayRequest{Opcode: 869, Classification: 1, Body: []byte{42, 0, 0, 0}}
				var handled bool
				var err error
				if transport == "legacy" {
					handled, err = currentGameplayModules.dispatchLegacyRequest(f.service, f.session, req)
				} else {
					handled, err = currentGameplayModules.dispatchUpperRequest(f.service, f.session, req)
				}
				if !handled || err != nil {
					t.Fatalf("handled=%v error=%v", handled, err)
				}
			}
			stream := f.conn.write.Bytes()
			for repeat := 0; repeat < 2; repeat++ {
				p, rest := splitGameServerUpperPacket(t, stream)
				stream = rest
				if p.Header.MsgID != 602 || p.Header.Classification != 0 || !bytes.Equal(p.Body, []byte{42, 0, 0, 0, 5, 31, 0, 0, 0}) {
					t.Fatalf("header=%+v body=%x", p.Header, p.Body)
				}
			}
			if len(stream) != 0 {
				t.Fatal("unexpected ACK or extra notification")
			}
			after, _, _ := f.repos.DungeonPermission.Load(ctx, "99")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("query mutated clear authority")
			}
		})
	}
}

func TestLukeSelectorQueryRejectsUnownedMalformedAndMissingResources(t *testing.T) {
	for _, failure := range []string{"short", "trailing", "other_sequence", "wrong_class", "no_character", "foreign_owner", "missing_pvf", "oversize_sequence", "foreign_record"} {
		t.Run(failure, func(t *testing.T) {
			f := newSeekerLukeNextFixture(t, seekerLukeNextSource([]uint32{5101, 5106}, []uint32{5101, 5106}, true))
			req := gameplayRequest{Opcode: 869, Classification: 1, Body: []byte{42, 0, 0, 0}}
			switch failure {
			case "short":
				req.Body = []byte{42}
			case "trailing":
				req.Body = append(req.Body, 0)
			case "other_sequence":
				req.Body[0] = 28
			case "wrong_class":
				req.Classification = 0
			case "no_character":
				f.session.selectedCharacterID = 0
			case "foreign_owner":
				f.session.accountID = "other"
			case "missing_pvf":
				delete(f.source, "etc/sequential_dungeon_info.etc")
			case "oversize_sequence":
				ids := make([]uint32, 9)
				for i := range ids {
					ids[i] = uint32(i + 1)
				}
				f.source["etc/sequential_dungeon_info.etc"] = seekerLukeNextSequenceText(ids, true)
			case "foreign_record":
				f.repos.DungeonPermission = &lukeForeignRecord{f.repos.DungeonPermission}
				f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return f.repos, true }
			}
			handled, err := currentGameplayModules.dispatchUpperRequest(f.service, f.session, req)
			if !handled || err != nil || f.conn.write.Len() != 0 {
				t.Fatalf("handled=%v error=%v sent=%x", handled, err, f.conn.write.Bytes())
			}
		})
	}
}

type lukeForeignRecord struct {
	dnfrepo.DungeonPermissionRepository
}

func (r *lukeForeignRecord) Load(context.Context, string) (dnfrepo.DungeonPermissionRecord, bool, error) {
	return dnfrepo.DungeonPermissionRecord{CharacterID: "other"}, true, nil
}

func TestLukeSelectorSettlementSendsFrozenProgressOnce(t *testing.T) {
	s, rt, session, conn, _ := prepareCompletedSettlementRuntime(t)
	plan := currentDungeonSettlementPacketPlanForTest(t, rt, 73)
	plan.LukeSelectorProgressBody = []byte{42, 0, 0, 0, 5, 31, 0, 0, 0}
	rt.settlementResultPlan = &plan
	request := make([]byte, currentDungeonPlayResultBaseSize)
	request[currentDungeonPlayResultClientRankPointOffset] = 73
	conn.write.Reset()
	if err := s.handleDungeonSetPlayResult(session, request); err != nil {
		t.Fatal(err)
	}
	stream := conn.write.Bytes()
	found := 0
	for len(stream) > 0 {
		p, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if p.Header.MsgID == 602 {
			found++
			if !bytes.Equal(p.Body, plan.LukeSelectorProgressBody) {
				t.Fatalf("progress=%x", p.Body)
			}
		}
	}
	if found != 1 {
		t.Fatalf("progress count=%d", found)
	}
	n := conn.write.Len()
	if err := s.handleDungeonSetPlayResult(session, request); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != n {
		t.Fatal("duplicate settlement resent progress")
	}
}

func TestLukeSelectorQueryRefreshesNewClearAndEmptyRecord(t *testing.T) {
	f := newSeekerLukeNextFixture(t, seekerLukeNextSource([]uint32{5101, 5106}, []uint32{5101, 5106}, true))
	body, err := f.service.currentLukeSelectorProgress(context.Background(), f.session)
	if err != nil || binary.LittleEndian.Uint32(body[5:]) != 0 {
		t.Fatalf("empty=%x err=%v", body, err)
	}
	if _, _, err := f.repos.DungeonPermission.UpsertMax(context.Background(), "99", 5101, 3); err != nil {
		t.Fatal(err)
	}
	body, err = f.service.currentLukeSelectorProgress(context.Background(), f.session)
	if err != nil || binary.LittleEndian.Uint32(body[5:]) != 1 {
		t.Fatalf("updated=%x err=%v", body, err)
	}
}

func TestLukeSelectorProgressRetryAfterFailedNotification(t *testing.T) {
	s, rt, session, _, _ := prepareCompletedSettlementRuntime(t)
	plan := currentDungeonSettlementPacketPlanForTest(t, rt, 73)
	plan.LukeSelectorProgressBody = []byte{42, 0, 0, 0, 5, 31, 0, 0, 0}
	rt.settlementResultPlan = &plan
	request := make([]byte, currentDungeonPlayResultBaseSize)
	request[currentDungeonPlayResultClientRankPointOffset] = 73
	failure := errors.New("progress send failed")
	conn := &failNthDungeonWriteConn{failAt: 4, err: failure}
	session.conn = conn
	if err := s.handleDungeonSetPlayResult(session, request); !errors.Is(err, failure) {
		t.Fatalf("failure=%v", err)
	}
	if rt.settlementLukeProgressSent {
		t.Fatal("failed packet marked sent")
	}
	if err := s.handleDungeonSetPlayResult(session, request); err != nil {
		t.Fatal(err)
	}
	if !rt.settlementLukeProgressSent {
		t.Fatal("retry did not send frozen progress")
	}
	stream := conn.write.Bytes()
	ids := []uint16{}
	for len(stream) > 0 {
		p, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		ids = append(ids, p.Header.MsgID)
	}
	if !reflect.DeepEqual(ids, []uint16{currentDungeonPlayResultNoticeMsgID, currentDungeonCharacterStateMsgID, currentDungeonClearRewardMsgID, 602}) {
		t.Fatalf("resent reward or lost progress: %v", ids)
	}
}

func TestLukeSelectorProgressRealPVF(t *testing.T) {
	path := os.Getenv("DNF_LUKE_SELECTOR_PVF")
	if path == "" {
		t.Skip("set DNF_LUKE_SELECTOR_PVF to the read-only current instance PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sequence, err := loadCurrentLukeNormalSequence(archive)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sequence.Dungeons, []uint32{5101, 5102, 5103, 5104, 5105, 5106}) {
		t.Fatalf("unexpected real Luke sequence=%v", sequence.Dungeons)
	}
	f := newSeekerLukeNextFixture(t, seekerLukeNextSource(sequence.Dungeons, sequence.Dungeons, true))
	f.service.dungeonRuleSource = archive
	ctx := context.Background()
	for i, id := range sequence.Dungeons {
		body, err := f.service.currentLukeSelectorProgress(ctx, f.session)
		if err != nil || binary.LittleEndian.Uint32(body[5:]) != (uint32(1)<<uint(i))-1 {
			t.Fatalf("step%d body=%x err=%v", i, body, err)
		}
		if _, _, err := f.repos.DungeonPermission.UpsertMax(ctx, "99", id, 3); err != nil {
			t.Fatal(err)
		}
	}
	body, err := f.service.currentLukeSelectorProgress(ctx, f.session)
	if err != nil || !bytes.Equal(body, []byte{42, 0, 0, 0, 5, 63, 0, 0, 0}) {
		t.Fatalf("all cleared=%x err=%v", body, err)
	}
}

func TestLukeSelectorProducerFreezesCommittedClearOnlyForQueriedOrdinaryLuke(t *testing.T) {
	for _, mode := range []string{"queried", "not_queried", "raid"} {
		t.Run(mode, func(t *testing.T) {
			s, rt, session, _, _ := prepareCompletedSettlementRuntime(t)
			repos, ok := s.repositoryGroup()
			if !ok {
				t.Fatal("repository missing")
			}
			character, found, err := repos.Character.Load(context.Background(), "99")
			if err != nil || !found {
				t.Fatal(err)
			}
			character.Level = 1
			character.Stats["exp"] = 0
			if err := repos.Character.Save(context.Background(), character); err != nil {
				t.Fatal(err)
			}
			if err := repos.Skill.Save(context.Background(), dnfrepo.SkillRecord{CharacterID: "99", Points: dnfrepo.SkillPointState{TotalSP: 10, RemainingSP: 10, SyncedLevel: 1}}); err != nil {
				t.Fatal(err)
			}
			rt.Character = character
			session.accountID = character.AccountID
			if mode != "not_queried" {
				session.lukeSelectorProgressCharacterID = session.selectedCharacterID
			}
			rt.Dungeon.ID = 5101
			rt.Dungeon.Metadata.BasisLevel.Set = true
			rt.Dungeon.Metadata.BasisLevel.Value = 1
			rt.Dungeon.Metadata.ExperienceIncreasingPoint.Set = true
			rt.Dungeon.Metadata.ExperienceIncreasingPoint.Value = 1
			rt.Dungeon.Path = "dungeon/luke_normal/test.dgn"
			rt.Dungeon.Metadata.DifficultyLevels = []int64{1, 1, 1, 1, 1}
			rt.Request.Difficulty = 0
			if mode == "raid" {
				rt.raidEntry = &currentRaidDungeonEntry{singleAntonMode: true}
			}
			s.dungeonRuleSource = bridgePVFSource{"etc/sequential_dungeon_info.etc": seekerLukeNextSequenceText([]uint32{5101, 5102, 5103, 5104, 5105, 5106}, true)}
			rt.settlementPlayResultReceived = true
			rt.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
			rt.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset] = 73
			resources := currentDungeonSettlementTestResources(t)
			for attempt := 0; attempt < 2; attempt++ {
				rt.settlementResultPlan = nil
				if err := s.produceCurrentDungeonSettlementPlanWithResourcesLocked(context.Background(), session, rt, resources); err != nil {
					t.Fatal(err)
				}
				body := rt.settlementResultPlan.LukeSelectorProgressBody
				if mode == "queried" {
					if !bytes.Equal(body, []byte{42, 0, 0, 0, 1, 1, 0, 0, 0}) {
						t.Fatalf("attempt %d progress=%x", attempt, body)
					}
				} else if len(body) != 0 {
					t.Fatalf("%s emitted progress=%x", mode, body)
				}
			}
		})
	}
}

// Pin the reported sequence transition, including an empty history and the
// saturated final dungeon. Tooltip masks and enterable cursors are independent.
func TestLukeSelectorUnlockCursorAdvancesToFirstUnclearedDungeon(t *testing.T) {
	sequence := currentLukeNormalSequence{Dungeons: []uint32{5101, 5102, 5103, 5104, 5105, 5106}}
	for _, tc := range []struct {
		clears int
		cursor byte
		mask   uint32
	}{{0, 0, 0}, {1, 1, 1}, {2, 2, 3}, {3, 3, 7}, {4, 4, 15}, {5, 5, 31}, {6, 5, 63}} {
		record := dnfrepo.DungeonPermissionRecord{}
		for _, id := range sequence.Dungeons[:tc.clears] {
			record.Entries = append(record.Entries, dnfrepo.DungeonPermissionEntry{DungeonID: id, ClearState: 3})
		}
		body := buildCurrentLukeSelectorProgress(sequence, record)
		if body[4] != tc.cursor || binary.LittleEndian.Uint32(body[5:]) != tc.mask {
			t.Fatalf("cleared=%d body=%x want cursor=%d mask=%d", tc.clears, body, tc.cursor, tc.mask)
		}
	}
}

func TestLukeSelectorSparseClearsDoNotUnlockPastMissingPredecessor(t *testing.T) {
	sequence := currentLukeNormalSequence{Dungeons: []uint32{5101, 5102, 5103, 5104, 5105, 5106}}
	record := dnfrepo.DungeonPermissionRecord{Entries: []dnfrepo.DungeonPermissionEntry{{DungeonID: 5101, ClearState: 3}, {DungeonID: 5103, ClearState: 3}, {DungeonID: 5106, ClearState: 1}, {DungeonID: 5102, ClearState: 0}}}
	body := buildCurrentLukeSelectorProgress(sequence, record)
	if body[4] != 1 || binary.LittleEndian.Uint32(body[5:]) != 37 {
		t.Fatalf("sparse clears must keep second dungeon as next entry: %x", body)
	}
}
