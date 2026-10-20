package onlineevent

import (
	"context"
	"errors"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestMonthlyAttendanceDeliveryRetryAccountSharingAndBoundary(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	account := dnfrepo.AccountRecord{AccountID: "account"}
	catalog := &MonthlyCatalog{Thresholds: [3]uint32{7, 14, 21}}
	for i := range catalog.Daily {
		catalog.Daily[i] = AttendancePVFItemReward{ItemID: int64(100 + i), Count: 1}
	}
	for i := range catalog.Bonus {
		catalog.Bonus[i] = AttendancePVFItemReward{ItemID: int64(200 + i), Count: 1}
	}
	for _, id := range []string{"1", "2"} {
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: "account"}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 9, 1, 6, 0, 0, 0, ChinaCalendar)
	for day := 0; day < 7; day++ {
		for clear := 0; clear < 5; clear++ {
			if err := RecordMonthlyClear(&account, now.AddDate(0, 0, day)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	cmd := MonthlyCommand{AccountID: "account", CharacterID: "1", Now: now.AddDate(0, 0, 6), ClaimBonus: true}
	cmd.Deliver = func(mailboxes dnfrepo.MailboxRepository, reward AttendancePVFItemReward, key string) (string, error) {
		if reward.ItemID == 101 {
			return "", errors.New("delivery failed")
		}
		return dnfrepo.AppendSystemMail(ctx, mailboxes, dnfrepo.SystemMailDelivery{RecipientCharacterID: cmd.CharacterID, Source: "test:" + key, Attachments: []dnfrepo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count}}})
	}
	if _, err := SettleMonthlyAttendance(ctx, repos, catalog, cmd); err == nil {
		t.Fatal("failed delivery committed")
	}
	mailbox, _, _ := repos.Mailbox.Load(ctx, "1")
	if len(mailbox.Mails) != 0 {
		t.Fatal("failed transaction leaked mail")
	}
	cmd.Deliver = func(mailboxes dnfrepo.MailboxRepository, reward AttendancePVFItemReward, key string) (string, error) {
		return dnfrepo.AppendSystemMail(ctx, mailboxes, dnfrepo.SystemMailDelivery{RecipientCharacterID: cmd.CharacterID, Source: "test:" + key, Attachments: []dnfrepo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count}}})
	}
	state, err := SettleMonthlyAttendance(ctx, repos, catalog, cmd)
	if err != nil || state.Days != 7 || state.Clears != 2 || state.Bonus != [3]uint32{2, 0, 0} || len(state.MailIDs) != 8 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	cmd.CharacterID = "2"
	state, err = SettleMonthlyAttendance(ctx, repos, catalog, cmd)
	if err != nil || len(state.MailIDs) != 0 || state.Days != 7 {
		t.Fatalf("alternate character replay=%+v err=%v", state, err)
	}
	cmd.AccountID = "foreign"
	if _, err := SettleMonthlyAttendance(ctx, repos, catalog, cmd); err == nil {
		t.Fatal("foreign account accepted")
	}
	cmd.AccountID = "account"
	cmd.ClaimBonus = false
	for day := 7; day < 30; day++ {
		cmd.Now = now.AddDate(0, 0, day)
		account, _, err = repos.Account.Load(ctx, "account")
		if err != nil {
			t.Fatal(err)
		}
		for clear := 1; clear <= 3; clear++ {
			if err := RecordMonthlyClear(&account, cmd.Now); err != nil {
				t.Fatal(err)
			}
			if err := repos.Account.Save(ctx, account); err != nil {
				t.Fatal(err)
			}
			state, err = SettleMonthlyAttendance(ctx, repos, catalog, cmd)
			wantDays := min(day, 28)
			if clear >= 2 {
				wantDays = min(day+1, 28)
			}
			wantMail := 0
			if day < 28 && clear == 2 {
				wantMail = 1
			}
			if err != nil || state.Days != uint32(wantDays) || len(state.MailIDs) != wantMail {
				t.Fatalf("day=%d clear=%d state=%+v err=%v", day, clear, state, err)
			}
			// Keep the mail receipt written by the settlement before the next clear.
			account, _, err = repos.Account.Load(ctx, "account")
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if state.Bonus != [3]uint32{2, 1, 1} {
		t.Fatalf("unclaimed accumulated rewards=%v", state.Bonus)
	}
	cmd.ClaimBonus = true
	state, err = SettleMonthlyAttendance(ctx, repos, catalog, cmd)
	if err != nil || state.Bonus != [3]uint32{2, 2, 2} || len(state.MailIDs) != 2 {
		t.Fatalf("remaining accumulated rewards=%+v err=%v", state, err)
	}
	state, err = SettleMonthlyAttendance(ctx, repos, catalog, cmd)
	if err != nil || len(state.MailIDs) != 0 {
		t.Fatalf("repeat claim=%+v err=%v", state, err)
	}
	cmd.Now = time.Date(2026, 10, 1, 5, 59, 59, 0, ChinaCalendar)
	state, err = SettleMonthlyAttendance(ctx, repos, catalog, cmd)
	if err != nil || state.Days != 28 {
		t.Fatalf("before monthly reset=%+v err=%v", state, err)
	}
	cmd.Now = cmd.Now.Add(time.Second)
	state, err = SettleMonthlyAttendance(ctx, repos, catalog, cmd)
	if err != nil || state.Days != 0 || state.Bonus != [3]uint32{} {
		t.Fatalf("after monthly reset=%+v err=%v", state, err)
	}
}
