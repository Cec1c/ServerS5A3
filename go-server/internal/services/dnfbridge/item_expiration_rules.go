package dnfbridge

import (
	"errors"
	"fmt"
	"math"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var errCurrentPVFUsablePeriodOutOfRange = errors.New("dnf runtime PVF usable period exceeds current item wire")

func currentPVFExpirationUnix(expirationDate time.Time) uint32 {
	if expirationDate.IsZero() || expirationDate.Unix() <= 0 {
		return 0
	}
	return sceneInventoryUint32FromInt64(expirationDate.Unix())
}

// currentPVFItemDefinitionWithUsablePeriodAt resolves the relative PVF
// [usable period] declaration on its own: it is the historical body of the
// grant-time helper, without the retired-date policy. Callers that must judge
// "是不是**PVF 自己写的那个绝对日期**已经过期"（例如子奖励要不要继承来源实例的活期限）
// use this form, because the policy would otherwise have moved that date forward
// already.
func currentPVFItemDefinitionWithUsablePeriodAt(
	definition dungeonDropItemDefinition,
	now time.Time,
) (dungeonDropItemDefinition, error) {
	if (definition.Kind != dungeonDropItemStackable && definition.Kind != dungeonDropItemEquipment) ||
		definition.UsablePeriodDays <= 0 {
		return definition, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nowUnix := now.Unix()
	if nowUnix <= 0 || nowUnix > math.MaxUint32 ||
		definition.UsablePeriodDays > (math.MaxUint32-nowUnix)/int64((24*time.Hour)/time.Second) {
		return dungeonDropItemDefinition{}, fmt.Errorf(
			"%w: item=%d days=%d now=%d",
			errCurrentPVFUsablePeriodOutOfRange,
			definition.ItemID,
			definition.UsablePeriodDays,
			nowUnix,
		)
	}
	expireUnix := nowUnix + definition.UsablePeriodDays*int64((24*time.Hour)/time.Second)
	definition.ExpirationDate = time.Unix(expireUnix, 0).UTC()
	return definition, nil
}

// currentPVFItemDefinitionForGrantAt resolves a relative PVF [usable period]
// exactly once, when a new item instance is granted.  Reconciliation must not
// call this helper: recomputing it on login would silently extend the item.
// Runtime PVF uses days for this field; the current EXE persists the resulting
// absolute Unix second in the 0x77 row.
//
// It also applies the operator's retired-item policy (见
// currentPVFRetiredExpirationRenewalDate)：PVF 声明的**绝对** [expiration date]
// 已经过去时，不再原样返回那个死日期，而是统一续期到 2028-01-01。
// 每个发放路径（掉落拾取/翻牌/任务/盒子/点券套餐/邮件领取）都从这里取定义，
// 所以"产出即过期"的那批退役道具在落地时就已经是活的了。
// 相对 [usable period] 的解析结果（now + N 天）永远是活的，策略不会碰它。
func currentPVFItemDefinitionForGrantAt(
	definition dungeonDropItemDefinition,
	now time.Time,
) (dungeonDropItemDefinition, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if definition.DailyDeleteItem {
		definition.ExpirationDate = itemexpiration.NextDailyDeleteAt(now)
		return definition, nil
	}
	resolved, err := currentPVFItemDefinitionWithUsablePeriodAt(definition, now)
	if err != nil {
		return dungeonDropItemDefinition{}, err
	}
	resolved.ExpirationDate = currentPVFRenewedRetiredExpirationDate(resolved.ExpirationDate, now)
	return resolved, nil
}

// currentPVFItemDefinitionForNestedRewardGrantAt resolves the reward's own
// runtime PVF period first. If the reward only has an already-expired static
// event date, but the package/booster instance that produced it carries a
// future per-instance deadline, the child reward inherits that live deadline.
// This keeps newly opened historical event packages from creating already
// expired inner boxes while still rejecting old source items that have no real
// instance deadline.
//
// 注意这里判"子奖励自己是否已经过期"用的是 currentPVFItemDefinitionWithUsablePeriodAt
// 的**策略之前**结果：来源实例带着更晚的活期限（例如 2037 到期的历史礼包）时，
// 既有行为就是让子奖励继承那个更晚的期限，续期策略不许把这条继承挤掉。
// 只有在来源本身也没有活期限时，才轮到续期地平线兜底 —— 老代码在那一步会把
// 早已过去的日期原样返回，开出来的箱子一出生就"已超过使用期限"。
func currentPVFItemDefinitionForNestedRewardGrantAt(
	definition dungeonDropItemDefinition,
	sourceStack dnfrepo.ItemStack,
	now time.Time,
) (dungeonDropItemDefinition, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if definition.DailyDeleteItem {
		return currentPVFItemDefinitionForGrantAt(definition, now)
	}
	selfResolved, err := currentPVFItemDefinitionWithUsablePeriodAt(definition, now)
	if err != nil {
		return dungeonDropItemDefinition{}, err
	}
	grantDefinition := selfResolved
	grantDefinition.ExpirationDate = currentPVFRenewedRetiredExpirationDate(selfResolved.ExpirationDate, now)
	if selfResolved.ExpirationDate.IsZero() || now.Before(selfResolved.ExpirationDate) {
		return grantDefinition, nil
	}
	sourceExpire := currentItemListStackExpire(sourceStack)
	if sourceExpire == 0 || uint64(sourceExpire) <= uint64(now.Unix()) {
		return grantDefinition, nil
	}
	grantDefinition.ExpirationDate = time.Unix(int64(sourceExpire), 0).UTC()
	return grantDefinition, nil
}

func currentItemStackExpirationMatches(stack dnfrepo.ItemStack, expirationDate time.Time) bool {
	actual := currentItemListStackExpire(stack)
	if expirationDate.IsZero() {
		return actual == 0
	}
	expected := expirationDate.Unix()
	return expected > 0 && expected <= math.MaxUint32 && actual == uint32(expected)
}

// currentPVFStackableUsePeriodSeconds mirrors the current client's 0x77-row
// reader. For item model types 26..29, row+0x0B is copied into the item's
// remaining-use-period field and counted down in seconds. The wire field is a
// u16, so a future absolute PVF date is renewed to the largest representable
// session horizon until the final 65,535 seconds.
func currentPVFStackableUsePeriodSeconds(expirationDate time.Time, now time.Time) uint16 {
	if expirationDate.IsZero() {
		return 0
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	remaining := expirationDate.Sub(now)
	if remaining <= 0 {
		return 0
	}
	seconds := int64(remaining / time.Second)
	if remaining%time.Second != 0 {
		seconds++
	}
	if seconds > math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(seconds)
}
