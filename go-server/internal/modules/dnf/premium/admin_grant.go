package premium

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

type AdminGrantResult struct {
	ExpireAt int64 `json:"expireAt"`
	Replayed bool  `json:"replayed"`
}

// GrantAdminContract must run inside the account transaction. A durable request
// receipt prevents a retried admin operation from extending the contract twice.
func GrantAdminContract(account *repo.AccountRecord, characterID string, premiumType, days int64, requestID string, now time.Time) (AdminGrantResult, error) {
	if account == nil || strings.TrimSpace(account.AccountID) == "" || characterID == "" || now.IsZero() {
		return AdminGrantResult{}, fmt.Errorf("契约账号、角色或时间无效")
	}
	switch premiumType {
	case TypeBlackDiamond, TypeOverEquip, TypeOverSkill, TypeBonusExp, TypeCrystal, TypeLethe:
	default:
		return AdminGrantResult{}, fmt.Errorf("不支持此契约类型")
	}
	if days < 1 || days > 3650 || len(requestID) < 8 || len(requestID) > 128 {
		return AdminGrantResult{}, fmt.Errorf("开通天数或请求编号无效")
	}
	digest := sha256.Sum256([]byte(requestID))
	key := "gm_premium_request_" + hex.EncodeToString(digest[:])
	fingerprint := characterID + ":" + strconv.FormatInt(premiumType, 10) + ":" + strconv.FormatInt(days, 10)
	if receipt := account.Metadata[key]; receipt != "" {
		fields := strings.Split(receipt, "|")
		if len(fields) != 2 || fields[0] != fingerprint {
			return AdminGrantResult{}, fmt.Errorf("请求编号已用于其他契约操作")
		}
		expiry, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return AdminGrantResult{}, fmt.Errorf("契约操作回执无效")
		}
		return AdminGrantResult{ExpireAt: expiry, Replayed: true}, nil
	}
	base := now.Unix()
	if current := ExpireAt(*account, premiumType); current > base {
		base = current
	}
	if base > math.MaxInt64-days*86400 {
		return AdminGrantResult{}, fmt.Errorf("契约到期时间超出范围")
	}
	Upsert(account, premiumType, days*86400, 1, now)
	expiry := ExpireAt(*account, premiumType)
	account.Metadata[key] = fingerprint + "|" + strconv.FormatInt(expiry, 10)
	return AdminGrantResult{ExpireAt: expiry}, nil
}
