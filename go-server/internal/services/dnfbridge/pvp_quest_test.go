package dnfbridge

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func buildPVPResultQuestCatalog(t *testing.T) *dnfquest.Catalog {
	t.Helper()
	source := questListTestSource{
		dnfquest.DefaultList:           "6688 `pvp/daily_pvp_02.qst`\n6689 `pvp/daily_pvp_03.qst`\n",
		"n_quest/pvp/daily_pvp_02.qst": "[type] `[pvp quest]`\n[sub type] 1\n[int data] 3 0\n[/int data]\n",
		"n_quest/pvp/daily_pvp_03.qst": "[type] `[pvp quest]`\n[sub type] 1\n[int data] 5 0\n[/int data]\n",
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestRealScriptPVFPVPWinQuestCounts(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify PVP win quest definitions")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	source := questListTestSource{dnfquest.DefaultList: "6688 `pvp/daily_pvp_02.qst`\n6689 `pvp/daily_pvp_03.qst`\n"}
	for _, path := range []string{"n_quest/pvp/daily_pvp_02.qst", "n_quest/pvp/daily_pvp_03.qst"} {
		source[path], err = archive.ReadText(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	for id, count := range map[int64]uint32{6688: 3, 6689: 5} {
		plan, err := catalog.PlanAccept(dnfquest.CharacterEligibility{Level: 90, Job: 0, GrowType: 1}, dnfrepo.QuestRecord{}, id)
		if err != nil || plan.InitTrigger != count {
			t.Fatalf("runtime PVF quest=%d trigger=%d want=%d err=%v", id, plan.InitTrigger, count, err)
		}
	}
}

func TestPVPQuestSettlementModesAndRetry(t *testing.T) {
	for _, mode := range []string{"matching", "manual", "practice", "team", "draw", "retry", "write_retry"} {
		t.Run(mode, func(t *testing.T) {
			s := pvpRoomTestService(t)
			s.questCatalog = buildPVPResultQuestCatalog(t)
			queue := newFakeCurrentDungeonDeathTimerQueue()
			s.gameplayTimers = queue
			repos, _ := s.repositoryGroup()
			room := &currentPVPRoom{id: 1, channelID: 501, battleMode: 2, matchingToken: 1, started: true,
				match: &currentPVPMatch{winner: 0xff, startedAt: s.gameplayNow()}, seatState: [8]byte{1, 2, 3}}
			s.pvpRooms = map[uint16]*currentPVPRoom{1: room}
			for seat := 0; seat < 3; seat++ {
				id := 29 + seat
				player := &gameSession{conn: &bufferConn{}, accountID: "pvp-" + strconv.Itoa(id),
					selectedCharacterID: uint16(id), channel: s.catalog.Channels()[1], pvpRoom: room}
				player.channel.Type = 8
				room.members[seat] = &currentPVPMember{session: player, characterID: uint16(id)}
				if err := repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: strconv.Itoa(id),
					States: map[int64]dnfrepo.QuestState{6688: {Status: "active", ProgressValue: 3}}}); err != nil {
					t.Fatal(err)
				}
			}
			winner := byte(0)
			switch mode {
			case "manual":
				room.matchingToken = 0
			case "practice":
				room.members[0].session.channel.Type = 13

			case "team":
				room.seatState[2] = 1
			case "draw":
				winner = 0xff
			case "retry":
				s.questCatalog = nil
				s.questCatalogLoadErr = errors.New("injected unavailable PVF")
			case "write_retry":
				room.members[0].session.conn = &pvpWriteFailureConn{remainingFailures: 1, writeErr: errors.New("injected packet failure")}
			}
			if err := s.sendCurrentPVPResultLocked(room, room.members[0].session, winner); err != nil {
				t.Fatal(err)
			}
			if mode == "retry" || mode == "write_retry" {
				if room.match.resultSent || !room.match.settlementPending || queue.scheduleCount != 1 {
					t.Fatal("failed settlement marked result sent or lost retry")
				}
				s.questCatalog = buildPVPResultQuestCatalog(t)
				s.questCatalogLoadErr = nil
				queue.tasks[0].callback(time.Now())
			}
			if !room.match.resultSent {
				t.Fatal("result did not publish")
			}
			if err := s.sendCurrentPVPResultLocked(room, room.members[0].session, winner); err != nil {
				t.Fatal(err)
			}
			for seat, member := range room.members[:3] {
				quest, _, err := repos.Quest.Load(context.Background(), strconv.Itoa(int(member.characterID)))
				want := int64(3)
				if seat == 0 && (mode == "matching" || mode == "retry" || mode == "write_retry") {
					want = 2
				}
				if err != nil || quest.States[6688].ProgressValue != want || len(quest.States) != 1 {
					t.Fatalf("seat=%d quest=%+v err=%v", seat, quest, err)
				}
				character, _, err := repos.Character.Load(context.Background(), strconv.Itoa(int(member.characterID)))
				certification := int64(0)
				if seat < 2 && (mode == "matching" || mode == "retry" || mode == "write_retry" || mode == "draw") {
					certification = 1
				}
				if err != nil || character.Stats["pvp_certification_count"] != certification {
					t.Fatalf("mode=%s seat=%d certification=%d want=%d err=%v", mode, seat, character.Stats["pvp_certification_count"], certification, err)
				}
				var wire []byte
				switch conn := member.session.conn.(type) {
				case *bufferConn:
					wire = conn.write.Bytes()
				case *pvpWriteFailureConn:
					wire = conn.write.Bytes()
				}
				results, snapshots := 0, 0
				for len(wire) > 0 {
					packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
					wire = rest
					if packet.Header.MsgID == 47 {
						results++
					}
					if packet.Header.MsgID == 574 {
						snapshots++
					}
				}
				if results != 1 || (want == 2 && snapshots != 1) || (want == 3 && snapshots != 0) {
					t.Fatalf("seat=%d results=%d snapshots=%d", seat, results, snapshots)
				}
			}
		})
	}
}
