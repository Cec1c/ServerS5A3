package experienceitem

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	repo "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

// This opt-in regression creates and drops only its own uniquely named
// database on a loopback MySQL server. The configured database is never used.
func TestUseCapsuleMySQLAtomicRegression(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid disposable MySQL DSN")
	}
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil || cfg.Net != "tcp" || host != "127.0.0.1" || port == "" {
		t.Fatal("DNFBRIDGE_PLAYER_TEST_DSN must use tcp(127.0.0.1:<port>)")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open disposable MySQL failed")
	}
	defer db.Close()
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(20)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	name := fmt.Sprintf("capsule_use_test_%d", time.Now().UnixNano())
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := db.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS `"+name+"`"); err != nil {
			t.Errorf("drop disposable capsule database: %v", err)
		}
	}()

	plan := repo.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{
		AutoCreate:      true,
		CreateDatabases: true,
		DatabasePlan:    plan,
	}); err != nil {
		t.Fatal(err)
	}
	repos, err := repomysql.NewMySQLGroup(
		repomysql.SQLDBAdapter{DB: db},
		repomysql.MySQLGroupOptions{DatabasePlan: plan},
	)
	if err != nil {
		t.Fatal(err)
	}
	newGroup := func(t *testing.T) repo.Group {
		t.Helper()
		group, err := repomysql.NewMySQLGroup(
			repomysql.SQLDBAdapter{DB: db},
			repomysql.MySQLGroupOptions{DatabasePlan: plan},
		)
		if err != nil {
			t.Fatal(err)
		}
		return group
	}

	source, tables, command, now := capsuleUseFixture(t)

	t.Run("commits_one_capsule_exp_and_skill_points", func(t *testing.T) {
		const characterID = "177"
		request := command
		request.AccountID = capsuleMySQLAccountID(characterID)
		request.CharacterID = characterID
		request.Definition.Amount = 500

		before := capsuleMySQLSeed(t, ctx, repos, source, characterID)
		result, err := UseCapsule(ctx, repos, tables, request)
		if err != nil {
			t.Fatal(err)
		}
		if result.ItemID != request.Definition.ItemID || result.Remaining != 1 ||
			result.Progression.Experience.LevelsGained != 2 ||
			result.Progression.Experience.NewLevel != 6 ||
			result.Progression.Experience.NewExperience != 1000 ||
			result.Progression.SkillPoints.SPGain != 20 ||
			result.Progression.SkillPoints.TPGain != 2 {
			t.Fatalf("capsule result = %+v", result)
		}

		after := capsuleMySQLSnapshot(t, ctx, newGroup(t), characterID)
		if after.Character.Level != 6 ||
			after.Character.Stats["exp"] != 1000 ||
			after.Character.Stats["gold"] != 456 ||
			after.Character.Stats["marker"] != 9 ||
			after.Character.Location.TownID != 39 {
			t.Fatalf("persisted character = level %d stats %v location %+v", after.Character.Level, after.Character.Stats, after.Character.Location)
		}
		for _, level := range []int{5, 6} {
			if got := after.Character.Stats[repo.LevelUpEventKey(level)]; got != now.Unix() {
				t.Fatalf("level event %d = %d, want %d", level, got, now.Unix())
			}
		}
		if !reflect.DeepEqual(after.Skill.Points, result.Progression.SkillPoints.New) ||
			!reflect.DeepEqual(after.Skill.Skills, before.Skill.Skills) {
			t.Fatalf("persisted skill = points %+v skills %+v", after.Skill.Points, after.Skill.Skills)
		}
		sourceStack, found := after.Inventory.Slots["0:4"]
		if !found || sourceStack.Count != 1 || sourceStack.Extra["pvf_path"] != request.Definition.PVFPath {
			t.Fatalf("persisted source stack = %+v, found=%v", sourceStack, found)
		}
		capsuleMySQLAssertOtherSlotsUnchanged(t, before.Inventory, after.Inventory, "0:4")
		if !reflect.DeepEqual(after.Quest, before.Quest) {
			t.Fatalf("ordinary capsule changed quest state: before=%+v after=%+v", before.Quest, after.Quest)
		}
	})

	t.Run("late_revalidate_rolls_back_cross_level_settlement", func(t *testing.T) {
		const characterID = "277"
		request := command
		request.AccountID = capsuleMySQLAccountID(characterID)
		request.CharacterID = characterID
		request.Definition.Amount = 500
		revalidateCalls := 0
		request.Revalidate = func() bool {
			revalidateCalls++
			return revalidateCalls == 1
		}
		proposedLevel := 0
		request.LevelStats = func(_ repo.CharacterRecord, level int) (map[string]int64, error) {
			proposedLevel = level
			return nil, nil
		}

		before := capsuleMySQLSeed(t, ctx, repos, source, characterID)
		result, err := UseCapsule(ctx, repos, tables, request)
		if !errors.Is(err, ErrCapsule) {
			t.Fatalf("late revalidate error = %v, want ErrCapsule", err)
		}
		if !reflect.DeepEqual(result, CapsuleResult{}) {
			t.Fatalf("failed capsule returned a result: %+v", result)
		}
		if proposedLevel != 6 || revalidateCalls < 2 {
			t.Fatalf("rollback path proposed level=%d revalidate calls=%d", proposedLevel, revalidateCalls)
		}
		after := capsuleMySQLSnapshot(t, ctx, newGroup(t), characterID)
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("late revalidate retained character, bag, skill, or quest changes:\nbefore=%+v\nafter=%+v", before, after)
		}
	})

	t.Run("sixteen_concurrent_attempts_consume_last_capsule_once", func(t *testing.T) {
		const characterID = "377"
		request := command
		request.AccountID = capsuleMySQLAccountID(characterID)
		request.CharacterID = characterID
		request.Definition.Amount = 500

		capsuleMySQLSeed(t, ctx, repos, source, characterID)
		bag, _, err := repos.Inventory.Load(ctx, characterID)
		if err != nil {
			t.Fatal(err)
		}
		bag = repo.CloneInventory(bag)
		stack := bag.Slots["0:4"]
		stack.Count = 1
		bag.Slots["0:4"] = stack
		if err := repos.Inventory.Save(ctx, bag); err != nil {
			t.Fatal(err)
		}
		before := capsuleMySQLSnapshot(t, ctx, newGroup(t), characterID)

		const attempts = 16
		groups := make([]repo.Group, attempts)
		for i := range groups {
			groups[i] = newGroup(t)
		}
		type attemptResult struct {
			result CapsuleResult
			err    error
		}
		results := make(chan attemptResult, attempts)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < attempts; i++ {
			wg.Add(1)
			go func(group repo.Group) {
				defer wg.Done()
				<-start
				result, err := UseCapsule(ctx, group, tables, request)
				results <- attemptResult{result: result, err: err}
			}(groups[i])
		}
		close(start)
		wg.Wait()
		close(results)

		successes := 0
		failures := 0
		var success CapsuleResult
		for attempt := range results {
			switch {
			case attempt.err == nil:
				successes++
				success = attempt.result
			case errors.Is(attempt.err, ErrCapsule):
				failures++
			default:
				t.Fatalf("concurrent capsule error = %v, want ErrCapsule", attempt.err)
			}
		}
		if successes != 1 || failures != attempts-1 {
			t.Fatalf("concurrent successes=%d failures=%d", successes, failures)
		}
		if success.Remaining != 0 || success.Progression.Experience.NewLevel != 6 {
			t.Fatalf("winning result = %+v", success)
		}

		after := capsuleMySQLSnapshot(t, ctx, newGroup(t), characterID)
		if _, found := after.Inventory.Slots["0:4"]; found {
			t.Fatalf("consumed last capsule remains: %+v", after.Inventory.Slots["0:4"])
		}
		capsuleMySQLAssertOtherSlotsUnchanged(t, before.Inventory, after.Inventory, "0:4")
		if after.Character.Level != 6 ||
			after.Character.Stats["exp"] != 1000 ||
			after.Character.Stats["gold"] != 456 ||
			after.Character.Stats["marker"] != 9 {
			t.Fatalf("concurrent persisted character = level %d stats %v", after.Character.Level, after.Character.Stats)
		}
		if !reflect.DeepEqual(after.Skill.Points, success.Skill.Points) ||
			!reflect.DeepEqual(after.Skill.Skills, before.Skill.Skills) {
			t.Fatalf("concurrent persisted skill = points %+v skills %+v", after.Skill.Points, after.Skill.Skills)
		}
		if !reflect.DeepEqual(after.Quest, before.Quest) {
			t.Fatalf("concurrent capsule changed quest state: before=%+v after=%+v", before.Quest, after.Quest)
		}
	})
}

