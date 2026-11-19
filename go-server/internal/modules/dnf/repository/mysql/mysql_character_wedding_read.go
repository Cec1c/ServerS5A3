package mysql

import (
	"context"
	"database/sql"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func (s *mysqlCharStore) LoadCharacterWedding(ctx context.Context, id string) (r.CharacterWeddingSnapshot, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return r.CharacterWeddingSnapshot{}, false, err
	}
	characters, err := s.router.readTable(mysqlCharTable, id)
	if err != nil {
		return r.CharacterWeddingSnapshot{}, false, err
	}
	stats, err := s.router.readTable(mysqlCharacterStatsTable, id)
	if err != nil {
		return r.CharacterWeddingSnapshot{}, false, err
	}

	execDB := s.router.db
	var readTx *observedSQLTransaction
	if _, inTx := execDB.(sqlTransactionDB); !inTx && !s.router.lockReads {
		if beginner, ok := execDB.(sqlTxBeginner); ok {
			tx, txErr := beginObservedSQLTransaction(ctx, beginner, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
			if txErr != nil {
				return r.CharacterWeddingSnapshot{}, false, txErr
			}
			readTx = tx
			defer readTx.Rollback()
			execDB = newSQLTransactionDB(readTx.Tx, s.router.db)
		}
	}

	// Split primary-key character lookup from the clustered (character_id, stat_key)
	// range scan so MariaDB never materializes a temporary table + filesort for
	// LEFT JOIN ... ORDER BY s.stat_key.
	result := r.CharacterWeddingSnapshot{Stats: map[string]int64{}}
	charQuery := s.router.selectQuery("SELECT character_id, account_id FROM " + characters + " WHERE character_id = ?")
	err = execDB.QueryRowContext(ctx, charQuery, id).Scan(&result.CharacterID, &result.AccountID)
	if errors.Is(err, sql.ErrNoRows) {
		if readTx != nil {
			if commitErr := readTx.Commit(); commitErr != nil {
				return r.CharacterWeddingSnapshot{}, false, commitErr
			}
		}
		return r.CharacterWeddingSnapshot{}, false, nil
	}
	if err != nil {
		return r.CharacterWeddingSnapshot{}, false, err
	}

	statsQuery := s.router.selectQuery("SELECT stat_key, stat_value FROM " + stats + " WHERE character_id = ? AND stat_key LIKE ? ESCAPE '=' ORDER BY stat_key")
	rows, err := execDB.QueryContext(ctx, statsQuery, id, "wedding=_v1=_%")
	if err != nil {
		return r.CharacterWeddingSnapshot{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var value int64
		if err := rows.Scan(&key, &value); err != nil {
			return r.CharacterWeddingSnapshot{}, false, err
		}
		result.Stats[key] = value
	}
	if err := rows.Err(); err != nil {
		return r.CharacterWeddingSnapshot{}, false, err
	}
	if err := rows.Close(); err != nil {
		return r.CharacterWeddingSnapshot{}, false, err
	}
	if readTx != nil {
		if commitErr := readTx.Commit(); commitErr != nil {
			return r.CharacterWeddingSnapshot{}, false, commitErr
		}
	}
	return result, true, nil
}
