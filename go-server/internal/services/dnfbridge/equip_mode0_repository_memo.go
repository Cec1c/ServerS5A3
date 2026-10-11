package dnfbridge

import (
	"context"
	"strings"
	"sync"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 穿脱装备 / 换时装时「外观重挂」这一次调用的**只读仓储记忆**。
//
// # 为什么需要它（2026-09-28 现场取证，不是推测）
//
// 操作者报障：「现在穿戴时装模型会跟着变 但是我对穿上脱下的速度不太满意」。
// mode0 外观包（`class0/op2 mode=0`，479 B）是**唯一**能让当前 EXE 重挂本地角色
// 外观的下行（证据见 game_aligned.go:799-828 与 avatar_appearance_coalesce.go），
// 而它在服务端的**构造期**（ACK 之后、mode0 发出之前）要反复读同一条记录。
//
// 用计数仓储实测 `sendSelectedActorMode0AppearanceRefresh` 的**一次调用**：
//
//	equipment loads=6  character loads=2  account loads=1
//
// 6 次装备读取的调用点（现场逐帧抓的，见 equip_mode0_repository_memo_test.go）：
//
//	#1 game_select_resolve.go:144            characterForEnter → dnfcharacterdata.Loader(PartEquipment)
//	#2 character_initialization.go:149       characterInitializationPresenceForRecord
//	#3 initial_equipment_roster_projection.go:136 attachEquipmentSummary
//	#4 current_actor_mode0_appearance_body.go:39  loadCurrentSelectedActorMode0AppearanceSummary
//	#5 current_actor_mode0_appearance_body.go:100 loadCurrentSelectedActorNameTagState
//	#6 current_equipped_creature.go:72       currentEquippedCreatureForCharacterWithError
//
// 也就是说：**一张 479 B 的外观包，要用同一条 `dnf_equipment_entries` 记录读 6 遍**
// （外加 2 遍角色、1 遍账号），全部落在「客户端点一下 → 模型重挂」的这条关键路径上。
// 这一段是**纯读**：durable 写入已经在 ACK 之前完成，构造期没有任何写。
//
// # 语义与安全边界
//
//   - 记忆**只活在一次调用里**（context 作用域，栈上持有），跨请求零共享、零过期；
//   - 只有 `Equipment` / `Character` / `Account` 三个仓读取被记忆，其余仓储原样透传；
//   - **任何一次 `Save` 立刻作废整份记忆**（`memoized*.Save` → `invalidate`），
//     所以"读-写-读"在同一调用内也绝不会读到旧值；
//   - 未安装记忆的路径（其它所有投影）逐字节不变：`repositoryGroupFor(ctx)` 在没有
//     记忆时直接回落 `s.repositoryGroup()`。
//
// # 开关（一键回退）
//
// `DNFBRIDGE_EQUIP_MODE0_REPOSITORY_MEMO`
//
//	未设 / reuse / on / 1 / 其它未知取值（**默认 = 修复档**）→ 装记忆（6→1 次装备读）。
//	off / disable / 0 / legacy / none                        → 逐字节回到今天：
//	    每次读都真的落到仓储（实测 6 次装备读）。
//
// 记忆不改变任何下行字节：`equip_mode0_repository_memo_test.go` 用同一份仓储
// 分别在开/关两档构造 mode0 包体并断言**逐字节相等**。
const equipMode0RepositoryMemoEnv = "DNFBRIDGE_EQUIP_MODE0_REPOSITORY_MEMO"

const (
	equipMode0RepositoryMemoReuse = "reuse"
	equipMode0RepositoryMemoOff   = "off"
)

// normalizeEquipMode0RepositoryMemoMode 归一化开关取值。
func normalizeEquipMode0RepositoryMemoMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case equipMode0RepositoryMemoOff, "disable", "0", "legacy", "none", "drop":
		return equipMode0RepositoryMemoOff
	default:
		return equipMode0RepositoryMemoReuse
	}
}

