package repository

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// 各业务只在原成功事务中调用；保存服务日桶，最多保留128天供不超过100天的赛季读取。
// 没有客户端写进度入口，失败事务和重放拒绝不会留下计数。
const BattlePassCounterPrefix = "battlepass2:"

func AddBattlePassCounter(ch *CharacterRecord, kind uint32, param int64, amount int64, at time.Time) {
	if ch == nil || amount <= 0 || kind > 45 || param < -1 || at.IsZero() {
		return
	}
	if ch.Stats == nil {
		ch.Stats = map[string]int64{}
	}
	day := ActivityDay(at)
	cutoff := ActivityDay(at.AddDate(0, 0, -128))
	for k := range ch.Stats {
		if strings.HasPrefix(k, BattlePassCounterPrefix) && len(k) > len(BattlePassCounterPrefix)+10 && k[len(BattlePassCounterPrefix):len(BattlePassCounterPrefix)+10] < cutoff {
			delete(ch.Stats, k)
		}
	}
	key := fmt.Sprintf("%s%s:%d:%d", BattlePassCounterPrefix, day, kind, param)
	old := max(ch.Stats[key], 0)
	ch.Stats[key] = old + min(amount, math.MaxInt64-old)
}
func ReadBattlePassCounterKey(key string) (day string, kind uint32, param int64, ok bool) {
	if !strings.HasPrefix(key, BattlePassCounterPrefix) {
		return
	}
	parts := strings.Split(strings.TrimPrefix(key, BattlePassCounterPrefix), ":")
	if len(parts) != 3 {
		return
	}
	t, e := time.Parse("2006-01-02", parts[0])
	if e != nil || t.Format("2006-01-02") != parts[0] {
		return
	}
	k, e := strconv.ParseUint(parts[1], 10, 32)
	if e != nil || k > 45 {
		return
	}
	p, e := strconv.ParseInt(parts[2], 10, 64)
	if e != nil || p < -1 {
		return
	}
	return parts[0], uint32(k), p, true
}
