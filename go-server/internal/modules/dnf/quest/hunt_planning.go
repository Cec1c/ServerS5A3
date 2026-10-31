package quest

import (
	"context"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"sort"
)

// HuntPlanningDependencyIDs retains exactly the reachable quest-clear graph.
// Both groups are inspected, matching the existing linked activation and
// parent synchronization semantics even when States shadows Progress for kills.
func (c *Catalog) HuntPlanningDependencyIDs(record dnfrepo.QuestRecord) []int64 {
	if c == nil {
		return nil
	}
	var queue []int64
	for _, group := range []map[int64]dnfrepo.QuestState{record.States, record.Progress} {
		for id, state := range group {
			if isActiveQuestStatus(state.Status) {
				queue = append(queue, id)
			}
		}
	}
	visited := make(map[int64]bool)
	dependencies := make(map[int64]bool)
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if visited[id] {
			continue
		}
		visited[id] = true
		definition, found := c.Find(id)
		if !found || !questClearDefinition(definition) {
			continue
		}
		for _, child := range definition.IntData {
			if child <= 0 {
				continue
			}
			dependencies[child] = true
			if !visited[child] {
				queue = append(queue, child)
			}
		}
	}
	result := make([]int64, 0, len(dependencies))
	for id := range dependencies {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (c *Catalog) LoadHuntPlanning(ctx context.Context, r dnfrepo.QuestRepository, id string) (dnfrepo.QuestRecord, bool, error) {
	if c == nil {
		return dnfrepo.QuestRecord{}, false, ErrCatalogEmpty
	}
	return dnfrepo.LoadQuestDependentPlanning(ctx, r, id, c, c.HuntPlanningDependencyIDs)
}
