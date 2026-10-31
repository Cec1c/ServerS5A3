package mysql

import (
	"context"
	"encoding/hex"
	"fmt"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

// SQL forms a superset of Go's status normalization: removing whitespace in
// the middle may select extra candidates. The Go predicate below rejects
// those; Unicode edge whitespace and legacy separators cannot hide active IDs.
// This uses existing character/group indexes, without a schema migration.
var activeQuestStatusSQL = func() string {
	expression := "CAST(status AS BINARY)"
	for _, r := range "_- \t\n\v\f\r\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000" {
		literal := "0x" + hex.EncodeToString([]byte(string(r)))
		expression = "REPLACE(" + expression + ", " + literal + ", '')"
	}
	normalized := "LOWER(CONVERT(" + expression + " USING utf8mb4)) COLLATE utf8mb4_bin IN ('active', 'accepted', 'inprogress', 'progress')"
	// Canonical rows use two cheap comparisons. Only legacy aliases need the
	// normalization chain; explicit binary replacement avoids connection/table
	// collation conflicts and preserves UTF-8 bytes until LOWER is applied.
	return "CASE WHEN BINARY status IN ('active','accepted','inprogress','progress') THEN 1 WHEN BINARY status IN ('complete','completed','cleared','finished','done') THEN 0 ELSE " + normalized + " END = 1"
}()

func (s *mysqlQuestStore) LoadQuestActivePlanning(ctx context.Context, id string) (repository.QuestRecord, bool, error) {
	table, err := s.router.writeTable(mysqlQuestStatesTable, id)
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	query := s.router.selectQuery("SELECT quest_id, status FROM " + table + " WHERE character_id = ? AND state_group IN ('states', 'progress') AND " + activeQuestStatusSQL + " ORDER BY quest_id")
	rows, err := s.router.db.QueryContext(ctx, query, id)
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	var ids []int64
	for rows.Next() {
		var questID int64
		var status string
		if err := rows.Scan(&questID, &status); err != nil {
			rows.Close()
			return repository.QuestRecord{}, false, err
		}
		if repository.QuestStatusActive(status) {
			ids = append(ids, questID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return repository.QuestRecord{}, false, err
	}
	if err := rows.Close(); err != nil {
		return repository.QuestRecord{}, false, err
	}
	// Load both groups by ID, including an inactive States shadow. Each chunk
	// is bounded and fetches only these IDs' extra fields, never full history.
	return s.LoadQuestIDs(ctx, id, ids)
}

func (r *characterSettlementScopedQuestRepository) LoadQuestActivePlanning(ctx context.Context, id string) (repository.QuestRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.characterID, id); err != nil {
		return repository.QuestRecord{}, false, err
	}
	return repository.LoadQuestActivePlanning(ctx, r.base, id)
}

// Keep candidate SQL within the fixed-statement admission size.
func validateActiveQuestStatusSQL() error {
	if len(activeQuestStatusSQL) > 3000 || strings.Contains(activeQuestStatusSQL, "?") {
		return fmt.Errorf("active quest SQL predicate exceeds fixed shape")
	}
	return nil
}
