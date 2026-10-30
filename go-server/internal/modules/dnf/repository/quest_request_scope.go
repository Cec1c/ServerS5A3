package repository

import (
	"context"
	"reflect"
)

type questPlanningScopeKey struct{}

// A scope belongs to one serial request and one repository/character pair.
// It must not be retained by a session or used concurrently. Transactional
// delta writers still validate before-images against the database.
type questPlanningScope struct {
	repository QuestRepository
	id         string
	record     QuestRecord
	found      bool
	loaded     bool
}

func WithQuestPlanningScope(ctx context.Context, r QuestRepository, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil || !reflect.TypeOf(r).Comparable() {
		return ctx
	}
	return context.WithValue(ctx, questPlanningScopeKey{}, &questPlanningScope{repository: r, id: id})
}

func planningScope(ctx context.Context, r QuestRepository, id string) *questPlanningScope {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(questPlanningScopeKey{}).(*questPlanningScope)
	if s == nil || s.id != id || r == nil || !reflect.TypeOf(r).Comparable() || s.repository != r {
		return nil
	}
	return s
}
