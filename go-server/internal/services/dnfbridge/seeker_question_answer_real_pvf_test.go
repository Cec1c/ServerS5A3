package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"testing"
)

func TestRealPVFQuestionAnswerBranches(t *testing.T) {
	ctx := context.Background()
	archive, catalog := seekerQueenLetterRealPVF(t)
	tables, e := progression.Load(ctx, archive)
	if e != nil {
		t.Fatal(e)
	}
	items, e := newPVFDungeonDropCatalog(archive)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		question uint16
		answer   byte
		branch   int64
		job      int
	}{{3868, 0, 3870, 2}, {3868, 1, 3871, 2}, {3868, 2, 3873, 2}, {3869, 0, 3872, 11}, {3869, 1, 3874, 11}} {
		t.Run(fmt.Sprintf("%d-%d", tc.question, tc.answer), func(t *testing.T) {
			repos := qst006Repositories(t, ctx)
			char, _, _ := repos.Character.Load(ctx, "19")
			char.Level = 87
			char.Job = strconv.Itoa(tc.job)
			if e := repos.Character.Save(ctx, char); e != nil {
				t.Fatal(e)
			}
			skill, _, _ := repos.Skill.Load(ctx, "19")
			skill.Points.SyncedLevel = 87
			if e := repos.Skill.Save(ctx, skill); e != nil {
				t.Fatal(e)
			}
			if e := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3867: {Status: "completed"}}}); e != nil {
				t.Fatal(e)
			}
			conn, s, session := qst006Bridge(repos, catalog, tables, items)
			session.townSceneReadyCharacterID = 19
			accept := []byte{31, 0, 0, 0}
			binary.LittleEndian.PutUint16(accept[2:], tc.question)
			if e := s.handleCurrentAcceptQuest(session, accept); e != nil {
				t.Fatal(e)
			}
			record, _, _ := repos.Quest.Load(ctx, "19")
			if record.States[int64(tc.question)].Status != "active" {
				t.Fatalf("not accepted: %+v", record)
			}
			body := []byte{33, 0, 0, 0, 0, tc.answer}
			binary.LittleEndian.PutUint16(body[2:], tc.question)
			if e := s.handleCurrentSetQuestTrigger(session, body); e != nil {
				t.Fatal(e)
			}
			qst006AssertFinishSuccess(t, qst006CallFinish(t, s, session, conn, tc.question), tc.question)
			record, _, _ = repos.Quest.Load(ctx, "19")
			if state := record.States[int64(tc.question)]; state.Status != "completed" || state.Extra["question_answer_id"] != strconv.Itoa(int(tc.answer)) {
				t.Fatalf("stored=%+v", state)
			}
			eligibility := dnfquest.CharacterEligibility{Level: 87, Job: tc.job}
			got := []int32{}
			for _, id := range catalog.QuestList(eligibility, record).IDs {
				if id >= 3870 && id <= 3874 {
					got = append(got, id)
				}
			}
			if len(got) != 1 || int64(got[0]) != tc.branch {
				t.Fatalf("branch list=%v want %d", got, tc.branch)
			}
			binary.LittleEndian.PutUint16(accept[2:], uint16(tc.branch))
			if e := s.handleCurrentAcceptQuest(session, accept); e != nil {
				t.Fatal(e)
			}
			record, _, _ = repos.Quest.Load(ctx, "19")
			if record.States[tc.branch].Status != "active" {
				t.Fatalf("successor not accepted: %+v", record.States[tc.branch])
			}
			t.Logf("real quest=%d job=%d answer=%d completed, branch=%d accepted", tc.question, tc.job, tc.answer, tc.branch)
		})
	}
}