func capsuleMySQLAccountID(characterID string) string {
	return "capsule-mysql-account-" + characterID
}

func capsuleMySQLSeed(t *testing.T, ctx context.Context, target, source repo.Group, characterID string) capsuleUseState {
	t.Helper()
	record := capsuleUseSnapshot(t, source)
	record.Character = repo.CloneCharacter(record.Character)
	record.Skill = repo.CloneSkill(record.Skill)
	record.Inventory = repo.CloneInventory(record.Inventory)
	record.Quest = repo.CloneQuest(record.Quest)
	record.Character.CharacterID = characterID
	record.Character.AccountID = capsuleMySQLAccountID(characterID)
	record.Character.Name = "CapsuleMySQL" + characterID
	record.Skill.CharacterID = characterID
	record.Inventory.CharacterID = characterID
	record.Quest.CharacterID = characterID

	if err := target.Account.Save(ctx, repo.AccountRecord{
		AccountID: record.Character.AccountID,
		Metadata:  map[string]string{"fixture": "capsule_mysql"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := target.Character.Save(ctx, record.Character); err != nil {
		t.Fatalf("seed character: %v", err)
	}
	if err := target.Skill.Save(ctx, record.Skill); err != nil {
		t.Fatalf("seed skill: %v", err)
	}
	if err := target.Inventory.Save(ctx, record.Inventory); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	if err := target.Quest.Save(ctx, record.Quest); err != nil {
		t.Fatalf("seed quest: %v", err)
	}
	return capsuleMySQLSnapshot(t, ctx, target, characterID)
}

func capsuleMySQLSnapshot(t *testing.T, ctx context.Context, group repo.Group, characterID string) capsuleUseState {
	t.Helper()
	character, found, err := group.Character.Load(ctx, characterID)
	if err != nil || !found {
		t.Fatalf("load character %s: found=%v err=%v", characterID, found, err)
	}
	skill, found, err := group.Skill.Load(ctx, characterID)
	if err != nil || !found {
		t.Fatalf("load skill %s: found=%v err=%v", characterID, found, err)
	}
	inventory, found, err := group.Inventory.Load(ctx, characterID)
	if err != nil || !found {
		t.Fatalf("load inventory %s: found=%v err=%v", characterID, found, err)
	}
	quest, found, err := group.Quest.Load(ctx, characterID)
	if err != nil || !found {
		t.Fatalf("load quest %s: found=%v err=%v", characterID, found, err)
	}
	return capsuleUseState{
		Character: repo.CloneCharacter(character),
		Skill:     repo.CloneSkill(skill),
		Inventory: repo.CloneInventory(inventory),
		Quest:     repo.CloneQuest(quest),
	}
}

func capsuleMySQLAssertOtherSlotsUnchanged(t *testing.T, before, after repo.InventoryRecord, consumedKey string) {
	t.Helper()
	for key, want := range before.Slots {
		if key == consumedKey {
			continue
		}
		got, found := after.Slots[key]
		if !found || !reflect.DeepEqual(got, want) {
			t.Fatalf("inventory slot %s changed: before=%+v after=%+v found=%v", key, want, got, found)
		}
	}
	for key := range after.Slots {
		if key == consumedKey {
			continue
		}
		if _, found := before.Slots[key]; !found {
			t.Fatalf("inventory gained unexpected slot %s: %+v", key, after.Slots[key])
		}
	}
	if !reflect.DeepEqual(before.Warehouse, after.Warehouse) {
		t.Fatalf("warehouse changed: before=%+v after=%+v", before.Warehouse, after.Warehouse)
	}
}
