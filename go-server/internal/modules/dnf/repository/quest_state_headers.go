package repository

import "context"

// QuestStateHeaders is read-only; it cannot be saved as a full quest record.
type QuestStateHeaders struct {
	CharacterID      string
	States, Progress map[int64]QuestState
}
type QuestStatesByIDsReader interface {
	LoadQuestStatesByIDs(context.Context, string, ...int64) (QuestStateHeaders, bool, error)
}

func SelectQuestStateHeaders(record QuestRecord, ids ...int64) QuestStateHeaders {
	selectIDs := func(src map[int64]QuestState) map[int64]QuestState {
		var result map[int64]QuestState
		for _, id := range ids {
			if state, ok := src[id]; ok {
				if result == nil {
					result = make(map[int64]QuestState)
				}
				state.Extra = nil
				result[id] = state
			}
		}
		return result
	}
	return QuestStateHeaders{record.CharacterID, selectIDs(record.States), selectIDs(record.Progress)}
}
func LoadQuestStatesByIDs(ctx context.Context, store QuestRepository, id string, ids ...int64) (QuestStateHeaders, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return QuestStateHeaders{}, false, err
	}
	if reader, ok := store.(QuestStatesByIDsReader); ok {
		return reader.LoadQuestStatesByIDs(ctx, id, ids...)
	}
	record, found, err := store.Load(ctx, id)
	if err != nil {
		return QuestStateHeaders{}, false, err
	}
	return SelectQuestStateHeaders(record, ids...), found, nil
}
