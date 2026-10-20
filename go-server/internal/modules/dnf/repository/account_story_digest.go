// 本文件定义账号级"剧情回顾(story digest)已看过"记录。
//
// 客户端在选角后会消费 class0/op1378 的 story digest last-level 通知，并把
// 该值与该角色当前等级各自选中的剧情回顾段比较：只要认为"落后"就每次登录都
// 强制播放一遍（操作者 2026-09-25 现象）。角色自己的
// story_digest_last_level 只在客户端上报"开始播放"(op1445) 时才推进，因此
// 跳过动画 / GM 拔高等级都会让它永远落后。
//
// 账号级记录就是为了兜住这两种情况：一旦账号下任意角色上报过一次，整个账号
// 就按"已看过"处理，语义是单调不减的 max，永不回退。
package repository

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	// AccountStoryDigestViewedLevelMetadataKey 是账号元数据(dnf_account_metadata)
	// 里的账号级标量：该账号已经接受过的剧情回顾最高等级。
	AccountStoryDigestViewedLevelMetadataKey = "story_digest_viewed_level"
)

var (
	ErrAccountStoryDigestAdvanceUnavailable = errors.New("dnf account story digest advance is unavailable")
	ErrAccountStoryDigestAccountMissing     = errors.New("dnf account story digest account is missing")
)

// AccountStoryDigestViewedLevelAdvancer owns the monotonic durability boundary
// for the account-level story-digest record. Implementations must never lower
// the stored value when requests race or replay.
type AccountStoryDigestViewedLevelAdvancer interface {
	AdvanceStoryDigestViewedLevel(context.Context, string, uint32) error
}

// AccountStoryDigestViewedLevel 读取账号记录里的账号级"已看过"等级。
// 缺失、空串或非法值都按 0（=从未看过）处理，与加这个记录之前的行为一致。
func AccountStoryDigestViewedLevel(account AccountRecord) uint32 {
	return ParseAccountStoryDigestViewedLevel(account.Metadata[AccountStoryDigestViewedLevelMetadataKey])
}

// ParseAccountStoryDigestViewedLevel 解析账号元数据里的十进制等级文本。
func ParseAccountStoryDigestViewedLevel(value string) uint32 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(parsed)
}

// FormatAccountStoryDigestViewedLevel 把等级写成账号元数据的十进制文本。
func FormatAccountStoryDigestViewedLevel(level uint32) string {
	return strconv.FormatUint(uint64(level), 10)
}

// AdvanceAccountStoryDigestViewedLevel 把账号级"已看过"等级推进到 level。
// 语义是 GREATEST(旧值, level)：幂等、可重入、任何并发或重放都不会让它回退。
func AdvanceAccountStoryDigestViewedLevel(
	ctx context.Context,
	repo AccountRepository,
	accountID string,
	level uint32,
) error {
	if repo == nil {
		return ErrAccountStoryDigestAdvanceUnavailable
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return ErrAccountStoryDigestAccountMissing
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if advancer, ok := repo.(AccountStoryDigestViewedLevelAdvancer); ok {
		return advancer.AdvanceStoryDigestViewedLevel(ctx, accountID, level)
	}
	// 兜底：底层只有整条 Save 时的读改写。仍然保持单调，不降级写。
	account, found, err := repo.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found {
		return ErrAccountStoryDigestAccountMissing
	}
	if AccountStoryDigestViewedLevel(account) >= level {
		return nil
	}
	return SaveAccountMetadataEntry(
		ctx,
		repo,
		account,
		AccountStoryDigestViewedLevelMetadataKey,
		FormatAccountStoryDigestViewedLevel(level),
		time.Now().UTC(),
	)
}
