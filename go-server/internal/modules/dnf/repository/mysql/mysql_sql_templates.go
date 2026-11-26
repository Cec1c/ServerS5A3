package mysql

import "sync"

// mysqlSQLTemplates stores only SQL text derived from validated table names and
// fixed column lists. It never holds player rows, assets or request results.
type mysqlSQLTemplates struct {
	characterMirrorBase sync.Map
	characterBase       sync.Map
}

func (r mysqlRouter) characterBaseQuery(table string) string {
	key := table
	if r.lockReads {
		key += "\x00FOR UPDATE"
	}
	if r.sqlTemplates != nil {
		if cached, ok := r.sqlTemplates.characterBase.Load(key); ok {
			return cached.(string)
		}
	}
	query := "SELECT character_id, account_id, slot, name, job, level, " + mysqlCharacterStatSelectList() + ", created_at, updated_at FROM " + table + " WHERE character_id = ?"
	if r.lockReads {
		query += " FOR UPDATE"
	}
	if r.sqlTemplates == nil {
		return query
	}
	actual, _ := r.sqlTemplates.characterBase.LoadOrStore(key, query)
	return actual.(string)
}
