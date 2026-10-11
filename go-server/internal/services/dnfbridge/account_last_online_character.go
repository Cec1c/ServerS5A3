package dnfbridge

import (
	"context"
	"strconv"
	"strings"
	"sync"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// lastOnlineCharacterMetadataKey 是账号 metadata 的键级字段（不是数据库列）：该账号
// 最后一次成功选角的角色 ID。好友离线时用它恢复"上次在线角色"；写入复用既有键级
// 接口 dnfrepo.SaveAccountMetadataEntry，不改 schema、不改协议。
const lastOnlineCharacterMetadataKey = "last_online_character"

// lastOnlineCharacterState 是账号级写序列点，用来堵住"旧选角的写迟到、把新值覆盖
// 回去"的 TOCTOU：
//   - locks：每账号一把锁，把"读旧值 → 比较 → 写新值"做成串行临界区；
//   - applied：每账号已生效的选角代次。
//
// 代次直接用会话 bind 时由 s.nextGameSessionGeneration 全 bridge 单调分配的 generation
// （identity.generation），不另造 token：旧 session 即使 remember 调用更晚、或阻塞在 IO
// 里更久，它的 generation 也更小，因此在临界区里输给新选角。代次是进程内状态：本服务是
// 单进程（单一 composition root）账号 metadata 的唯一写入者，在飞行的旧写与新写共享
// 同一张表；跨进程写入需要数据库内的持久版本，这里不假装支持。
type lastOnlineCharacterState struct {
	mu      sync.Mutex
	applied map[string]uint64
	locks   map[string]*sync.Mutex
}

func (st *lastOnlineCharacterState) accountLock(accountID string) *sync.Mutex {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.locks == nil {
		st.locks = make(map[string]*sync.Mutex)
	}
	if st.locks[accountID] == nil {
		st.locks[accountID] = &sync.Mutex{}
	}
	return st.locks[accountID]
}

func (st *lastOnlineCharacterState) appliedSeq(accountID string) uint64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.applied[accountID]
}

func (st *lastOnlineCharacterState) markApplied(accountID string, seq uint64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.applied == nil {
		st.applied = make(map[string]uint64)
	}
	st.applied[accountID] = seq
}

// rememberLastOnlineCharacter 在成功选角后记录该账号最后选中的角色（好友离线回退）。
// 只认已定案账号（session.accountID 非空，不落共享兜底）且角色确属该账号；写入在账号
// 锁内比较已应用的 generation（单进程序列门控，不是数据库原子 CAS），旧选角的迟到写不会
// 覆盖新值。失败一律 fail-open。
func (s *Service) rememberLastOnlineCharacter(ctx context.Context, session *gameSession, charID uint16) {
	if s == nil || session == nil || charID == 0 {
		return
	}
	accountID := strings.TrimSpace(session.accountID)
	if accountID == "" {
		return
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Account == nil || repositories.Character == nil {
		return
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.character != charID {
		return
	}
	// 选角序列 = bind 时分配的全 bridge 单调 generation：与 remember 的调用时机无关，
	// 所以旧 session 即便更晚调用（或阻塞更久）也拿不到更大的序列。
	seq := identity.generation
	character, found, err := repositories.Character.Load(ctx, strconv.FormatUint(uint64(charID), 10))
	if err != nil || !found || strings.TrimSpace(character.AccountID) != accountID {
		return
	}
	// owner 复核只决定"有没有资格记录"，不承担防旧覆新；顺序由下面账号锁内的 generation 比较保证。
	if !s.boundGameSessionCharacterCurrent(identity) {
		return
	}
	lock := s.lastOnlineCharacter.accountLock(accountID)
	lock.Lock()
	defer lock.Unlock()
	account, found, err := repositories.Account.Load(ctx, accountID)
	if err != nil || !found || strings.TrimSpace(account.AccountID) != accountID {
		return
	}
	if s.lastOnlineCharacter.appliedSeq(accountID) >= seq {
		return
	}
	if err := dnfrepo.SaveAccountMetadataEntry(ctx, repositories.Account, account, lastOnlineCharacterMetadataKey, character.CharacterID, s.gameplayNow()); err != nil {
		s.logGameEvent(session, "game-last-online-character-persist-failed", "char_id", charID, "error", err)
		return
	}
	s.lastOnlineCharacter.markApplied(accountID, seq)
	s.logGameEvent(session, "game-last-online-character-persisted", "char_id", charID)
}
