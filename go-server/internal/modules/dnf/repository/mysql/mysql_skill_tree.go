package mysql

import (
	"context"
	"database/sql"
	"fmt"

	"longheng.io/server/internal/modules/dnf/repository"
)

const (
	mysqlSkillTreeTable       = "skill_tree_profiles"
	mysqlSkillTreeStatesTable = "skill_tree_profile_states"
)

// 新增关系表，不改写历史已学技能，也不把旧 skill_tree_index=0/1 当成购买凭据。
func mysqlSkillTreeSchema(database, prefix string) []string {
	return []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
  character_id VARCHAR(128) NOT NULL,
  expanded TINYINT UNSIGNED NOT NULL DEFAULT 0,
  active_tree TINYINT UNSIGNED NOT NULL DEFAULT 0,
  total_sp INT NOT NULL DEFAULT 0,
  remaining_sp INT NOT NULL DEFAULT 0,
  total_tp INT NOT NULL DEFAULT 0,
  remaining_tp INT NOT NULL DEFAULT 0,
  synced_level INT NOT NULL DEFAULT 0,
  PRIMARY KEY (character_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`, mysqlTable(database, prefix, mysqlSkillTreeTable)),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
  character_id VARCHAR(128) NOT NULL,
  skill_id BIGINT NOT NULL,
  skill_level INT NOT NULL DEFAULT 0,
  enabled TINYINT UNSIGNED NOT NULL DEFAULT 0,
  PRIMARY KEY (character_id, skill_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`, mysqlTable(database, prefix, mysqlSkillTreeStatesTable)),
	}
}

func (s *mysqlSkillStore) loadSkillTrees(ctx context.Context, characterID string) (repository.SkillTreeState, error) {
	state, found, err := s.loadSkillTreeProfile(ctx, characterID)
	if err != nil || !found {
		return state, err
	}
	statesTable, err := s.router.readTable(mysqlSkillTreeStatesTable, characterID)
	if err != nil {
		return state, err
	}
	state.Other.Skills, err = loadSkillStates(ctx, s.router, statesTable, characterID)
	return state, err
}

// 复用同一条页头查询及校验，经验窄读不加载另一页技能明细。
func (s *mysqlSkillStore) loadSkillTreeProfile(ctx context.Context, characterID string) (repository.SkillTreeState, bool, error) {
	var state repository.SkillTreeState
	table, err := s.router.readTable(mysqlSkillTreeTable, characterID)
	if err != nil {
		return state, false, err
	}
	err = s.router.db.QueryRowContext(ctx, s.router.selectQuery(
		"SELECT expanded, active_tree, total_sp, remaining_sp, total_tp, remaining_tp, synced_level FROM "+table+" WHERE character_id = ?"), characterID).Scan(
		&state.Expanded, &state.Active, &state.Other.Points.TotalSP, &state.Other.Points.RemainingSP,
		&state.Other.Points.TotalTP, &state.Other.Points.RemainingTP, &state.Other.Points.SyncedLevel)
	if err == sql.ErrNoRows {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	if state.Active > 1 || (!state.Expanded && state.Active != 0) {
		return state, false, fmt.Errorf("技能页持久化状态无效: character_id=%s active_tree=%d expanded=%t", characterID, state.Active, state.Expanded)
	}
	return state, true, nil
}

func (s *mysqlSkillStore) saveSkillTrees(ctx context.Context, database SQLDB, characterID string, state repository.SkillTreeState) error {
	if state.Active > 1 || (!state.Expanded && state.Active != 0) {
		return fmt.Errorf("技能页保存状态无效: character_id=%s", characterID)
	}
	table, err := s.router.writeTable(mysqlSkillTreeTable, characterID)
	if err != nil {
		return err
	}
	statesTable, err := s.router.writeTable(mysqlSkillTreeStatesTable, characterID)
	if err != nil {
		return err
	}
	columns := []string{"character_id", "expanded", "active_tree", "total_sp", "remaining_sp", "total_tp", "remaining_tp", "synced_level"}
	var updates []string
	for _, column := range columns[1:] {
		updates = append(updates, updateValue(column))
	}
	points := state.Other.Points
	if _, err := database.ExecContext(ctx, buildUpsert(table, columns, updates), characterID, state.Expanded, state.Active,
		points.TotalSP, points.RemainingSP, points.TotalTP, points.RemainingTP, points.SyncedLevel); err != nil {
		return err
	}
	return replaceSkillStates(ctx, database, statesTable, characterID, state.Other.Skills)
}
