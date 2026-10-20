// 本文件持有 op29「买技能/退技能」批次里 refund 条目的遗忘河之水门槛。
//
// 现场缺陷（2026-09-25 20:31，香港 packet_log.txt:925/931 请求、928/934 应答）：
// 操作者把已学技能改成更低等级再点「学习」，请求确实是 refund 条目
// （tree=0 count=1 skill=21 refund=1 delta=1|3 mode=0），服务端回 [00 03]
// （= 缺遗忘河之水），等级没有变化，客户端于是继续显示原等级。
//
// 回退值（改动前行为）：任何带 refund 条目的批次都必须在同一事务里消耗一个
// 遗忘河之水 (item 3)，没有就整体拒绝：ErrSkillRefundConsumableRequired →
// op29 失败码 3（86JP BuySkillService.ExecuteWithRefundConsumable 口径）。
//
// 默认（改动后行为）：refund 条目按请求等级下调并退还差额 SP，不收遗忘河之水。
// 依据是同一份代码基线的**厂商 9.16 运行时实测**（D:\DNF90-test）：
// packet_log.txt:14674 请求 hex=...1900010100（skill=25 refund=1 delta=1），
// 14677 应答 reason=applied buy_skill: char=1 tree=0 entries=1 remainSP=120
// remainTP=0 refund_water=false，随后 14679 才回 class1/op29 成功体
// [01 00 78 00 00 00 01 03 19 00 00 00 00]（remainSP 105→120 = 退 1 级）。
// A21 参考实现同样把「退技能」当合法请求处理（BuySkillService refund 分支），
// 其 PvP 路径 ExecutePvp 本来就不查消耗品。
//
// 一键回退：DNFBRIDGE_SKILL_REFUND_REQUIRE_WATER=1
// （也接受 true/on/yes/water/require/required），恢复「必须有遗忘河之水」。
package skillcmd

import (
	"os"
	"strings"
)

// skillRefundRequireWaterEnv 是退技能门槛的单一开关。
// 默认（未设置 / 任何其它取值）= 不收遗忘河之水；写 1 恢复改动前的强门槛。
const skillRefundRequireWaterEnv = "DNFBRIDGE_SKILL_REFUND_REQUIRE_WATER"

// refundRequiresForgetRiverWater 解析 DNFBRIDGE_SKILL_REFUND_REQUIRE_WATER。
// 只有显式写入真值才回到「必须先有遗忘河之水」的回退档。
func refundRequiresForgetRiverWater() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(skillRefundRequireWaterEnv))) {
	case "1", "true", "on", "yes", "water", "require", "required":
		return true
	default:
		return false
	}
}
