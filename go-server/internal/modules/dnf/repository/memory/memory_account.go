package memory

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"sync"
	"time"

	"longheng.io/server/internal/platform/db"
)

type memoryAccountStore struct {
	*db.MemoryStore[repository.AccountRecord]
	nameMu sync.Mutex
}

func newMemoryAccountStore() *memoryAccountStore {
	return &memoryAccountStore{
		MemoryStore: db.NewMemoryStore(repository.AccountKey, repository.CloneAccount),
	}
}

func (s *memoryAccountStore) Save(ctx context.Context, record repository.AccountRecord) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	s.nameMu.Lock()
	defer s.nameMu.Unlock()
	name := strings.TrimSpace(record.RepresentAccountName)
	if name != "" {
		for _, existing := range s.Snapshot(nil) {
			if existing.AccountID != record.AccountID && strings.EqualFold(strings.TrimSpace(existing.RepresentAccountName), name) {
				return repository.ErrRepresentAccountNameExists
			}
		}
	}
	record.RepresentAccountName = name
	return s.MemoryStore.Save(ctx, record)
}

func (s *memoryAccountStore) SaveMetadataEntry(
	ctx context.Context,
	accountID string,
	key string,
	value string,
	updatedAt time.Time,
) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	accountID = strings.TrimSpace(accountID)
	key = strings.TrimSpace(key)
	if accountID == "" {
		return db.ErrRecordKeyRequired
	}
	if key == "" {
		return repository.ErrAccountMetadataKeyRequired
	}
	s.nameMu.Lock()
	defer s.nameMu.Unlock()
	record, found, err := s.MemoryStore.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found {
		return repository.ErrAccountMetadataUnavailable
	}
	record = repository.CloneAccount(record)
	if record.Metadata == nil {
		record.Metadata = make(map[string]string)
	}
	record.Metadata[key] = value
	record.UpdatedAt = updatedAt.UTC()
	return s.MemoryStore.Save(ctx, record)
}

// AddAccountEpicPieceBalance 在内存仓储上原子自增账号级史诗图鉴碎片数量。
// 与 SaveMetadataEntry 共用 nameMu，因此"读取-加-写回"整段不会被并发写穿插；
// 结果按 repository.AccountEpicPieceBalanceMax 截顶，与 MySQL 档的 LEAST(...) 同上限。
func (s *memoryAccountStore) AddAccountEpicPieceBalance(
	ctx context.Context,
	accountID string,
	pieceID uint32,
	amount uint32,
	updatedAt time.Time,
) (uint32, error) {
	if err := ctxErr(ctx); err != nil {
		return 0, err
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return 0, db.ErrRecordKeyRequired
	}
	if pieceID == 0 || amount == 0 {
		return 0, repository.ErrAccountEpicPieceIDRequired
	}
	s.nameMu.Lock()
	defer s.nameMu.Unlock()
	record, found, err := s.MemoryStore.Load(ctx, accountID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, repository.ErrAccountMetadataUnavailable
	}
	record = repository.CloneAccount(record)
	if record.Metadata == nil {
		record.Metadata = make(map[string]string)
	}
	key := repository.AccountEpicPieceMetadataKey(pieceID)
	next := uint64(repository.ParseAccountEpicPieceBalance(record.Metadata[key])) + uint64(amount)
	if next > uint64(repository.AccountEpicPieceBalanceMax) {
		next = uint64(repository.AccountEpicPieceBalanceMax)
	}
	record.Metadata[key] = repository.FormatAccountEpicPieceBalance(uint32(next))
	record.UpdatedAt = updatedAt.UTC()
	if err := s.MemoryStore.Save(ctx, record); err != nil {
		return 0, err
	}
	return uint32(next), nil
}

// AdvanceStoryDigestViewedLevel 单调推进账号级"剧情回顾已看过"等级。
// 与 SaveMetadataEntry 用同一把锁，读取-比较-写入之间不会被并发写穿插，
// 因此重放或并发上报都不会让记录回退。
func (s *memoryAccountStore) AdvanceStoryDigestViewedLevel(
	ctx context.Context,
	accountID string,
	level uint32,
) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return db.ErrRecordKeyRequired
	}
	s.nameMu.Lock()
	defer s.nameMu.Unlock()
	record, found, err := s.MemoryStore.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found {
		return repository.ErrAccountStoryDigestAccountMissing
	}
	if repository.AccountStoryDigestViewedLevel(record) >= level {
		return nil
	}
	record = repository.CloneAccount(record)
	if record.Metadata == nil {
		record.Metadata = make(map[string]string)
	}
	record.Metadata[repository.AccountStoryDigestViewedLevelMetadataKey] =
		repository.FormatAccountStoryDigestViewedLevel(level)
	record.UpdatedAt = time.Now().UTC()
	return s.MemoryStore.Save(ctx, record)
}

func (s *memoryAccountStore) FindAccountIDByRepresentName(ctx context.Context, name string) (string, bool, error) {
	if err := ctxErr(ctx); err != nil {
		return "", false, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false, nil
	}
	for _, record := range s.Snapshot(nil) {
		if strings.EqualFold(strings.TrimSpace(record.RepresentAccountName), name) {
			return record.AccountID, true, nil
		}
	}
	return "", false, nil
}

// ListAccounts 是有界的跨账号检索（GM 面板用），与 MySQL 实现同一套契约：
// 先按 `updated_at DESC, account_id ASC` 统一排序，再按 query 过滤（大小写不敏感，
// 同时匹配 account_id 与代表角色名），最后截到 limit。
//
// 用 `Snapshot(less)` 而不是先取快照再自己排序：Snapshot 返回的已经是**副本**
// （`memory_store.go:76-88`），所以排序不会碰到内部顺序，也就不需要再深拷贝一次。
func (s *memoryAccountStore) ListAccounts(ctx context.Context, query string, limit int) ([]repository.AccountRecord, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	records := s.Snapshot(func(left, right repository.AccountRecord) bool {
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		return left.AccountID < right.AccountID
	})
	out := make([]repository.AccountRecord, 0, limit)
	for _, record := range records {
		if query != "" &&
			!strings.Contains(strings.ToLower(record.AccountID), query) &&
			!strings.Contains(strings.ToLower(record.RepresentAccountName), query) {
			continue
		}
		out = append(out, record)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
