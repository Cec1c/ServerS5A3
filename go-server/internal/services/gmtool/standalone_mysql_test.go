package gmtool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
	"longheng.io/server/internal/modules/dnf/testbenefits"
)

func independentGMFixture(t *testing.T) (*Server, *Server, repository.Group, repository.Group) {
	t.Helper()
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	admin, err := sql.Open("mysql", dsn)
	mustGM(t, err)
	name := fmt.Sprintf("gm_split_test_%d", time.Now().UnixNano())
	_, err = admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4")
	mustGM(t, err)
	t.Cleanup(func() { admin.Exec("DROP DATABASE `" + name + "`"); admin.Close() })
	cfg, err := driver.ParseDSN(dsn)
	mustGM(t, err)
	cfg.DBName = name
	plan := repository.DatabasePlan{WriteDatabases: []string{name}, ReadDatabases: []string{name}}
	var servers []*Server
	var groups []repository.Group
	for i := 0; i < 2; i++ {
		db, err := sql.Open("mysql", cfg.FormatDSN())
		mustGM(t, err)
		t.Cleanup(func() { db.Close() })
		if i == 0 {
			mustGM(t, repomysql.EnsureMySQLSchema(context.Background(), db, repomysql.SchemaOptions{AutoCreate: true, DatabasePlan: plan}))
			_, err = db.Exec("CREATE TABLE web_accounts (id INT PRIMARY KEY, account_id VARCHAR(64) NOT NULL)")
			mustGM(t, err)
			_, err = db.Exec("INSERT INTO web_accounts VALUES (1,'fixture:1')")
			mustGM(t, err)
		}
		group, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
		mustGM(t, err)
		store, err := testbenefits.NewStore(db, "web_accounts")
		mustGM(t, err)
		mustGM(t, store.EnsureSchema(context.Background()))
		servers = append(servers, &Server{db: db, cfg: Config{Table: "web_accounts"}, deps: Deps{Benefits: store, Repos: func() (repository.Group, bool) { return group, true }}})
		groups = append(groups, group)
	}
	mustGM(t, groups[0].Account.Save(context.Background(), repository.AccountRecord{AccountID: "fixture:1"}))
	return servers[0], servers[1], groups[0], groups[1]
}

type lateMailAbort struct {
	repository.MailboxAssetUnitOfWork
	inner repository.AccountMailboxCharacterUnitOfWork
	err   error
}

func (u lateMailAbort) WithinAccountMailboxCharacter(ctx context.Context, accountID, characterID string, apply func(repository.AccountRepository, repository.CharacterRecord, repository.MailboxRepository) error) error {
	return u.inner.WithinAccountMailboxCharacter(ctx, accountID, characterID, func(a repository.AccountRepository, c repository.CharacterRecord, m repository.MailboxRepository) error {
		if err := apply(a, c, m); err != nil {
			return err
		}
		return u.err
	})
}

