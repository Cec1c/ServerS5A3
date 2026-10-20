package skillcmd

import (
	"fmt"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// formatGrowTypeDetail renders the packed grow_type together with the two facts
// the bare `grow=%d` field could not express, so one log line answers "is this
// character genuinely untransferred, or did the authoritative value go
// missing?":
//
//	grow_present=false            记录没有携带权威键（仓储未水合 / 部分记录）
//	branch=0 awakening=0          合法值：未转职
//	branch=1 awakening=2          合法值：一转分支 1 + 二觉
//
// 线上历史教训：2026-09-24 的 `skill=16 grow=0` 被读成"grow_type 从未落库"，
// 而该角色当时确实未转职（`dnf_characters.grow_type=0`，PVF 也拒绝）。
func formatGrowTypeDetail(packed int, present bool) string {
	value, inDomain := packed, packed >= 0 && packed <= 0xff
	if !present {
		return " grow_present=false grow_out_of_domain=false"
	}
	branch, awakening := dnfrepo.DecodeCharacterGrowType(packed)
	outOfDomain := !inDomain
	if outOfDomain {
		value = packed & 0xff
	}
	return fmt.Sprintf(" grow_present=true branch=%d awakening=%d grow_out_of_domain=%t grow_raw=%d",
		branch, awakening, outOfDomain, value)
}
