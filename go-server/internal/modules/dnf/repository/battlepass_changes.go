package repository

import "sync"

// BattlePassChange只携带已提交的计数；观察者不做SQL或网络操作。
type BattlePassChange struct {
	AccountID, CharacterID string
	Counters               map[string]int64
	Account, Fatigue       bool
}
type BattlePassChangeFeed struct {
	mu        sync.RWMutex
	serial    uint64
	listeners map[string]map[uint64]func(BattlePassChange)
}

func NewBattlePassChangeFeed() *BattlePassChangeFeed {
	return &BattlePassChangeFeed{listeners: make(map[string]map[uint64]func(BattlePassChange))}
}
func (f *BattlePassChangeFeed) Interested(account string) bool {
	if f == nil {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.listeners[account]) > 0
}
func (f *BattlePassChangeFeed) Subscribe(account string, fn func(BattlePassChange)) func() {
	if f == nil || account == "" || fn == nil {
		return func() {}
	}
	f.mu.Lock()
	f.serial++
	id := f.serial
	if f.listeners[account] == nil {
		f.listeners[account] = make(map[uint64]func(BattlePassChange))
	}
	f.listeners[account][id] = fn
	f.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			delete(f.listeners[account], id)
			if len(f.listeners[account]) == 0 {
				delete(f.listeners, account)
			}
			f.mu.Unlock()
		})
	}
}
func (f *BattlePassChangeFeed) Publish(e BattlePassChange) {
	if f == nil {
		return
	}
	f.mu.RLock()
	list := make([]func(BattlePassChange), 0, len(f.listeners[e.AccountID]))
	for _, fn := range f.listeners[e.AccountID] {
		list = append(list, fn)
	}
	f.mu.RUnlock()
	for _, fn := range list {
		fn(e)
	}
}
