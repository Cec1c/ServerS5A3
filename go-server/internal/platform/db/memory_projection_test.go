package db

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

type projectionTestValue struct {
	Key    string
	Value  int
	Mirror int
	Blob   []int
}

func projectionTestKey(value projectionTestValue) string { return value.Key }

func newProjectionTestStore(clones *atomic.Int64) *MemoryStore[projectionTestValue] {
	return NewMemoryStore(projectionTestKey, func(value projectionTestValue) projectionTestValue {
		clones.Add(1)
		value.Blob = append([]int(nil), value.Blob...)
		return value
	})
}

func TestProjectMemoryValueSkipsCloneWhileLoadStillClones(t *testing.T) {
	ctx := context.Background()
	var clones atomic.Int64
	store := newProjectionTestStore(&clones)
	want := projectionTestValue{Key: "a", Value: 7, Mirror: 14, Blob: []int{1, 2, 3}}
	if err := store.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	if clones.Load() != 1 {
		t.Fatalf("save clone count = %d, want 1", clones.Load())
	}

	projected, found, err := ProjectMemoryValue(ctx, store, "a", func(value projectionTestValue) int {
		return value.Value
	})
	if err != nil || !found || projected != want.Value {
		t.Fatalf("projection = (%d, %t, %v), want (%d, true, nil)", projected, found, err, want.Value)
	}
	if clones.Load() != 1 {
		t.Fatalf("projection invoked clone function: clones=%d", clones.Load())
	}

	loaded, found, err := store.Load(ctx, "a")
	if err != nil || !found {
		t.Fatalf("Load found=%t err=%v", found, err)
	}
	if clones.Load() != 2 {
		t.Fatalf("Load clone count = %d, want 2", clones.Load())
	}
	loaded.Blob[0] = 99
	loadedAgain, _, _ := store.Load(ctx, "a")
	if !reflect.DeepEqual(loadedAgain.Blob, want.Blob) {
		t.Fatalf("Load did not return a detached clone: %v", loadedAgain.Blob)
	}
}

func TestProjectMemoryValueMissingAndCanceled(t *testing.T) {
	var clones atomic.Int64
	store := newProjectionTestStore(&clones)
	callbackCalls := 0
	project := func(projectionTestValue) int {
		callbackCalls++
		return 1
	}

	got, found, err := ProjectMemoryValue(context.Background(), store, "missing", project)
	if err != nil || found || got != 0 || callbackCalls != 0 || clones.Load() != 0 {
		t.Fatalf("missing projection=(%d,%t,%v) calls=%d clones=%d", got, found, err, callbackCalls, clones.Load())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, found, err = ProjectMemoryValue(ctx, store, "missing", project)
	if !errors.Is(err, context.Canceled) || found || got != 0 || callbackCalls != 0 || clones.Load() != 0 {
		t.Fatalf("canceled projection=(%d,%t,%v) calls=%d clones=%d", got, found, err, callbackCalls, clones.Load())
	}
}

func TestProjectMemoryValueConcurrentSaveIsConsistent(t *testing.T) {
	ctx := context.Background()
	var clones atomic.Int64
	store := newProjectionTestStore(&clones)
	if err := store.Save(ctx, projectionTestValue{Key: "a", Value: 0, Mirror: 0}); err != nil {
		t.Fatal(err)
	}

	type view struct {
		value  int
		mirror int
	}
	writerErr := make(chan error, 2)
	readerErr := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for value := 1; value <= 2000; value++ {
			if err := store.Save(ctx, projectionTestValue{Key: "a", Value: value, Mirror: value * 2}); err != nil {
				writerErr <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for iteration := 0; iteration < 4000; iteration++ {
			projected, found, err := ProjectMemoryValue(ctx, store, "a", func(value projectionTestValue) view {
				return view{value: value.Value, mirror: value.Mirror}
			})
			if err != nil {
				readerErr <- err
				return
			}
			if !found || projected.mirror != projected.value*2 {
				readerErr <- errors.New("inconsistent projection observed")
				return
			}
		}
	}()
	wg.Wait()
	select {
	case err := <-writerErr:
		t.Fatal(err)
	case err := <-readerErr:
		t.Fatal(err)
	default:
	}
}
