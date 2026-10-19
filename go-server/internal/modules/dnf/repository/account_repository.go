// 本文件定义 DNF 账号仓储接口和记录。
package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"longheng.io/server/internal/platform/db"
)

// AccountRepository 保存账号级可变状态。
type AccountRepository interface {
	db.Store[AccountRecord]
}

// AccountMetadataEntryWriter persists one normalized account-metadata row
// without replacing unrelated keys.
type AccountMetadataEntryWriter interface {
	SaveMetadataEntry(context.Context, string, string, string, time.Time) error
}

// AccountMetadataLimitedEntryWriter serializes a bounded active set against
// other account metadata writes, preserving unrelated entries and retries.
type AccountMetadataLimitedEntryWriter interface {
	SaveMetadataEntryWithPrefixLimit(ctx context.Context, accountID, prefix, key, activeValue string, limit int, updatedAt time.Time) error
}

var ErrAccountMetadataLimitReached = errors.New("dnf account metadata active set limit reached")

// RepresentAccountNameFinder exposes the account-wide nickname uniqueness
// lookup required by the current client's adventure-group registration flow.
// It is kept separate from AccountRepository so transaction-scoped stores do
// not need to support a cross-account query.
type RepresentAccountNameFinder interface {
	FindAccountIDByRepresentName(context.Context, string) (string, bool, error)
}

var ErrRepresentAccountNameExists = errors.New("dnf represent account name already exists")

var (
	ErrAccountMetadataKeyRequired = errors.New("dnf account metadata key is required")
	ErrAccountMetadataUnavailable = errors.New("dnf account metadata repository is unavailable")
	// ErrAccountListUnavailable：具体仓储没有实现跨账号检索能力时返回。
	// 与 ErrAccountMetadataUnavailable 同风格——"这个能力没接上"是一种明确的失败，
	// 绝不退化成"返回空列表"（那会让 GM 面板把"查不到"显示成"没有这个账号"）。
	ErrAccountListUnavailable = errors.New("dnf account list repository is unavailable")
)

// AccountLister 暴露 GM 面板使用的**有界跨账号检索**。
//
// 与 RepresentAccountNameFinder 同样的理由单独成接口、不并进 AccountRepository：
// 事务作用域里的账号 store 不该凭空获得跨账号读能力。
type AccountLister interface {
	ListAccounts(context.Context, string, int) ([]AccountRecord, error)
}

// ListAccounts 在具体仓储显式支持时执行一次有界跨账号检索。
//
// 契约（与上层 GM 面板的约定，全部在测试里钉死）：
//   - repo 为 nil、或未实现 AccountLister ⇒ ErrAccountListUnavailable（不是空列表）；
//   - query 会被 TrimSpace；空串表示不过滤；
//   - limit <= 0 取 50，> 200 截到 200 —— 上限存在的意义是这次查询一定会跨账号，
//     不能让一个手滑的调用把整张账号表拉进内存；
//   - ctx 为 nil 时用 context.Background()。
func ListAccounts(ctx context.Context, repo AccountRepository, query string, limit int) ([]AccountRecord, error) {
	if repo == nil {
		return nil, ErrAccountListUnavailable
	}
	lister, ok := repo.(AccountLister)
	if !ok {
		return nil, ErrAccountListUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	return lister.ListAccounts(ctx, strings.TrimSpace(query), limit)
}

// AccountRecord 是 DNF 账号仓储记录。
type AccountRecord struct {
	AccountID            string            `json:"account_id"`
	State                string            `json:"state,omitempty"`
	HonorExp             uint64            `json:"honor_exp,omitempty"`
	RepresentAccountName string            `json:"represent_account_name,omitempty"`
	Metadata             map[string]string `json:"metadata,omitempty"`
	CreatedAt            time.Time         `json:"created_at,omitempty"`
	UpdatedAt            time.Time         `json:"updated_at,omitempty"`
}

// CloneAccount 拷贝账号记录，避免调用方和底层存储共享 Metadata map。
func CloneAccount(record AccountRecord) AccountRecord {
	record.Metadata = cloneStringMap(record.Metadata)
	return record
}

// SaveAccountMetadataEntry updates one metadata key. MySQL and memory stores
// implement a native key-level write; other repositories fall back to saving
// the supplied authoritative account snapshot.
func SaveAccountMetadataEntry(
	ctx context.Context,
	repo AccountRepository,
	account AccountRecord,
	key string,
	value string,
	updatedAt time.Time,
) error {
	if repo == nil {
		return ErrAccountMetadataUnavailable
	}
	accountID := strings.TrimSpace(account.AccountID)
	if accountID == "" {
		return db.ErrRecordKeyRequired
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ErrAccountMetadataKeyRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	updatedAt = updatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	if writer, ok := repo.(AccountMetadataEntryWriter); ok {
		return writer.SaveMetadataEntry(ctx, accountID, key, value, updatedAt)
	}
	account = CloneAccount(account)
	if account.Metadata == nil {
		account.Metadata = make(map[string]string)
	}
	account.Metadata[key] = value
	account.UpdatedAt = updatedAt
	return repo.Save(ctx, account)
}

func AccountKey(record AccountRecord) string {
	return strings.TrimSpace(record.AccountID)
}

type PVPTotalTeamNameFinder interface {
	FindAccountIDByPVPTotalTeamName(context.Context, string) (string, bool, error)
}
