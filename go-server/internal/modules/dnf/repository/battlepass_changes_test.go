package repository

import "testing"

func TestBattlePassChangeFeedAccountIsolation(t *testing.T) {
	f := NewBattlePassChangeFeed()
	a, b := 0, 0
	stop := f.Subscribe("a", func(e BattlePassChange) { a++ })
	f.Subscribe("b", func(e BattlePassChange) { b++ })
	f.Publish(BattlePassChange{AccountID: "a"})
	if a != 1 || b != 0 {
		t.Fatal("跨账号发布", a, b)
	}
	stop()
	stop()
	f.Publish(BattlePassChange{AccountID: "a"})
	if a != 1 || f.Interested("a") {
		t.Fatal("取消后仍订阅")
	}
}
