package quest

import dnfrepo "longheng.io/server/internal/modules/dnf/repository"

// IsCompleted 使用现有任务完成状态判据，兼容旧 States 与 Progress 两种存储。
func IsCompleted(record dnfrepo.QuestRecord, questID int64) bool {
	if questID <= 0 {
		return false
	}
	return isCompletedQuestStatus(record.States[questID].Status) || isCompletedQuestStatus(record.Progress[questID].Status)
}
