package repository

import (
	"math"
	"strconv"
	"time"
)

const ReturneeCubeSkillCountKey = "seria_returnee_cube_skill_count"
const ReturneeDimensionCountKey = "seria_returnee_dimension_clear_count"
const ReturneeVillagePrefix = "seria_returnee_village:"

// 新手与回归共用权威任务计数；两类身份仍各有独立资格字段。
func SeriaWelcomeActiveAt(ch CharacterRecord, at time.Time) bool {
	return ch.Stats["is_return_user"] != 0 && ch.Stats["seria_returnee_active_until"] > at.Unix() || ch.Stats["seria_newbie_active_until"] > at.Unix()
}

// 仅在原成功事务中调用。资格过期、失败及已拦截重放不能产生回归进度。
func AddReturneeCounter(ch *CharacterRecord, key string, at time.Time) {
	if ch == nil || !SeriaWelcomeActiveAt(*ch, at) {
		return
	}
	if key != ReturneeCubeSkillCountKey && key != ReturneeDimensionCountKey {
		return
	}
	ch.Stats[key] = min(max(ch.Stats[key], 0), math.MaxInt64-1) + 1
}

// 记录服务端已验证的非赛丽亚城镇落点；与位置同保存、同回滚，不接收客户端任务进度。
func RecordReturneeVillage(ch *CharacterRecord, townID int64, at time.Time) {
	if ch == nil || townID <= 0 || !SeriaWelcomeActiveAt(*ch, at) {
		return
	}
	ch.Stats[ReturneeVillagePrefix+strconv.FormatInt(townID, 10)] = at.UnixNano()
}
