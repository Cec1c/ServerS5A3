package repository

import "context"

// CharacterQuestStatusReader 是可选的实时只读能力。只返回两个任务投影的状态，
// 不返回可保存的局部QuestRecord；found表示角色存在，缺失时调用者保留原完整路径。
// 状态保持原字符串，不由数据库校对规则判断是否active，不跨请求缓存。
type CharacterQuestStatusReader interface {
	LoadCharacterQuestStatuses(context.Context, string) (statuses []string, found bool, err error)
}

// QuestActivity只携带实时编号及原始状态，不能作为局部QuestRecord保存。
type QuestActivity struct {
	QuestID int64
	Status  string
}

// CharacterQuestActivityReader包含States和Progress两个投影。资格由Go及PVF判断，
// 不读历史Extra，不跨死亡请求缓存；found仍表示角色存在。
type CharacterQuestActivityReader interface {
	LoadCharacterQuestActivities(context.Context, string) (activities []QuestActivity, found bool, err error)
}
