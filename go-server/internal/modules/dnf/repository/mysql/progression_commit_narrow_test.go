package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/progression"
	"longheng.io/server/internal/modules/dnf/repository"
)

// progressionCommitSQLState is a deterministic database/sql/driver fixture for
// exercising progression.Commit through the real MySQL unit-of-work and narrow
// character/skill loaders. All data here is synthetic.
type progressionCommitSQLState struct {
	characterID string
	accountID   string
	level       int
	stats       map[string]int64
	points      repository.SkillPointState
	updatedAt   time.Time

	begins    int
	commits   int
	rollbacks int
	reads     []progressionCommitSQLCall
	writes    []progressionCommitSQLCall
	failExec  int
	execErr   error
}

type progressionCommitSQLCall struct {
	query string
	args  []driver.Value
}

type progressionCommitConnector struct{ state *progressionCommitSQLState }

func (c progressionCommitConnector) Connect(context.Context) (driver.Conn, error) {
	return progressionCommitConn{state: c.state}, nil
}

func (c progressionCommitConnector) Driver() driver.Driver {
	return progressionCommitDriver{state: c.state}
}

type progressionCommitDriver struct{ state *progressionCommitSQLState }

func (d progressionCommitDriver) Open(string) (driver.Conn, error) {
	return progressionCommitConn{state: d.state}, nil
}

type progressionCommitConn struct{ state *progressionCommitSQLState }

func (c progressionCommitConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}

func (progressionCommitConn) Close() error { return nil }

func (c progressionCommitConn) Begin() (driver.Tx, error) {
	c.state.begins++
	return progressionCommitTx{state: c.state}, nil
}

func (c progressionCommitConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}

func (c progressionCommitConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.reads = append(c.state.reads, progressionCommitNamedCall(query, args))
	rows, width, err := c.state.selectRows(query)
	if err != nil {
		return nil, err
	}
	return &progressionCommitRows{rows: rows, width: width}, nil
}

func (c progressionCommitConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.writes = append(c.state.writes, progressionCommitNamedCall(query, args))
	if c.state.failExec > 0 && len(c.state.writes) == c.state.failExec {
		if c.state.execErr != nil {
			return nil, c.state.execErr
		}
		return nil, errors.New("synthetic progression write failure")
	}
	return driver.RowsAffected(1), nil
}

type progressionCommitTx struct{ state *progressionCommitSQLState }

func (t progressionCommitTx) Commit() error {
	t.state.commits++
	return nil
}

func (t progressionCommitTx) Rollback() error {
	t.state.rollbacks++
	return nil
}

type progressionCommitRows struct {
	rows  [][]driver.Value
	width int
	next  int
}

func (r *progressionCommitRows) Columns() []string {
	columns := make([]string, r.width)
	for idx := range columns {
		columns[idx] = "fixture_column"
	}
	return columns
}

func (*progressionCommitRows) Close() error { return nil }

