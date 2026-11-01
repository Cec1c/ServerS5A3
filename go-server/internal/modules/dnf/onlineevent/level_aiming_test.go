package onlineevent

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestLevelAimingAllJobsReservationRollbackAndReplay(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 8, 0, 0, 0, time.UTC)
	c := &LevelAimingCatalog{Limit: 5, MinLevel: 1, MaxLevel: 86, Job: 99, Starts: now.Add(-time.Hour), Ends: now.Add(time.Hour), Main: AttendancePVFItemReward{ItemID: 100, Count: 1}, Points: map[uint32]int64{20: 5, 30: 10}, Stages: []LegacyLevelReward{{Level: 10, Item: AttendancePVFItemReward{ItemID: 101, Count: 1}}, {Level: 20, Item: AttendancePVFItemReward{ItemID: 102, Count: 1}}}}
	for job := 0; job < 16; job++ {
		repos := memory.NewMemoryGroup()
		if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "a"}); err != nil {
			t.Fatal(err)
		}
		save := func(id string, level int) {
			t.Helper()
			ch, _, _ := repos.Character.Load(ctx, id)
			ch.CharacterID = id
			ch.AccountID = "a"
			ch.Job = strconv.Itoa(job)
			ch.Level = level
			ch.CreatedAt = now
			if err := repos.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
		}
		save("1", 1)
		save("2", 1)
		cmd := LevelAimingCommand{AccountID: "a", CharacterID: "1", Now: now, Register: true}
		fail := false
		cmd.Deliver = func(mailboxes dnfrepo.MailboxRepository, r AttendancePVFItemReward, key string) (string, error) {
			if fail && r.ItemID == 102 {
				return "", errors.New("mail failure")
			}
			return dnfrepo.AppendSystemMail(ctx, mailboxes, dnfrepo.SystemMailDelivery{RecipientCharacterID: cmd.CharacterID, Source: "test", CompletionKey: key, Attachments: []dnfrepo.MailAttachment{{ItemID: r.ItemID, Count: r.Count}}})
		}
		result, err := SettleLevelAiming(ctx, repos, c, cmd)
		if err != nil || result.Target != 0 || result.Character.Stats[LevelAimingRoleStat] != 5 {
			t.Fatalf("job=%d register=%+v err=%v", job, result, err)
		}
		cmd.CharacterID = "2"
		if _, err = SettleLevelAiming(ctx, repos, c, cmd); err == nil {
			t.Fatal("second event character accepted")
		}
		cmd.CharacterID = "1"
		cmd.Register = false
		cmd.Target = 20
		result, err = SettleLevelAiming(ctx, repos, c, cmd)
		if err != nil || result.Reservations != 1 || result.Character.Stats[LevelAimingRoleStat] != 3 {
			t.Fatalf("reserve=%+v err=%v", result, err)
		}
		cmd.Target = 30
		if _, err = SettleLevelAiming(ctx, repos, c, cmd); err == nil {
			t.Fatal("unfinished reservation changed")
		}
		save("1", 20)
		cmd.Target = 0
		fail = true
		if _, err = SettleLevelAiming(ctx, repos, c, cmd); err == nil {
			t.Fatal("failed delivery committed")
		}
		box, _, _ := repos.Mailbox.Load(ctx, "1")
		if len(box.Mails) != 0 {
			t.Fatal("rollback leaked mail")
		}
		fail = false
		result, err = SettleLevelAiming(ctx, repos, c, cmd)
		if err != nil || !result.Awarded || len(result.MailIDs) != 3 {
			t.Fatalf("settle=%+v err=%v", result, err)
		}
		result, err = SettleLevelAiming(ctx, repos, c, cmd)
		if err != nil || len(result.MailIDs) != 0 {
			t.Fatalf("replay=%+v err=%v", result, err)
		}
		cmd.Target = 30
		result, err = SettleLevelAiming(ctx, repos, c, cmd)
		if err != nil || result.Start != 21 || result.Reservations != 2 {
			t.Fatalf("next=%+v err=%v", result, err)
		}
		save("1", 30)
		cmd.Target = 0
		result, err = SettleLevelAiming(ctx, repos, c, cmd)
		if err != nil || len(result.MailIDs) != 1 {
			t.Fatalf("next reward=%+v err=%v", result, err)
		}
		box, _, _ = repos.Mailbox.Load(ctx, "1")
		var coins int64
		for _, mail := range box.Mails {
			for _, a := range mail.Attachments {
				if a.ItemID == 100 {
					coins += a.Count
				}
			}
		}
		if coins != 15 {
			t.Fatalf("cumulative coins=%d", coins)
		}
		cmd.AccountID = "foreign"
		if _, err = SettleLevelAiming(ctx, repos, c, cmd); err == nil {
			t.Fatal("foreign account accepted")
		}
		cmd.AccountID = "a"
		cmd.Target = 85
		if _, err = SettleLevelAiming(ctx, repos, c, cmd); err != nil {
			t.Fatal(err)
		}
		save("1", 85)
		cmd.Target = 86
		if _, err = SettleLevelAiming(ctx, repos, c, cmd); err != nil {
			t.Fatal(err)
		}
		cmd.Target = 0
		if result, err = SettleLevelAiming(ctx, repos, c, cmd); err != nil || result.Start != 86 || result.Target != 86 {
			t.Fatalf("85 to 86 interval=%+v err=%v", result, err)
		}
		cmd.Target = 40
		cmd.Now = c.Ends
		if _, err = SettleLevelAiming(ctx, repos, c, cmd); err == nil {
			t.Fatal("expired event accepted")
		}
	}
}
