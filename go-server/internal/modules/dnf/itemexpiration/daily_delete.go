package itemexpiration

import (
	"fmt"
	"strconv"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const DailyDeleteDeadlineKey = "daily_delete_at"

var dailyDeleteLocation = time.FixedZone("CST", 8*60*60)

// NextDailyDeleteAt returns the first 06:00 service-day boundary strictly
// after acquisition. A material obtained at exactly 06:00 belongs to the new
// day. UTC persistence does not depend on the server operating-system zone.
func NextDailyDeleteAt(now time.Time) time.Time {
	local := now.In(dailyDeleteLocation)
	boundary := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, dailyDeleteLocation)
	if !now.Before(boundary) {
		boundary = boundary.AddDate(0, 0, 1)
	}
	return boundary.UTC()
}

// DailyDeleteBoundaryAtOrAfter 把任意时刻吸附到"服务日 06:00 边界"：
// 返回不早于 deadline 的第一个 06:00（同一时区口径，见 dailyDeleteLocation）。
//
// 存在的理由（2026-11-07）：PVF 的 [usable period] 是**相对天数**，
// 早期实现直接算 `now + N×24h`，落到秒 —— 于是同一天连开的同一个限期道具
// 每次得到不同的绝对到期时间，堆叠合并判据（要求过期时间整数秒相等）永远失败，
// 背包里同一个道具散成十几格（现场：赛丽亚的幸运开出 10008064，8 行、8 个不同秒）。
//
// 吸附到 06:00 之后，同一服务日发放的同种限期道具得到**完全相同**的到期时间，
// 既能合并，也符合"到期日 06:00 删除"的运营口径。
// 代价是实际有效期从"精确 N×24 小时"变成"第 N 天所在服务日的 06:00"，
// 即最多比原来多 24 小时、最少相同 —— 不会比原语义更短。
func DailyDeleteBoundaryAtOrAfter(deadline time.Time) time.Time {
	local := deadline.In(dailyDeleteLocation)
	boundary := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, dailyDeleteLocation)
	if boundary.Before(deadline) {
		boundary = boundary.AddDate(0, 0, 1)
	}
	return boundary.UTC()
}

// ReconcileDailyDelete is called only for a definition whose real PVF has
// [daily delete item] 1. Existing persistent deadlines never move on relogin.
// Legacy instances lack an acquisition timestamp; first observation assigns
// the next boundary rather than guessing that they were acquired yesterday.
func ReconcileDailyDelete(stack dnfrepo.ItemStack, now time.Time) (dnfrepo.ItemStack, bool, bool, error) {
	deadline := NextDailyDeleteAt(now)
	if raw := stack.Extra[DailyDeleteDeadlineKey]; raw != "" {
		unix, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || unix <= 0 || unix > 1<<32-1 {
			return stack, false, false, fmt.Errorf("invalid daily-delete deadline item=%d", stack.ItemID)
		}
		deadline = time.Unix(unix, 0).UTC()
		if !now.Before(deadline) {
			return stack, false, true, nil
		}
	}
	cloned := make(map[string]string, len(stack.Extra)+3)
	for key, value := range stack.Extra {
		cloned[key] = value
	}
	changed := false
	value := strconv.FormatInt(deadline.Unix(), 10)
	for _, key := range []string{DailyDeleteDeadlineKey, "expire_time", "expire_unix"} {
		if cloned[key] != value {
			cloned[key] = value
			changed = true
		}
	}
	if !stack.ExpireAt.Equal(deadline) {
		stack.ExpireAt = deadline
		changed = true
	}
	stack.Extra = cloned
	return stack, changed, false, nil
}
