//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func runRaidFailureRefundSQL(t *testing.T, ctx context.Context, db *sql.DB, schema string, g repo.Group) {
	t.Helper()
	at := time.Unix(1800000000, 0).UTC()
	for mode := byte(1); mode <= 3; mode++ {
		id := fmt.Sprint(940 + int(mode))
		d := raid.EntryDebit{InstanceID: "00112233445566778899aabbccddeeff", Mode: mode, ItemID: 100, Count: 2, RefundItemID: 100, RefundCount: 2, PaidAt: at}
		c := repo.CharacterRecord{CharacterID: id, AccountID: "refund-account", Slot: int(mode), Name: fmt.Sprintf("refund%d", mode), Level: 1, Stats: map[string]int64{"keep": 7}}
		if err := raid.RecordEntryDebit(&c, d); err != nil {
			t.Fatal(err)
		}
		if err := g.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		loadedOwner, found, err := g.Character.Load(ctx, id)
		if err != nil || !found || loadedOwner.CharacterID != id || loadedOwner.AccountID != c.AccountID {
			t.Fatalf("fixture owner roundtrip: %+v %t %v", loadedOwner, found, err)
		}
		cmd := raid.EntryRefundCommand{Debit: d, CharacterID: id, AccountID: c.AccountID, FailedAt: at.Add(time.Hour), Attachments: []repo.MailAttachment{{ItemID: 100, Count: 2, RawEntry: []byte{1, 2}, Extra: map[string]string{"keep": "yes"}}}}
		if mode == 1 {
			// Refuse the receipt write after the mailbox writer has run. Both
			// must disappear on rollback, then the original command can retry.
			trigger := "CREATE TRIGGER `" + schema + "`.refund_receipt_fail BEFORE INSERT ON `" + schema + "`.`dnf_character_stats` FOR EACH ROW BEGIN IF NEW.stat_key LIKE 'raid_entry_v1_%_mail' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='refund receipt failure'; END IF; END"
			if _, err := db.ExecContext(ctx, trigger); err != nil {
				t.Fatal(err)
			}
			_, err := raid.RefundEntryByMail(ctx, g, cmd)
			if _, dropErr := db.ExecContext(ctx, "DROP TRIGGER `"+schema+"`.refund_receipt_fail"); dropErr != nil {
				t.Fatal(dropErr)
			}
			if err == nil {
				t.Fatal("SQL failure ignored")
			}
			mail, _, err := g.Mailbox.Load(ctx, id)
			if err != nil || len(mail.Mails) != 0 {
				t.Fatal("mail survived failed receipt", err, mail)
			}
			loaded, _, _ := g.Character.Load(ctx, id)
			status, err := raid.QueryEntryRefund(loaded, d)
			if err != nil || status.Refunded {
				t.Fatal("receipt survived rollback", status, err)
			}
		}
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				status, err := raid.RefundEntryByMail(ctx, g, cmd)
				if err != nil || !status.Refunded {
					t.Errorf("SQL concurrent refund: %+v %v", status, err)
				}
			}()
		}
		wg.Wait()
		mail, _, err := g.Mailbox.Load(ctx, id)
		if err != nil || len(mail.Mails) != 1 {
			t.Fatal("SQL duplicate mail", err, mail)
		}
		loaded, _, err := g.Character.Load(ctx, id)
		status, statusErr := raid.QueryEntryRefund(loaded, d)
		if err != nil || statusErr != nil || !status.Refunded || loaded.Stats["keep"] != 7 {
			t.Fatal("SQL query/readback", status, err, statusErr)
		}
	}
}
