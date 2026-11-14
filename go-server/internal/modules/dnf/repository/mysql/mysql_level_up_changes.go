package mysql

import r "longheng.io/server/internal/modules/dnf/repository"

func stageLevelUpChange(database SQLDB, feed *r.LevelUpChangeFeed, record r.CharacterRecord, previous *r.CharacterRecord, dirty map[r.CharacterField]struct{}) {
	if feed == nil {
		return
	}
	changed := false
	if _, stats := dirty[r.CharacterFieldStats]; stats {
		for key, value := range record.Stats {
			if _, event := r.LevelUpEventLevel(key); event && value > 0 && (previous == nil || previous.Stats[key] != value) {
				changed = true
				break
			}
		}
	}
	if _, base := dirty[r.CharacterFieldBase]; base && previous != nil && previous.Level != record.Level {
		changed = true
	}
	if !changed {
		return
	}
	db, ok := database.(sqlTransactionDB)
	if !ok {
		return
	}
	tx, ok := db.tx.(*committedSQLTransaction)
	if !ok {
		return
	}
	id := record.CharacterID
	tx.pending = append(tx.pending, func() { feed.Publish(id) })
}
