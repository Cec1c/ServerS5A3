package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestProgressionDeltaDoesNotRewriteUnrelatedStats(t *testing.T) {
	db := &fakeSQLDB{}
	store := newTestMySQLGroup(t, db).Character.(*mysqlCharStore)
	store.router.lockReads = true
	before := repository.CharacterRecord{CharacterID: "c", AccountID: "a", Level: 89, Stats: map[string]int64{"exp": 100}}
	for i := 0; i < 100; i++ {
		before.Stats[fmt.Sprintf("kept_%d", i)] = int64(i)
	}
	after := repository.CloneCharacter(before)
	after.Stats["exp"] = 120
	if err := store.SaveProgressionCharacter(context.Background(), before, after); err != nil {
		t.Fatal(err)
	}
	if len(db.execs) != 1 {
		t.Fatalf("ordinary EXP uses %d statements: %+v", len(db.execs), db.execs)
	}
	if !strings.Contains(db.execs[0].Query, "`exp` = ?") || strings.Contains(db.execs[0].Query, "DELETE") {
		t.Fatal(db.execs[0].Query)
	}
	db.execs = nil
	after.Level = 90
	after.Stats[repository.LevelUpEventKey(90)] = 1800000000
	if err := store.SaveProgressionCharacter(context.Background(), before, after); err != nil {
		t.Fatal(err)
	}
	if len(db.execs) != 2 || !strings.Contains(db.execs[1].Query, "IF(stat_value = 0") {
		t.Fatalf("permanent audit merge missing: %+v", db.execs)
	}
	for _, call := range db.execs {
		if strings.Contains(call.Query, "DELETE") {
			t.Fatal("history deleted")
		}
	}
}

func TestProgressionDeltaRejectsUnlockedOrForeignRecordAndPropagatesFailure(t *testing.T) {
	db := &fakeSQLDB{}
	s := newTestMySQLGroup(t, db).Character.(*mysqlCharStore)
	r := repository.CharacterRecord{CharacterID: "c", AccountID: "a", Level: 90, Stats: map[string]int64{"exp": 1}}
	if err := s.SaveProgressionCharacter(context.Background(), r, r); err == nil || len(db.execs) != 0 {
		t.Fatal("unlocked write accepted")
	}
	s.router.lockReads = true
	other := r
	other.AccountID = "b"
	if err := s.SaveProgressionCharacter(context.Background(), r, other); err == nil || len(db.execs) != 0 {
		t.Fatal("foreign account accepted")
	}
	want := errors.New("write failed")
	db.execErr = want
	if err := s.SaveProgressionCharacter(context.Background(), r, r); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
}

func TestProgressionReadsOmitRosterAndSkillCollections(t *testing.T) {
	values := []any{"c", "a", 90}
	for range mysqlCharacterStatColumns {
		values = append(values, int64(0))
	}
	values = append(values, time.Unix(1800000000, 0))
	db := &fakeSQLDB{rows: []fakeSQLRow{{values: values}, {values: []any{"c", 100, 50, 10, 5, 90, time.Unix(1800000000, 0)}}}}
	g := newTestMySQLGroup(t, db)
	c, ok, err := repository.LoadProgressionCharacter(context.Background(), g.Character, "c")
	if err != nil || !ok || c.AccountID != "a" {
		t.Fatalf("character=%+v ok=%t err=%v", c, ok, err)
	}
	s, ok, err := repository.LoadProgressionSkill(context.Background(), g.Skill, "c")
	if err != nil || !ok || s.Points.RemainingSP != 50 {
		t.Fatalf("skill=%+v ok=%t err=%v", s, ok, err)
	}
	if len(db.execs) != 3 {
		t.Fatalf("narrow read used %d statements", len(db.execs))
	}
	for _, call := range db.execs {
		for _, unwanted := range []string{"FROM `roster", "FROM `location", "skill_states", "cooldowns", "layouts"} {
			if strings.Contains(call.Query, unwanted) {
				t.Fatalf("unwanted %s query", unwanted)
			}
		}
	}
}

func TestProgressionDeltaFailureRollsBackAuditAndBaseTogether(t *testing.T) {
	for _, failAt := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			state := &inventoryGroupSQLState{}
			db := sql.OpenDB(questBatchConnector{state: state, failAt: failAt})
			defer db.Close()
			uow := &mysqlCharacterProgressionUnitOfWork{router: mysqlRouter{
				db: SQLDBAdapter{DB: db}, readDBs: []string{"read"}, writeDBs: []string{"write"}, tablePrefix: "dnf_",
			}}
			before := repository.CharacterRecord{CharacterID: "c", AccountID: "a", Level: 89, Stats: map[string]int64{"exp": 100}}
			after := repository.CloneCharacter(before)
			after.Level = 90
			after.Stats[repository.LevelUpEventKey(90)] = 1800000000
			err := uow.WithinCharacterProgression(context.Background(), "c", func(chars repository.CharacterRepository, _ repository.SkillRepository) error {
				return repository.SaveProgressionCharacter(context.Background(), chars, before, after)
			})
			if failAt == 0 {
				if err != nil || state.commits != 1 || state.rollbacks != 0 {
					t.Fatalf("success err=%v state=%+v", err, state)
				}
			} else if err == nil || state.commits != 0 || state.rollbacks != 1 {
				t.Fatalf("partial delta not rolled back err=%v state=%+v", err, state)
			}
		})
	}
}