func TestStandaloneMailMySQLLateFailureRollsBackReceiptAndMail(t *testing.T) {
	_, _, g1, g2 := independentGMFixture(t)
	ctx := context.Background()
	character := repository.CharacterRecord{CharacterID: "1", AccountID: "fixture:1", Name: "fixture", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	mustGM(t, g1.Character.Save(ctx, character))
	delivery := repository.SystemMailDelivery{RecipientCharacterID: "1", Title: "fixture", Source: "gm-test", Attachments: []repository.MailAttachment{{ItemID: 123, Count: 1}}}
	abort := errors.New("fixture late abort")
	failed := g1
	failed.MailboxAssets = lateMailAbort{MailboxAssetUnitOfWork: g1.MailboxAssets, inner: g1.MailboxAssets.(repository.AccountMailboxCharacterUnitOfWork), err: abort}
	if _, _, err := sendItemMailOnce(ctx, failed, character, "rollback-request", "content", delivery); !errors.Is(err, abort) {
		t.Fatalf("late abort missing: %v", err)
	}
	mail, _, err := g2.Mailbox.Load(ctx, "1")
	mustGM(t, err)
	account, _, err := g2.Account.Load(ctx, character.AccountID)
	mustGM(t, err)
	if len(mail.Mails) != 0 || len(account.Metadata) != 0 {
		t.Fatalf("failed transaction leaked mail or receipt: mails=%d metadata=%v", len(mail.Mails), account.Metadata)
	}
	_, replayed, err := sendItemMailOnce(ctx, g2, character, "rollback-request", "content", delivery)
	mustGM(t, err)
	if replayed {
		t.Fatal("rolled back receipt prevented retry")
	}
	mail, _, err = g1.Mailbox.Load(ctx, "1")
	mustGM(t, err)
	if len(mail.Mails) != 1 {
		t.Fatalf("retry mails=%d", len(mail.Mails))
	}
}

func mustGM(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestStandaloneScannerMySQLIndependentPools(t *testing.T) {
	s1, s2, g1, g2 := independentGMFixture(t)
	ctx := context.Background()
	rule, err := s1.deps.Benefits.CreateCampaign(ctx, testbenefits.Job{Title: "fixture", Body: "fixture", Attachments: []repository.MailAttachment{{ItemID: 123, Count: 1}}}, "fixture", true)
	mustGM(t, err)
	rule, err = s1.deps.Benefits.EnableCampaign(ctx, rule.ID, rule.Revision, true)
	mustGM(t, err)
	for i, created := range []time.Time{rule.FirstEnabledAt.Add(-time.Hour), rule.FirstEnabledAt.Add(time.Second)} {
		mustGM(t, g1.Character.Save(ctx, repository.CharacterRecord{CharacterID: fmt.Sprint(i + 1), AccountID: "fixture:1", Name: "fixture", Slot: i, CreatedAt: created}))
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(s *Server) { defer wg.Done(); _, err := s.ScanNewCharacters(ctx, 0, 10); failures <- err }([]*Server{s1, s2}[i%2])
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		mustGM(t, err)
	}
	old, _, err := g2.Mailbox.Load(ctx, "1")
	mustGM(t, err)
	if len(old.Mails) != 0 {
		t.Fatal("old character automatically granted")
	}
	mail, _, err := g2.Mailbox.Load(ctx, "2")
	mustGM(t, err)
	if len(mail.Mails) != 1 {
		t.Fatalf("concurrent scanner mails=%d", len(mail.Mails))
	}
	mail.Mails = map[string]repository.MailRecord{}
	mustGM(t, repository.SaveMailboxFields(ctx, g2.Mailbox, mail, repository.MailboxFieldMails))
	// Reload rule store as a newly started GM, reset cursor, and retry after deletion.
	s2.deps.Benefits, err = testbenefits.NewStore(s2.db, "web_accounts")
	mustGM(t, err)
	_, err = s2.ScanNewCharacters(ctx, 0, 10)
	mustGM(t, err)
	mail, _, err = g1.Mailbox.Load(ctx, "2")
	mustGM(t, err)
	if len(mail.Mails) != 0 {
		t.Fatal("restart/deletion caused duplicate")
	}
	rule, err = s1.deps.Benefits.EnableCampaign(ctx, rule.ID, rule.Revision, false)
	mustGM(t, err)
	mustGM(t, g2.Character.Save(ctx, repository.CharacterRecord{CharacterID: "3", AccountID: "fixture:1", Name: "during pause", Slot: 2, CreatedAt: rule.FirstEnabledAt.Add(2 * time.Second)}))
	_, err = s2.ScanNewCharacters(ctx, 0, 10)
	mustGM(t, err)
	mail, _, err = g1.Mailbox.Load(ctx, "3")
	mustGM(t, err)
	if len(mail.Mails) != 0 {
		t.Fatal("paused rule granted")
	}
	_, err = s1.deps.Benefits.EnableCampaign(ctx, rule.ID, rule.Revision, true)
	mustGM(t, err)
	_, err = s2.ScanNewCharacters(ctx, 0, 10)
	mustGM(t, err)
	mail, _, err = g1.Mailbox.Load(ctx, "3")
	mustGM(t, err)
	if len(mail.Mails) != 1 {
		t.Fatal("resumed rule missed newcomer")
	}
	job, err := s1.deps.Benefits.PreviewCampaign(ctx, g1, rule.ID)
	mustGM(t, err)
	_, err = s1.deps.Benefits.Run(ctx, g1, job.ID, false)
	mustGM(t, err)
	old, _, err = g2.Mailbox.Load(ctx, "1")
	mustGM(t, err)
	if len(old.Mails) != 1 {
		t.Fatal("manual backfill missed old character")
	}
	mail, _, err = g2.Mailbox.Load(ctx, "2")
	mustGM(t, err)
	if len(mail.Mails) != 0 {
		t.Fatal("manual backfill redelivered deleted mail")
	}
}

func TestStandaloneMailMySQLDurableRequest(t *testing.T) {
	_, _, g1, g2 := independentGMFixture(t)
	ctx := context.Background()
	character := repository.CharacterRecord{CharacterID: "1", AccountID: "fixture:1", Name: "fixture", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	mustGM(t, g1.Character.Save(ctx, character))
	delivery := repository.SystemMailDelivery{RecipientCharacterID: "1", Title: "fixture", Source: "gm-test", Attachments: []repository.MailAttachment{{ItemID: 123, Count: 1}}}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(g repository.Group) {
			defer wg.Done()
			_, _, err := sendItemMailOnce(ctx, g, character, "request-id-1", "content-1", delivery)
			failures <- err
		}([]repository.Group{g1, g2}[i%2])
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		mustGM(t, err)
	}
	mail, _, err := g2.Mailbox.Load(ctx, "1")
	mustGM(t, err)
	if len(mail.Mails) != 1 {
		t.Fatal("duplicate mail", len(mail.Mails))
	}
	mail.Mails = map[string]repository.MailRecord{}
	mustGM(t, repository.SaveMailboxFields(ctx, g1.Mailbox, mail, repository.MailboxFieldMails))
	id, replayed, err := sendItemMailOnce(ctx, g2, character, "request-id-1", "content-1", delivery)
	mustGM(t, err)
	if !replayed || id == "" {
		t.Fatal("durable receipt missing")
	}
	if _, _, err = sendItemMailOnce(ctx, g1, character, "request-id-1", "different", delivery); err == nil {
		t.Fatal("changed content accepted")
	}
	mail, _, err = g2.Mailbox.Load(ctx, "1")
	mustGM(t, err)
	if len(mail.Mails) != 0 {
		t.Fatal("receipt did not survive deletion")
	}
}
