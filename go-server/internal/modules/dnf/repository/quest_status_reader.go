package repository

import "context"

// QuestStatusReader reads one persisted quest status without loading the full
// quest history.
type QuestStatusReader interface {
	LoadQuestStatus(context.Context, string, int64) (string, bool, error)
}
