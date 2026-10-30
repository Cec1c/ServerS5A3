package dnfbridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"time"

	"github.com/go-sql-driver/mysql"
)

var shortBindingCode = regexp.MustCompile(`^[0-9]{6}$`)
var legacyBindingCode = regexp.MustCompile(`^[a-f0-9]{32}$`)

func bindingCodeHash(code string) string {
	digest := sha256.Sum256([]byte(code))
	return hex.EncodeToString(digest[:])
}

// Reservations prevent collisions and keep consumed or rotated codes out of
// circulation until their original deadline. Only _qq_codes can authorize a
// binding; a reservation alone cannot. Neither table contains plaintext codes.
func (a *accountAPIServer) reserveBindingCode(ctx context.Context, tx *sql.Tx, id string, expires int64, random io.Reader) (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		n, err := rand.Int(random, big.NewInt(1000000))
		if err != nil {
			return "", err
		}
		code := fmt.Sprintf("%06d", n.Int64())
		hash := bindingCodeHash(code)
		_, err = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_qq_code_reservations(code_hash,account_id,expires_at) VALUES(?,?,?)", hash, id, expires)
		if err == nil {
			return code, nil
		}
		var duplicate *mysql.MySQLError
		if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
			return "", err
		}
		result, err := tx.ExecContext(ctx, "UPDATE "+a.table+"_qq_code_reservations SET account_id=?,expires_at=? WHERE code_hash=? AND expires_at<=?", id, expires, hash, time.Now().Unix())
		if err != nil {
			return "", err
		}
		if changed, err := result.RowsAffected(); err != nil {
			return "", err
		} else if changed == 1 {
			return code, nil
		}
	}
	return "", securityError(503, "binding_busy", "绑定码生成繁忙，请稍后重试")
}

// Persist the QQ attempt budget separately from the binding transaction so a
// failed code or process restart cannot reset it. It never affects login limits.
func (a *accountAPIServer) chargeBindingAttempt(ctx context.Context, qq string) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	key, now := bindingCodeHash("qq-binding:"+qq), time.Now().Unix()
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_auth_failures(subject,updated_at) VALUES(?,?) ON DUPLICATE KEY UPDATE subject=VALUES(subject)", key, now); err != nil {
		return err
	}
	var count int
	var start int64
	if err = tx.QueryRowContext(ctx, "SELECT failures,updated_at FROM "+a.table+"_auth_failures WHERE subject=? FOR UPDATE", key).Scan(&count, &start); err != nil {
		return err
	}
	if start+600 <= now {
		count, start = 0, now
	}
	if count >= 6 {
		return errors.New("绑定尝试过于频繁，请在 10 分钟后重试")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE "+a.table+"_auth_failures SET failures=?,updated_at=? WHERE subject=?", count+1, start, key); err != nil {
		return err
	}
	return tx.Commit()
}
