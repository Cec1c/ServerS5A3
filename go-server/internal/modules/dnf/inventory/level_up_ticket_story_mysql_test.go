package inventory

import (
	"context"
	"reflect"
	"testing"
	"time"

	pvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestLevelUpTicketStoryMySQLAtomicReloadAndResume(t *testing.T) {
	db, name, newGroup := itemTransactionMySQLFixture(t)
	seed, tables, _, req, now := levelTicketFixture(t)
	ctx := context.Background()
	g := newGroup()
	ch, _, _ := seed.Character.Load(ctx, "2")
	bag, _, _ := seed.Inventory.Load(ctx, "2")
	sk, _, _ := seed.Skill.Load(ctx, "2")
	q, _, _ := seed.Quest.Load(ctx, "2")
	for _, err := range []error{g.Account.Save(ctx, repo.AccountRecord{AccountID: "test"}), g.Character.Save(ctx, ch), g.Inventory.Save(ctx, bag), g.Skill.Save(ctx, sk), g.Quest.Save(ctx, q), g.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "2"})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	base := "[grade] `[epic]`\n[level] 50 99\n[job] `[all]`\n[exposed by npc] 1\n[difficulty] `N`\n[attribute] `not give exp quest`\n"
	source := levelTicketSource{quest.DefaultList: "3868 `question.qst`\n3873 `branch.qst`", "n_quest/question.qst": base + "[type] `[question]`\n[question info] `Prompt` `Prompt` `A` `B` `C`\n", "n_quest/branch.qst": base + "[type] `[meet npc]`\n[pre required quest answer] 3868 2\n"}
	ix, err := pvf.Build(ctx, source, pvf.BuildOptions{Lists: []string{quest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := quest.Load(ctx, ix)
	if err != nil {
		t.Fatal(err)
	}
	before := levelTicketSnapshot(t, g)
	checks := 0
	req.Revalidate = func() bool { checks++; return checks < 2 }
	if _, err = ApplyLevelUpTicket(ctx, g, tables, catalog, req, now); err == nil {
		t.Fatal("late failure accepted")
	}
	if !reflect.DeepEqual(before, levelTicketSnapshot(t, newGroup())) {
		t.Fatal("SQL ticket rollback left authorization/assets")
	}
	req.Revalidate = func() bool { return true }
	r, err := ApplyLevelUpTicket(ctx, g, tables, catalog, req, now)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := newGroup()
	ch, _, err = reloaded.Character.Load(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	auth, ok, err := quest.ReadTicketStoryAuthorization(ch)
	if err != nil || !ok || auth.Generation != 1 || auth.Cutoff != 50 {
		t.Fatal(auth, ok, err)
	}
	owner, _ := quest.NewOwner(reloaded)
	cmd := quest.Command{AccountID: "test", SelectedCharacterID: 2, QuestID: 3868}
	if _, err = owner.ApplyAccept(ctx, catalog, quest.CharacterEligibility{Level: 51, Job: 2}, cmd); err != nil {
		t.Fatal(err)
	}
	cmd.HasAnswer = true
	cmd.Answer = 2
	if _, err = owner.ApplyQuestionAnswer(ctx, catalog, cmd); err != nil {
		t.Fatal(err)
	}
	// A new repository group simulates reconnect after the answer persisted.
	owner, _ = quest.NewOwner(newGroup())
	in := quest.FinishCommitInput{AccountID: "test", CharacterID: "2", QuestID: 3868, Multiplier: 1, CommittedAt: now.Add(time.Minute), Progression: tables, MaximumCharacterLevel: 90}
	beforeFinish := levelTicketSnapshot(t, newGroup())
	if _, err = db.Exec("CREATE TRIGGER `" + name + "`.reject_story BEFORE INSERT ON `" + name + "`.dnf_quest_states FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test quest write failure'"); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.ApplyFinishSettlement(ctx, catalog, in); err == nil {
		t.Fatal("failed SQL finish committed")
	}
	if !reflect.DeepEqual(beforeFinish, levelTicketSnapshot(t, newGroup())) {
		t.Fatal("SQL failure partially committed answer finish or resume")
	}
	if _, err = db.Exec("DROP TRIGGER `" + name + "`.reject_story"); err != nil {
		t.Fatal(err)
	}
	first, err := owner.ApplyFinishSettlement(ctx, catalog, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := owner.ApplyFinishSettlement(ctx, catalog, in)
	if err != nil || !second.Replayed {
		t.Fatal(err)
	}
	final := newGroup()
	ch, _, _ = final.Character.Load(ctx, "2")
	q, _, _ = final.Quest.Load(ctx, "2")
	afterBag, _, _ := final.Inventory.Load(ctx, "2")
	auth, _, err = quest.ReadTicketStoryAuthorization(ch)
	if err != nil || auth.Generation != 1 || auth.Revision != 2 || q.States[3873].Status != "completed" || first.PostCommitQuest.States[3868].Extra["question_answer_id"] != "2" {
		t.Fatal("SQL continuation not durable", auth, err)
	}
	if afterBag.Slots["0:4"].Count != r.Remaining || ch.Stats["gold"] != 456 {
		t.Fatal("resume changed ticket/gold")
	}
	if len(first.Items) > 0 || len(first.Currency) > 0 {
		t.Fatal("unexpected question assets")
	}
}
