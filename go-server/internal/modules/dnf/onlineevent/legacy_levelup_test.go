package onlineevent

import (
	"context"
	"errors"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestLegacyLevelReservationDeliveryRollbackAndReplay(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	save := func(id string, level int) {
		t.Helper()
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: "a", Level: level}); err != nil {
			t.Fatal(err)
		}
	}
	save("1", 15)
	save("2", 15)
	catalog := &LegacyLevelCatalog{
		Main:  []LegacyLevelReward{{35, AttendancePVFItemReward{ItemID: 100, Count: 1}}, {45, AttendancePVFItemReward{ItemID: 101, Count: 1}}},
		Stage: []LegacyLevelReward{{10, AttendancePVFItemReward{ItemID: 10, Count: 1}}, {20, AttendancePVFItemReward{ItemID: 20, Count: 1}}, {30, AttendancePVFItemReward{ItemID: 30, Count: 1}}, {40, AttendancePVFItemReward{ItemID: 40, Count: 1}}},
	}
	cmd := LegacyLevelCommand{AccountID: "a", CharacterID: "1", Now: time.Now(), Target: 35}
	fail := false
	cmd.Deliver = func(mailboxes dnfrepo.MailboxRepository, reward AttendancePVFItemReward, key string) (string, error) {
		if fail && reward.ItemID == 30 {
			return "", errors.New("mail failure")
		}
		return dnfrepo.AppendSystemMail(ctx, mailboxes, dnfrepo.SystemMailDelivery{RecipientCharacterID: cmd.CharacterID, Source: "test", CompletionKey: key, Attachments: []dnfrepo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count}}})
	}
	state, err := SettleLegacyLevel(ctx, repos, catalog, cmd)
	if err != nil || state.Target != 35 || state.Start != 15 || state.Stages != 0 {
		t.Fatalf("reserve=%+v err=%v", state, err)
	}
	cmd.Target = 45
	if _, err := SettleLegacyLevel(ctx, repos, catalog, cmd); err == nil {
		t.Fatal("reservation changed")
	}
	cmd.Target, cmd.Claim = 0, true
	if _, err := SettleLegacyLevel(ctx, repos, catalog, cmd); err == nil {
		t.Fatal("early claim accepted")
	}
	save("1", 45)
	fail = true
	if _, err := SettleLegacyLevel(ctx, repos, catalog, cmd); err == nil {
		t.Fatal("failed mail committed")
	}
	mailbox, _, _ := repos.Mailbox.Load(ctx, "1")
	if len(mailbox.Mails) != 0 {
		t.Fatal("rollback leaked mail")
	}
	fail = false
	state, err = SettleLegacyLevel(ctx, repos, catalog, cmd)
	if err != nil || !state.Claimed || state.Stages != 6 || len(state.MailIDs) != 3 {
		t.Fatalf("claim=%+v err=%v", state, err)
	}
	state, err = SettleLegacyLevel(ctx, repos, catalog, cmd)
	if err != nil || len(state.MailIDs) != 0 {
		t.Fatalf("replay=%+v err=%v", state, err)
	}
	cmd.CharacterID, cmd.Claim = "2", false
	state, err = SettleLegacyLevel(ctx, repos, catalog, cmd)
	if err != nil || state.Target != 0 || state.Claimed {
		t.Fatalf("character isolation=%+v err=%v", state, err)
	}
	cmd.AccountID = "foreign"
	if _, err := SettleLegacyLevel(ctx, repos, catalog, cmd); err == nil {
		t.Fatal("foreign account accepted")
	}
	cmd.AccountID = "a"
	account, _, _ := repos.Account.Load(ctx, "a")
	account.Metadata[LegacyLevelMetadataKey] = `{"2":{"target":0,"claimed":true}}`
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	if _, err := SettleLegacyLevel(ctx, repos, catalog, cmd); err == nil {
		t.Fatal("corrupt reservation accepted")
	}
}