// equipMode0RepositoryMemoMode 返回归一化后的档位（日志用）。
func (s *Service) equipMode0RepositoryMemoMode() string {
	if s == nil || strings.TrimSpace(s.options.equipMode0RepositoryMemoMode) == "" {
		return equipMode0RepositoryMemoReuse
	}
	return s.options.equipMode0RepositoryMemoMode
}

// equipMode0RepositoryMemoEnabled 表示该档位要在外观刷新这一次调用里装只读记忆。
func (s *Service) equipMode0RepositoryMemoEnabled() bool {
	if s == nil {
		return false
	}
	return s.equipMode0RepositoryMemoMode() != equipMode0RepositoryMemoOff
}

// repositoryMemoKey 是 context 里的记忆键（私有空结构体，外部无法构造）。
type repositoryMemoKey struct{}

// repositoryMemo 是一次调用内的三个仓读取缓存。
//
// 零值可用；`mu` 只保护缓存与作废标志（同一调用链上可能有并发读）。
type repositoryMemo struct {
	mu sync.Mutex

	equipment     map[string]memoEquipmentEntry
	character     map[string]memoCharacterEntry
	account       map[string]memoAccountEntry
	invalidatedAt int
	loads         int
}

type memoEquipmentEntry struct {
	record dnfrepo.EquipmentRecord
	found  bool
}

type memoCharacterEntry struct {
	record dnfrepo.CharacterRecord
	found  bool
}

type memoAccountEntry struct {
	record dnfrepo.AccountRecord
	found  bool
}

// withEquipMode0RepositoryMemo 把一份空记忆挂到 ctx 上，返回新 ctx 与记忆本体。
// 调用方负责在本次调用结束时丢弃它（不需要显式清理）。
func withEquipMode0RepositoryMemo(ctx context.Context) (context.Context, *repositoryMemo) {
	if ctx == nil {
		ctx = context.Background()
	}
	memo := &repositoryMemo{}
	return context.WithValue(ctx, repositoryMemoKey{}, memo), memo
}

// repositoryMemoFromContext 取出本次调用链上的记忆；没有则返回 nil。
func repositoryMemoFromContext(ctx context.Context) *repositoryMemo {
	if ctx == nil {
		return nil
	}
	memo, _ := ctx.Value(repositoryMemoKey{}).(*repositoryMemo)
	return memo
}

// repositoryGroupFor 返回本次调用链应使用的仓储组。
//
// 装了记忆（只有外观刷新这一条路会装）→ 返回把 Equipment/Character/Account
// 换成记忆版的**副本**（其余字段与 `s.repositoryGroup()` 逐字段相同）；
// 没装 → 与改动前逐字节相同地返回原仓储组。
func (s *Service) repositoryGroupFor(ctx context.Context) (dnfrepo.Group, bool) {
	repos, ok := s.repositoryGroup()
	if !ok {
		return repos, false
	}
	memo := repositoryMemoFromContext(ctx)
	if memo == nil {
		return repos, true
	}
	return memo.wrap(repos), true
}

// wrap 只替换三个被记忆的仓；其它仓储原样透传。
func (m *repositoryMemo) wrap(repos dnfrepo.Group) dnfrepo.Group {
	wrapped := repos
	if repos.Equipment != nil {
		wrapped.Equipment = memoEquipmentRepository{memo: m, inner: repos.Equipment}
	}
	if repos.Character != nil {
		wrapped.Character = memoCharacterRepository{memo: m, inner: repos.Character}
	}
	if repos.Account != nil {
		wrapped.Account = memoAccountRepository{memo: m, inner: repos.Account}
	}
	return wrapped
}

// invalidate 作废全部缓存：任何 Save 之后绝不允许再读到旧值。
func (m *repositoryMemo) invalidate() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.equipment = nil
	m.character = nil
	m.account = nil
	m.invalidatedAt++
	m.mu.Unlock()
}

