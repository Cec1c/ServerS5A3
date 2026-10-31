//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/progression"
	"longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"strings"
	"testing"
	"time"
)

func runProgressionRealDBCase(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	name string,
	g repository.Group,
) {
	t.Helper()
	if db == nil || name == "" || g.CharacterProgression == nil || g.Character == nil || g.Skill == nil {
		t.Fatal("progression real-DB case requires db, schema name, and character/progression repositories")
	}

	const (
		characterID = "progression-real-db-89"
		accountID   = "progression-real-db-account"
	)
	stats := make(map[string]int64, 70)
	stats["exp"] = 100
	for i := 0; i < 69; i++ {
		stats[fmt.Sprintf("progression_unrelated_%02d", i)] = int64(10_000 + i)
	}
	if len(stats) != 70 {
		t.Fatalf("synthetic character stats = %d, want 70", len(stats))
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	character := repository.CharacterRecord{
		CharacterID: characterID,
		AccountID:   accountID,
		Slot:        7,
		Name:        "Progression89",
		Job:         "synthetic-job",
		Level:       89,
		Stats:       stats,
		Location: repository.CharacterLocation{
			ChannelID: 7,
			TownID:    38,
			DungeonID: 1101,
			RoomID:    "progression-room",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	initialPoints := repository.SkillPointState{
		TotalSP:     100,
		RemainingSP: 75,
		TotalTP:     3,
		RemainingTP: 2,
		SyncedLevel: 89,
	}
	skill := repository.SkillRecord{
		CharacterID: characterID,
		Skills: map[int64]repository.SkillState{
			100: {Level: 5, Enabled: true},
		},
		Points: initialPoints,
		Layouts: map[int]repository.SkillLayout{
			0: {0: 100},
		},
		Cooldowns: map[int64]time.Time{
			100: now.Add(time.Minute),
		},
		UpdatedAt: now,
	}
	if err := g.Character.Save(ctx, character); err != nil {
		t.Fatalf("seed character: %v", err)
	}
	if err := g.Skill.Save(ctx, skill); err != nil {
		t.Fatalf("seed skill: %v", err)
	}
	seedCharacter, ok, err := g.Character.Load(ctx, characterID)
	if err != nil || !ok {
		t.Fatalf("load seeded character: ok=%t err=%v", ok, err)
	}
	seedSkill, ok, err := g.Skill.Load(ctx, characterID)
	if err != nil || !ok {
		t.Fatalf("load seeded skill: ok=%t err=%v", ok, err)
	}

	assertFullCharacter := func(got repository.CharacterRecord, wantLevel int, wantExperience int64) {
		t.Helper()
		if got.CharacterID != character.CharacterID || got.AccountID != character.AccountID || got.Slot != character.Slot {
			t.Fatalf("character identity changed: %+v", got)
		}
		if got.Name != character.Name || got.Job != character.Job || got.Location != character.Location {
			t.Fatalf("character presentation/location changed: name=%q job=%q location=%+v", got.Name, got.Job, got.Location)
		}
		if got.Level != wantLevel || got.Stats["exp"] != wantExperience {
			t.Fatalf("progression state level=%d exp=%d, want level=%d exp=%d", got.Level, got.Stats["exp"], wantLevel, wantExperience)
		}
		for key, want := range stats {
			if key == "exp" {
				want = wantExperience
			}
			if got.Stats[key] != want {
				t.Fatalf("character stat %q = %d, want %d", key, got.Stats[key], want)
			}
		}
	}
	assertSkillCollections := func(got repository.SkillRecord) {
		t.Helper()
		if !reflect.DeepEqual(got.Skills, seedSkill.Skills) ||
			!reflect.DeepEqual(got.Layouts, seedSkill.Layouts) ||
			!reflect.DeepEqual(got.Cooldowns, seedSkill.Cooldowns) ||
			!reflect.DeepEqual(got.Trees, seedSkill.Trees) {
			t.Fatalf("progression changed skill collections: %+v", got)
		}
	}
	assertFullCharacter(seedCharacter, 89, 100)

	ordinary := progression.CommitPlan{
		CharacterID: characterID,
		AccountID:   accountID,
		Expected: progression.CommitState{
			Level:       89,
			Experience:  100,
			SkillPoints: initialPoints,
		},
		Next: progression.CommitState{
			Level:       89,
			Experience:  140,
			SkillPoints: initialPoints,
		},
	}
	result, err := progression.Commit(ctx, g.CharacterProgression, ordinary)
	if err != nil {
		t.Fatalf("ordinary experience commit: %v", err)
	}
	if !result.Applied || result.Idempotent || result.Current != ordinary.Next {
		t.Fatalf("ordinary experience result = %+v", result)
	}
	committedCharacter, ok, err := g.Character.Load(ctx, characterID)
	if err != nil || !ok {
		t.Fatalf("load character after ordinary commit: ok=%t err=%v", ok, err)
	}
	assertFullCharacter(committedCharacter, 89, 140)
	committedSkill, ok, err := g.Skill.Load(ctx, characterID)
	if err != nil || !ok {
		t.Fatalf("load skill after ordinary commit: ok=%t err=%v", ok, err)
	}
	assertSkillCollections(committedSkill)
	if committedSkill.Points != initialPoints {
		t.Fatalf("ordinary experience changed SP/TP: %+v", committedSkill.Points)
	}

	replay, err := progression.Commit(ctx, g.CharacterProgression, ordinary)
	if err != nil {
		t.Fatalf("replay ordinary experience: %v", err)
	}
	if replay.Applied || !replay.Idempotent || replay.Current != ordinary.Next {
		t.Fatalf("replay result = %+v", replay)
	}
	replayedCharacter, ok, err := g.Character.Load(ctx, characterID)
	if err != nil || !ok {
		t.Fatalf("load character after replay: ok=%t err=%v", ok, err)
	}
	assertFullCharacter(replayedCharacter, 89, 140)

	assertRejected := func(label string, plan progression.CommitPlan, want error) {
		t.Helper()
		beforeCharacter, ok, err := g.Character.Load(ctx, characterID)
		if err != nil || !ok {
			t.Fatalf("%s: load pre-rejection character: ok=%t err=%v", label, ok, err)
		}
		beforeSkill, ok, err := g.Skill.Load(ctx, characterID)
		if err != nil || !ok {
			t.Fatalf("%s: load pre-rejection skill: ok=%t err=%v", label, ok, err)
		}
		if _, err := progression.Commit(ctx, g.CharacterProgression, plan); !errors.Is(err, want) {
			t.Fatalf("%s: Commit error = %v, want %v", label, err, want)
		}
		afterCharacter, ok, err := g.Character.Load(ctx, characterID)
		if err != nil || !ok {
			t.Fatalf("%s: load post-rejection character: ok=%t err=%v", label, ok, err)
		}
		afterSkill, ok, err := g.Skill.Load(ctx, characterID)
		if err != nil || !ok {
			t.Fatalf("%s: load post-rejection skill: ok=%t err=%v", label, ok, err)
		}
		if !reflect.DeepEqual(afterCharacter, beforeCharacter) || !reflect.DeepEqual(afterSkill, beforeSkill) {
			t.Fatalf("%s: rejected commit changed character or skill", label)
		}
	}

	nextOrdinary := progression.CommitPlan{
		CharacterID: characterID,
		AccountID:   accountID,
		Expected: progression.CommitState{
			Level:       89,
			Experience:  140,
			SkillPoints: initialPoints,
		},
		Next: progression.CommitState{
			Level:       89,
			Experience:  180,
			SkillPoints: initialPoints,
		},
	}
	wrongAccount := nextOrdinary
	wrongAccount.AccountID = "progression-real-db-other-account"
	assertRejected("cross-account", wrongAccount, progression.ErrCommitOwnershipMismatch)

	staleVersion := nextOrdinary
	staleVersion.Expected.Experience = 139
	assertRejected("stale-version", staleVersion, progression.ErrCommitVersionPrecondition)

	leveledPoints := repository.SkillPointState{
		TotalSP:     106,
		RemainingSP: 81,
		TotalTP:     4,
		RemainingTP: 3,
		SyncedLevel: 90,
	}
	levelUp := progression.CommitPlan{
		CharacterID: characterID,
		AccountID:   accountID,
		Expected: progression.CommitState{
			Level:       89,
			Experience:  140,
			SkillPoints: initialPoints,
		},
		Next: progression.CommitState{
			Level:       90,
			Experience:  180,
			SkillPoints: leveledPoints,
		},
	}

	beforeFailureCharacter, ok, err := g.Character.Load(ctx, characterID)
	if err != nil || !ok {
		t.Fatalf("load pre-failure character: ok=%t err=%v", ok, err)
	}
	beforeFailureSkill, ok, err := g.Skill.Load(ctx, characterID)
	if err != nil || !ok {
		t.Fatalf("load pre-failure skill: ok=%t err=%v", ok, err)
	}
	const triggerName = "fail_progression_skill"
	createTrigger := fmt.Sprintf(
		"CREATE TRIGGER `%s`.`%s` BEFORE UPDATE ON `%s`.`dnf_%s` FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'isolated progression skill failure'",
		name, triggerName, name, mysqlSkillTable,
	)
	dropTrigger := fmt.Sprintf("DROP TRIGGER IF EXISTS `%s`.`%s`", name, triggerName)
	installed := false
	defer func() {
		if !installed {
			return
		}
		if _, err := db.ExecContext(context.Background(), dropTrigger); err != nil {
			t.Errorf("drop progression skill failure trigger: %v", err)
		}
	}()
	if _, err := db.ExecContext(ctx, createTrigger); err != nil {
		t.Fatalf("install progression skill failure trigger: %v", err)
	}
	installed = true
	if _, err := progression.Commit(ctx, g.CharacterProgression, levelUp); err == nil || !strings.Contains(err.Error(), "isolated progression skill failure") {
		t.Fatalf("skill failure was not injected: %v", err)
	}
	if _, err := db.ExecContext(ctx, dropTrigger); err != nil {
		t.Fatalf("drop progression skill failure trigger: %v", err)
	}
	installed = false

	failedCharacter, ok, err := g.Character.Load(ctx, characterID)
	if err != nil || !ok {
		t.Fatalf("load character after failed skill write: ok=%t err=%v", ok, err)
	}
	failedSkill, ok, err := g.Skill.Load(ctx, characterID)
	if err != nil || !ok {
		t.Fatalf("load skill after failed skill write: ok=%t err=%v", ok, err)
	}
	if !reflect.DeepEqual(failedCharacter, beforeFailureCharacter) || !reflect.DeepEqual(failedSkill, beforeFailureSkill) {
		t.Fatal("failed skill write did not roll back character and skill")
	}
	assertFullCharacter(failedCharacter, 89, 140)
	if failedSkill.Points != initialPoints {
		t.Fatalf("failed skill write kept new SP/TP: %+v", failedSkill.Points)
	}

	result, err = progression.Commit(ctx, g.CharacterProgression, levelUp)
	if err != nil {
		t.Fatalf("level-up commit: %v", err)
	}
	if !result.Applied || result.Idempotent || result.Current != levelUp.Next {
		t.Fatalf("level-up result = %+v", result)
	}
	finalCharacter, ok, err := g.Character.Load(ctx, characterID)
	if err != nil || !ok {
		t.Fatalf("load final character: ok=%t err=%v", ok, err)
	}
	assertFullCharacter(finalCharacter, 90, 180)
	finalSkill, ok, err := g.Skill.Load(ctx, characterID)
	if err != nil || !ok {
		t.Fatalf("load final skill: ok=%t err=%v", ok, err)
	}
	if finalSkill.Points != leveledPoints {
		t.Fatalf("final SP/TP = %+v, want %+v", finalSkill.Points, leveledPoints)
	}
	assertSkillCollections(finalSkill)
}

func runAccountMetadataRealDBCase(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	name string,
	g repository.Group,
) {
	t.Helper()
	if ctx == nil || db == nil {
		t.Fatal("account metadata case requires context and database")
	}
	suffix := strings.TrimPrefix(name, "runtime_delta_")
	safeSchema := strings.HasPrefix(name, "runtime_delta_") && suffix != "" && repository.IsSQLIdentifier(name)
	for _, r := range suffix {
		if r < '0' || r > '9' {
			safeSchema = false
			break
		}
	}
	if !safeSchema {
		t.Fatalf("account metadata case requires a disposable runtime_delta_<digits> schema, got %q", name)
	}

	uow, ok := g.MailboxAssets.(repository.AccountMailboxCharacterUnitOfWork)
	if !ok || uow == nil {
		t.Fatal("account mailbox character transaction is unavailable")
	}

	const (
		accountID          = "metadata-mailbox-account"
		otherAccountID     = "metadata-mailbox-other-account"
		characterID        = "metadata-mailbox-character"
		unownedCharacterID = "metadata-mailbox-unowned-character"
		seedMailID         = "seed-receipt"
		claimKey           = "metadata_mailbox_claim"
		rollbackKey        = "metadata_mailbox_rollback_probe"
		successSource      = "metadata-mailbox-success"
		failureSource      = "metadata-mailbox-must-rollback"
		triggerName        = "reject_account_metadata_delete"
		seedMetadataCount  = 128
	)

	now := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	metadata := make(map[string]string, seedMetadataCount)
	for i := 0; i < seedMetadataCount; i++ {
		metadata[fmt.Sprintf("metadata_seed_%03d", i)] = fmt.Sprintf("value_%03d", i)
	}
	if err := g.Account.Save(ctx, repository.AccountRecord{
		AccountID: accountID,
		State:     "active",
		Metadata:  metadata,
	}); err != nil {
		t.Fatal(err)
	}
	if err := g.Account.Save(ctx, repository.AccountRecord{
		AccountID: otherAccountID,
		State:     "active",
		Metadata:  map[string]string{"metadata_mailbox_other": "preserve"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, repository.CharacterRecord{
		CharacterID: characterID,
		AccountID:   accountID,
		Slot:        0,
		Name:        "MetadataMailbox",
		Job:         "0",
		Level:       90,
		Stats:       map[string]int64{"gold": 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := g.Mailbox.Save(ctx, repository.MailboxRecord{
		CharacterID: characterID,
		Mails: map[string]repository.MailRecord{
			seedMailID: {
				MailID:               seedMailID,
				RecipientCharacterID: characterID,
				Title:                "seed receipt",
				CreatedAt:            now.Add(-time.Minute),
			},
		},
		UpdatedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	// Account.Save replaces the entire metadata map and starts with a DELETE.
	// Keeping this trigger installed while the single-key transaction succeeds
	// proves the wrapped writer did not fall back to a whole-account save.
	metadataTable := mysqlTable(name, defaultTablePrefix, mysqlAccountMetadataTable)
	triggerRef := quoteSQLIdentifier(name) + "." + quoteSQLIdentifier(triggerName)
	if _, err := db.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+triggerRef); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.ExecContext(cleanupCtx, "DROP TRIGGER IF EXISTS "+triggerRef); err != nil {
			t.Errorf("drop isolated metadata trigger: %v", err)
		}
	}()
	createTrigger := "CREATE TRIGGER " + triggerRef +
		" BEFORE DELETE ON " + metadataTable +
		" FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='account metadata delete forbidden'"
	if _, err := db.ExecContext(ctx, createTrigger); err != nil {
		t.Fatal(err)
	}

	assertSeedMetadata := func(account repository.AccountRecord) {
		t.Helper()
		for i := 0; i < seedMetadataCount; i++ {
			key := fmt.Sprintf("metadata_seed_%03d", i)
			want := fmt.Sprintf("value_%03d", i)
			if account.Metadata[key] != want {
				t.Fatalf("metadata %s=%q, want %q", key, account.Metadata[key], want)
			}
		}
	}
	loadAccount := func() repository.AccountRecord {
		t.Helper()
		account, found, err := g.Account.Load(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatal("target account missing")
		}
		return account
	}
	loadMailbox := func() repository.MailboxRecord {
		t.Helper()
		mailbox, found, err := g.Mailbox.Load(ctx, characterID)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatal("target mailbox missing")
		}
		return mailbox
	}

	successMailID := ""
	err := uow.WithinAccountMailboxCharacter(ctx, accountID, characterID, func(
		accounts repository.AccountRepository,
		character repository.CharacterRecord,
		mailboxes repository.MailboxRepository,
	) error {
		if character.CharacterID != characterID || character.AccountID != accountID {
			return fmt.Errorf("wrong character snapshot: id=%q account=%q", character.CharacterID, character.AccountID)
		}
		account, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found || len(account.Metadata) != seedMetadataCount {
			return fmt.Errorf("account metadata seed found=%t entries=%d", found, len(account.Metadata))
		}
		if err := repository.SaveAccountMetadataEntry(ctx, accounts, account, claimKey, "claimed", now); err != nil {
			return err
		}
		mailID, err := repository.AppendSystemMail(ctx, mailboxes, repository.SystemMailDelivery{
			RecipientCharacterID: characterID,
			Title:                "metadata mailbox receipt",
			Body:                 "single-key metadata committed with a mailbox receipt",
			Source:               successSource,
			CompletionKey:        successSource,
			Attachments:          []repository.MailAttachment{{ItemID: 1001, Count: 1}},
			CreatedAt:            now,
		})
		if err != nil {
			return err
		}
		successMailID = mailID
		return nil
	})
	if err != nil {
		t.Fatalf("metadata and mailbox transaction: %v", err)
	}
	if successMailID == "" {
		t.Fatal("mailbox receipt id was not returned")
	}

	accountAfterSuccess := loadAccount()
	if len(accountAfterSuccess.Metadata) != seedMetadataCount+1 {
		t.Fatalf("metadata entries after success=%d, want %d", len(accountAfterSuccess.Metadata), seedMetadataCount+1)
	}
	assertSeedMetadata(accountAfterSuccess)
	if accountAfterSuccess.Metadata[claimKey] != "claimed" {
		t.Fatalf("single-key metadata=%q", accountAfterSuccess.Metadata[claimKey])
	}
	mailboxAfterSuccess := loadMailbox()
	if _, ok := mailboxAfterSuccess.Mails[seedMailID]; !ok {
		t.Fatal("seed mailbox receipt disappeared")
	}
	successMail, ok := mailboxAfterSuccess.Mails[successMailID]
	if !ok || successMail.Metadata["source"] != successSource {
		t.Fatalf("success mailbox receipt id=%q mail=%+v", successMailID, successMail)
	}

	wrongOwnerCallbackRan := false
	err = uow.WithinAccountMailboxCharacter(ctx, otherAccountID, characterID, func(
		repository.AccountRepository,
		repository.CharacterRecord,
		repository.MailboxRepository,
	) error {
		wrongOwnerCallbackRan = true
		return nil
	})
	if !errors.Is(err, repository.ErrMailboxAssetTransactionUnavailable) {
		t.Fatalf("wrong-owner transaction error=%v", err)
	}
	if wrongOwnerCallbackRan {
		t.Fatal("wrong-owner callback ran")
	}

	foreignAccountRejected, foreignMailboxRejected := false, false
	err = uow.WithinAccountMailboxCharacter(ctx, accountID, characterID, func(
		accounts repository.AccountRepository,
		_ repository.CharacterRecord,
		mailboxes repository.MailboxRepository,
	) error {
		_, _, accountErr := accounts.Load(ctx, otherAccountID)
		foreignAccountRejected = accountErr != nil
		_, _, mailboxErr := mailboxes.Load(ctx, unownedCharacterID)
		foreignMailboxRejected = mailboxErr != nil
		if !foreignAccountRejected || !foreignMailboxRejected {
			return fmt.Errorf("scoped transaction accepted foreign account=%t foreign mailbox=%t", !foreignAccountRejected, !foreignMailboxRejected)
		}
		return accountErr
	})
	if err == nil || !foreignAccountRejected || !foreignMailboxRejected {
		t.Fatalf("scope rejection err=%v account=%t mailbox=%t", err, foreignAccountRejected, foreignMailboxRejected)
	}

	injectedFailure := errors.New("injected after metadata and mailbox writes")
	failedMailID := ""
	err = uow.WithinAccountMailboxCharacter(ctx, accountID, characterID, func(
		accounts repository.AccountRepository,
		_ repository.CharacterRecord,
		mailboxes repository.MailboxRepository,
	) error {
		account, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("target account disappeared")
		}
		if err := repository.SaveAccountMetadataEntry(ctx, accounts, account, rollbackKey, "written-before-error", now); err != nil {
			return err
		}
		mailID, err := repository.AppendSystemMail(ctx, mailboxes, repository.SystemMailDelivery{
			RecipientCharacterID: characterID,
			Title:                "rollback probe",
			Body:                 "must not survive callback failure",
			Source:               failureSource,
			CompletionKey:        failureSource,
			Attachments:          []repository.MailAttachment{{ItemID: 1002, Count: 1}},
			CreatedAt:            now.Add(time.Second),
		})
		if err != nil {
			return err
		}
		failedMailID = mailID
		written, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found || written.Metadata[rollbackKey] != "written-before-error" {
			return errors.New("rollback metadata was not written before injection")
		}
		mailbox, found, err := mailboxes.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("mailbox disappeared before injection")
		}
		if _, ok := mailbox.Mails[failedMailID]; !ok {
			return errors.New("rollback mailbox receipt was not written before injection")
		}
		return injectedFailure
	})
	if !errors.Is(err, injectedFailure) || failedMailID == "" {
		t.Fatalf("late callback failure err=%v failed_mail_id=%q", err, failedMailID)
	}

	accountAfterRollback := loadAccount()
	if len(accountAfterRollback.Metadata) != seedMetadataCount+1 {
		t.Fatalf("metadata entries after rollback=%d, want %d", len(accountAfterRollback.Metadata), seedMetadataCount+1)
	}
	assertSeedMetadata(accountAfterRollback)
	if accountAfterRollback.Metadata[claimKey] != "claimed" {
		t.Fatalf("committed claim rolled back: %q", accountAfterRollback.Metadata[claimKey])
	}
	if _, ok := accountAfterRollback.Metadata[rollbackKey]; ok {
		t.Fatal("rollback metadata row survived callback failure")
	}
	mailboxAfterRollback := loadMailbox()
	if _, ok := mailboxAfterRollback.Mails[seedMailID]; !ok {
		t.Fatal("seed mailbox receipt missing after rollback")
	}
	if _, ok := mailboxAfterRollback.Mails[successMailID]; !ok {
		t.Fatal("committed success receipt disappeared")
	}
	if _, ok := mailboxAfterRollback.Mails[failedMailID]; ok {
		t.Fatalf("rollback-only mailbox receipt %q survived", failedMailID)
	}
}
