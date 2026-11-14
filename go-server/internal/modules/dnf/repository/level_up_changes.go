package repository

import "sync"

// LevelUpChangeFeed is an in-process wakeup after a successful commit. Durable
// pending events remain authoritative, including on overflow or process exit.
// Subscribers must only enqueue work; they must never read SQL or wait on players.
type LevelUpChangeFeed struct {
	mu        sync.RWMutex
	next      uint64
	listeners map[uint64]func(string)
}

func (f *LevelUpChangeFeed) Subscribe(fn func(string)) func() {
	if f == nil || fn == nil {
		return func() {}
	}
	f.mu.Lock()
	if f.listeners == nil {
		f.listeners = make(map[uint64]func(string))
	}
	f.next++
	id := f.next
	f.listeners[id] = fn
	f.mu.Unlock()
	return func() { f.mu.Lock(); delete(f.listeners, id); f.mu.Unlock() }
}

func (f *LevelUpChangeFeed) Publish(id string) {
	if f == nil || id == "" {
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, fn := range f.listeners {
		fn(id)
	}
}