// loadsTotal 是给测试用的计数（只统计被记忆的三个仓的读取次数）。
func (m *repositoryMemo) loadsTotal() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loads
}

type memoEquipmentRepository struct {
	memo  *repositoryMemo
	inner dnfrepo.EquipmentRepository
}

func (r memoEquipmentRepository) Load(ctx context.Context, id string) (dnfrepo.EquipmentRecord, bool, error) {
	r.memo.mu.Lock()
	if entry, cached := r.memo.equipment[id]; cached {
		r.memo.mu.Unlock()
		return entry.record, entry.found, nil
	}
	r.memo.loads++
	r.memo.mu.Unlock()
	record, found, err := r.inner.Load(ctx, id)
	if err != nil {
		return record, found, err
	}
	r.memo.mu.Lock()
	if r.memo.equipment == nil {
		r.memo.equipment = make(map[string]memoEquipmentEntry, 1)
	}
	r.memo.equipment[id] = memoEquipmentEntry{record: record, found: found}
	r.memo.mu.Unlock()
	return record, found, nil
}

func (r memoEquipmentRepository) Save(ctx context.Context, record dnfrepo.EquipmentRecord) error {
	r.memo.invalidate()
	return r.inner.Save(ctx, record)
}

type memoCharacterRepository struct {
	memo  *repositoryMemo
	inner dnfrepo.CharacterRepository
}

func (r memoCharacterRepository) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	r.memo.mu.Lock()
	if entry, cached := r.memo.character[id]; cached {
		r.memo.mu.Unlock()
		return entry.record, entry.found, nil
	}
	r.memo.loads++
	r.memo.mu.Unlock()
	record, found, err := r.inner.Load(ctx, id)
	if err != nil {
		return record, found, err
	}
	r.memo.mu.Lock()
	if r.memo.character == nil {
		r.memo.character = make(map[string]memoCharacterEntry, 1)
	}
	r.memo.character[id] = memoCharacterEntry{record: record, found: found}
	r.memo.mu.Unlock()
	return record, found, nil
}

func (r memoCharacterRepository) Save(ctx context.Context, record dnfrepo.CharacterRecord) error {
	r.memo.invalidate()
	return r.inner.Save(ctx, record)
}

func (r memoCharacterRepository) ListByAccount(ctx context.Context, accountID string, limit int) ([]dnfrepo.CharacterRecord, error) {
	return r.inner.ListByAccount(ctx, accountID, limit)
}

func (r memoCharacterRepository) FindIDByName(ctx context.Context, name string) (string, bool, error) {
	return r.inner.FindIDByName(ctx, name)
}

func (r memoCharacterRepository) NextNumericID(ctx context.Context) (int, error) {
	return r.inner.NextNumericID(ctx)
}

type memoAccountRepository struct {
	memo  *repositoryMemo
	inner dnfrepo.AccountRepository
}

func (r memoAccountRepository) Load(ctx context.Context, id string) (dnfrepo.AccountRecord, bool, error) {
	r.memo.mu.Lock()
	if entry, cached := r.memo.account[id]; cached {
		r.memo.mu.Unlock()
		return entry.record, entry.found, nil
	}
	r.memo.loads++
	r.memo.mu.Unlock()
	record, found, err := r.inner.Load(ctx, id)
	if err != nil {
		return record, found, err
	}
	r.memo.mu.Lock()
	if r.memo.account == nil {
		r.memo.account = make(map[string]memoAccountEntry, 1)
	}
	r.memo.account[id] = memoAccountEntry{record: record, found: found}
	r.memo.mu.Unlock()
	return record, found, nil
}

func (r memoAccountRepository) Save(ctx context.Context, record dnfrepo.AccountRecord) error {
	r.memo.invalidate()
	return r.inner.Save(ctx, record)
}