func (r *progressionCommitRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

func progressionCommitNamedCall(query string, args []driver.NamedValue) progressionCommitSQLCall {
	values := make([]driver.Value, len(args))
	for idx := range args {
		values[idx] = args[idx].Value
	}
	return progressionCommitSQLCall{query: query, args: values}
}

func (s *progressionCommitSQLState) selectRows(query string) ([][]driver.Value, int, error) {
	switch {
	case strings.Contains(query, "SELECT character_id, account_id, level,"):
		values := make([]driver.Value, 0, 4+len(mysqlCharacterStatColumns))
		values = append(values, s.characterID, s.accountID, int64(s.level))
		for _, column := range mysqlCharacterStatColumns {
			values = append(values, s.stats[column.key])
		}
		values = append(values, s.updatedAt)
		return [][]driver.Value{values}, len(values), nil
	case strings.Contains(query, "SELECT stat_key, stat_value FROM"):
		keys := make([]string, 0, len(s.stats))
		for key := range s.stats {
			if !isMySQLCharacterMirrorStatKey(key) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		rows := make([][]driver.Value, 0, len(keys))
		for _, key := range keys {
			rows = append(rows, []driver.Value{key, s.stats[key]})
		}
		return rows, 2, nil
	case strings.Contains(query, "SELECT character_id, total_sp, remaining_sp, total_tp, remaining_tp, synced_level"):
		values := []driver.Value{
			s.characterID,
			int64(s.points.TotalSP),
			int64(s.points.RemainingSP),
			int64(s.points.TotalTP),
			int64(s.points.RemainingTP),
			int64(s.points.SyncedLevel),
			s.updatedAt,
		}
		return [][]driver.Value{values}, len(values), nil
	default:
		return nil, 0, errors.New("unexpected progression SQL query: " + query)
	}
}

func newProgressionCommitDriverUOW(t *testing.T, state *progressionCommitSQLState) repository.CharacterProgressionUnitOfWork {
	t.Helper()
	db := sql.OpenDB(progressionCommitConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	group, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{
		DatabasePlan: repository.DatabasePlan{
			WriteDatabases: []string{"writer"},
			ReadDatabases:  []string{"replica"},
		},
		TablePrefix: "dnf",
		Now: func() time.Time {
			return state.updatedAt
		},
	})
	if err != nil {
		t.Fatalf("NewMySQLGroupFromDB() error = %v", err)
	}
	if group.CharacterProgression == nil {
		t.Fatal("MySQL character progression unit of work is unavailable")
	}
	return group.CharacterProgression
}

func progressionCommitState(level int, experience int64, points repository.SkillPointState) *progressionCommitSQLState {
	updated := time.Date(2026, 10, 30, 15, 0, 0, 0, time.UTC)
	stats := make(map[string]int64, len(mysqlCharacterStatColumns)+1)
	for _, column := range mysqlCharacterStatColumns {
		stats[column.key] = column.fallback
	}
	stats["exp"] = experience
	return &progressionCommitSQLState{
		characterID: "synthetic-character",
		accountID:   "synthetic-account",
		level:       level,
		stats:       stats,
		points:      points,
		updatedAt:   updated,
	}
}

func progressionCommitPoints(level int) repository.SkillPointState {
	return repository.SkillPointState{
		TotalSP:     100,
		RemainingSP: 75,
		TotalTP:     3,
		RemainingTP: 2,
		SyncedLevel: level,
	}
}

func progressionCommitPlan(state *progressionCommitSQLState, nextLevel int, nextExperience uint32) progression.CommitPlan {
	return progression.CommitPlan{
		CharacterID: state.characterID,
		AccountID:   state.accountID,
		Expected: progression.CommitState{
			Level:       state.level,
			Experience:  uint32(state.stats["exp"]),
			SkillPoints: state.points,
		},
		Next: progression.CommitState{
			Level:       nextLevel,
			Experience:  nextExperience,
			SkillPoints: progressionCommitPoints(nextLevel),
		},
	}
}

func assertProgressionCommitLockedReads(t *testing.T, state *progressionCommitSQLState) {
	t.Helper()
	if len(state.reads) != 3 {
		t.Fatalf("progression reads = %d, want 3: %+v", len(state.reads), state.reads)
	}
	for _, call := range state.reads {
		if !strings.Contains(call.query, "`writer`.") || strings.Contains(call.query, "`replica`") {
			t.Fatalf("read did not use write database: %s", call.query)
		}
		if !strings.HasSuffix(call.query, " FOR UPDATE") {
			t.Fatalf("read did not lock its row: %s", call.query)
		}
		for _, omitted := range []string{"`dnf_skill_states`", "`dnf_skill_layouts`", "`dnf_skill_cooldowns`", "`dnf_rosters`"} {
			if strings.Contains(call.query, omitted) {
				t.Fatalf("narrow progression path read %s: %s", omitted, call.query)
			}
		}
	}
}

func assertProgressionCommitNoDelete(t *testing.T, state *progressionCommitSQLState) {
	t.Helper()
	for _, call := range state.writes {
		if strings.Contains(strings.ToUpper(call.query), "DELETE") {
			t.Fatalf("progression issued an unexpected delete: %s", call.query)
		}
	}
}

func TestProgressionCommitMySQLDriverOrdinaryExperienceWritesOnlyChangedEXP(t *testing.T) {
	ctx := context.Background()
	state := progressionCommitState(89, 100, progressionCommitPoints(89))
	state.stats["quest_receipt"] = 7
	uow := newProgressionCommitDriverUOW(t, state)
	plan := progressionCommitPlan(state, 89, 140)

	result, err := progression.Commit(ctx, uow, plan)
	if err != nil {
		t.Fatalf("progression.Commit() error = %v", err)
	}
	if !result.Applied || result.Idempotent || result.Previous != plan.Expected || result.Current != plan.Next {
		t.Fatalf("progression.Commit() result = %+v", result)
	}
	assertProgressionCommitLockedReads(t, state)
	if state.begins != 1 || state.commits != 1 || state.rollbacks != 0 {
		t.Fatalf("transaction counts begins=%d commits=%d rollbacks=%d", state.begins, state.commits, state.rollbacks)
	}
	if len(state.writes) != 1 {
		t.Fatalf("ordinary EXP writes = %d, want 1: %+v", len(state.writes), state.writes)
	}
	write := state.writes[0]
	if !strings.HasPrefix(write.query, "UPDATE ") || !strings.Contains(write.query, "`dnf_characters`") {
		t.Fatalf("ordinary EXP write = %s", write.query)
	}
	if !strings.Contains(write.query, "SET updated_at = ?, `exp` = ? WHERE") || strings.Contains(write.query, "level = ?") {
		t.Fatalf("ordinary EXP wrote unrelated character fields: %s", write.query)
	}
	if len(write.args) != 4 || write.args[1] != int64(140) || write.args[2] != state.characterID || write.args[3] != state.accountID {
		t.Fatalf("ordinary EXP args = %#v", write.args)
	}
	assertProgressionCommitNoDelete(t, state)
}

func TestProgressionCommitMySQLDriverMaximumLevelHonorWritesOnlyChangedExtraStat(t *testing.T) {
	ctx := context.Background()
	state := progressionCommitState(90, 900, progressionCommitPoints(90))
	state.stats["honor_progress"] = 10
	uow := newProgressionCommitDriverUOW(t, state)
	plan := progressionCommitPlan(state, 90, 900)
	plan.ExpectedCharacterStats = map[string]int64{"honor_progress": 10}
	plan.NextCharacterStats = map[string]int64{"honor_progress": 35}

	result, err := progression.Commit(ctx, uow, plan)
	if err != nil {
		t.Fatalf("progression.Commit() error = %v", err)
	}
	if !result.Applied || result.Idempotent {
		t.Fatalf("progression.Commit() result = %+v", result)
	}
	assertProgressionCommitLockedReads(t, state)
	if len(state.writes) != 2 {
		t.Fatalf("maximum-level honor writes = %d, want 2: %+v", len(state.writes), state.writes)
	}
	if got := state.writes[0].query; !strings.Contains(got, "SET updated_at = ? WHERE") || strings.Contains(got, "`exp` = ?") || strings.Contains(got, "level = ?") {
		t.Fatalf("maximum-level honor rewrote unchanged progression: %s", got)
	}
	honor := state.writes[1]
	if !strings.Contains(honor.query, "INSERT INTO ") || !strings.Contains(honor.query, "`dnf_character_stats`") {
		t.Fatalf("maximum-level honor write = %s", honor.query)
	}
	if len(honor.args) != 3 || honor.args[1] != "honor_progress" || honor.args[2] != int64(35) {
		t.Fatalf("maximum-level honor args = %#v", honor.args)
	}
	if state.commits != 1 || state.rollbacks != 0 {
		t.Fatalf("transaction counts commits=%d rollbacks=%d", state.commits, state.rollbacks)
	}
	assertProgressionCommitNoDelete(t, state)
}

func TestProgressionCommitMySQLDriverLevelAuditUsesMergingInsert(t *testing.T) {
	ctx := context.Background()
	state := progressionCommitState(89, 100, progressionCommitPoints(89))
	uow := newProgressionCommitDriverUOW(t, state)
	plan := progressionCommitPlan(state, 90, 140)

	result, err := progression.Commit(ctx, uow, plan)
	if err != nil {
		t.Fatalf("progression.Commit() error = %v", err)
	}
	if !result.Applied || result.Idempotent {
		t.Fatalf("progression.Commit() result = %+v", result)
	}
	assertProgressionCommitLockedReads(t, state)
	if len(state.writes) != 3 {
		t.Fatalf("level audit writes = %d, want 3: %+v", len(state.writes), state.writes)
	}
	characterWrite := state.writes[0]
	if !strings.Contains(characterWrite.query, "level = ?") || !strings.Contains(characterWrite.query, "`exp` = ?") {
		t.Fatalf("level/EXP update = %s", characterWrite.query)
	}
	auditWrite := state.writes[1]
	if !strings.Contains(auditWrite.query, "`dnf_character_stats`") || !strings.Contains(auditWrite.query, "IF(stat_value = 0") {
		t.Fatalf("level audit insert = %s", auditWrite.query)
	}
	if len(auditWrite.args) != 3 || auditWrite.args[1] != repository.LevelUpEventKey(90) || auditWrite.args[2].(int64) <= 0 {
		t.Fatalf("level audit args = %#v", auditWrite.args)
	}
	skillWrite := state.writes[2]
	if !strings.Contains(skillWrite.query, "INSERT INTO ") || !strings.Contains(skillWrite.query, "`dnf_skills`") {
		t.Fatalf("skill point update = %s", skillWrite.query)
	}
	if state.commits != 1 || state.rollbacks != 0 {
		t.Fatalf("transaction counts commits=%d rollbacks=%d", state.commits, state.rollbacks)
	}
	assertProgressionCommitNoDelete(t, state)
}

func TestProgressionCommitMySQLDriverWriteFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	state := progressionCommitState(89, 100, progressionCommitPoints(89))
	state.failExec = 2
	state.execErr = errors.New("synthetic level audit failure")
	uow := newProgressionCommitDriverUOW(t, state)
	plan := progressionCommitPlan(state, 90, 140)

	_, err := progression.Commit(ctx, uow, plan)
	if !errors.Is(err, state.execErr) {
		t.Fatalf("progression.Commit() error = %v", err)
	}
	if len(state.writes) != 2 {
		t.Fatalf("writes before rollback = %d, want 2: %+v", len(state.writes), state.writes)
	}
	if state.begins != 1 || state.commits != 0 || state.rollbacks != 1 {
		t.Fatalf("transaction counts begins=%d commits=%d rollbacks=%d", state.begins, state.commits, state.rollbacks)
	}
}

func TestProgressionCommitMySQLDriverOwnershipAndVersionRejectionIssueNoWrites(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*progressionCommitSQLState, *progression.CommitPlan)
		want   error
	}{
		{
			name: "wrong account",
			mutate: func(_ *progressionCommitSQLState, plan *progression.CommitPlan) {
				plan.AccountID = "another-synthetic-account"
			},
			want: progression.ErrCommitOwnershipMismatch,
		},
		{
			name: "stale version",
			mutate: func(_ *progressionCommitSQLState, plan *progression.CommitPlan) {
				plan.Expected.Experience = 99
			},
			want: progression.ErrCommitVersionPrecondition,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := progressionCommitState(89, 100, progressionCommitPoints(89))
			uow := newProgressionCommitDriverUOW(t, state)
			plan := progressionCommitPlan(state, 89, 140)
			test.mutate(state, &plan)

			_, err := progression.Commit(context.Background(), uow, plan)
			if !errors.Is(err, test.want) {
				t.Fatalf("progression.Commit() error = %v, want %v", err, test.want)
			}
			if len(state.writes) != 0 {
				t.Fatalf("rejected progression wrote %+v", state.writes)
			}
			if state.begins != 1 || state.commits != 0 || state.rollbacks != 1 {
				t.Fatalf("transaction counts begins=%d commits=%d rollbacks=%d", state.begins, state.commits, state.rollbacks)
			}
		})
	}
}
