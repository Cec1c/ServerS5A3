package tagtournament

import (
	"context"
	"fmt"
	"math"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

const recordsKey = "tag_tournament_records_v1"

var recordZone = time.FixedZone("北京时间", 8*60*60)

// RecordWeek以周四06:00为边界；不复用每天06:00重置的奖励账本。
func RecordWeek(now time.Time) int64 {
	shifted := now.In(recordZone).Add(-6 * time.Hour)
	day := time.Date(shifted.Year(), shifted.Month(), shifted.Day(), 6, 0, 0, 0, recordZone)
	delta := (int(shifted.Weekday()) - int(time.Thursday) + 7) % 7
	return day.AddDate(0, 0, -delta).Unix()
}

type RecordCharacter struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Job  string `json:"job"`
	Grow int64  `json:"grow"`
}

type ClearRecord struct {
	Mode       Mode               `json:"mode"`
	ElapsedMS  uint32             `json:"elapsed_ms"`
	FinishedAt time.Time          `json:"finished_at"`
	Characters [2]RecordCharacter `json:"characters"`
}

type CompletionRecord struct {
	Current ClearRecord  `json:"current"`
	Best    ClearRecord  `json:"best"`
	Weekly  *ClearRecord `json:"weekly,omitempty"`
}

type recordReceipt struct {
	Token string `json:"token"`
	CompletionRecord
}

type recordLedger struct {
	Best    [2]*ClearRecord        `json:"best"`
	Weeks   map[int64]*ClearRecord `json:"weeks"`
	Receipt *recordReceipt         `json:"receipt,omitempty"`
}

func validClearRecord(r *ClearRecord) bool {
	return r != nil && (r.Mode == Normal || r.Mode == Hard) && r.ElapsedMS > 0 && r.ElapsedMS <= math.MaxInt32 &&
		!r.FinishedAt.IsZero() && r.Characters[0].ID != "" && r.Characters[1].ID != "" && r.Characters[0].ID != r.Characters[1].ID
}

func loadRecordLedger(account repo.AccountRecord) (recordLedger, error) {
	var ledger recordLedger
	if raw, ok := account.Metadata[recordsKey]; ok {
		if err := decodeMetadata(raw, &ledger); err != nil {
			return ledger, err
		}
		for i, r := range ledger.Best {
			if r != nil && (!validClearRecord(r) || r.Mode != Mode(i+1)) {
				return ledger, fmt.Errorf("帝国竞技场最快记录损坏")
			}
		}
		if len(ledger.Weeks) > 2 {
			return ledger, fmt.Errorf("帝国竞技场周记录数量无效")
		}
		for week, r := range ledger.Weeks {
			if !validClearRecord(r) || r.Mode != Hard || RecordWeek(r.FinishedAt) != week {
				return ledger, fmt.Errorf("帝国竞技场周记录周期或模式无效")
			}
		}
		if p := ledger.Receipt; p != nil && (p.Token == "" || !validClearRecord(&p.Current) || !validClearRecord(&p.Best) || p.Current.Mode != p.Best.Mode || p.Best.ElapsedMS > p.Current.ElapsedMS) {
			return ledger, fmt.Errorf("帝国竞技场通关记录回执无效")
		}
	}
	if ledger.Weeks == nil {
		ledger.Weeks = make(map[int64]*ClearRecord)
	}
	return ledger, nil
}

// Records只读当前账号的本周困难最佳与分模式历史最佳。缺记录保持空，不伪造
// 当前队伍为最快队伍；历史角色改名/删除不篡改当时成绩的出战职业快照。
func (o *Owner) Records(ctx context.Context, accountID string) (best [2]*ClearRecord, weekly *ClearRecord, err error) {
	if o == nil || accountID == "" {
		return best, nil, fmt.Errorf("帝国竞技场记录缺少账号")
	}
	a, found, err := o.repositories.Account.Load(ctx, accountID)
	if err != nil {
		return best, nil, err
	}
	if !found || a.AccountID != accountID {
		return best, nil, fmt.Errorf("帝国竞技场记录账号不存在")
	}
	l, err := loadRecordLedger(a)
	return l.Best, l.Weeks[RecordWeek(o.now())], err
}

// RecordCompletion只接纳已由服务端Boss死亡推进到终局的Run。已领奖的重复
// 挑战仍可刷新纪录；账号交易内校验挑战凭据，并冻结本场应答供写失败重试。
func (o *Owner) RecordCompletion(ctx context.Context, r *Run) (CompletionRecord, error) {
	if o == nil || r == nil {
		return CompletionRecord{}, fmt.Errorf("帝国竞技场记录缺少挑战")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admission
	if r.catalog != o.catalog || r.phase != Finished || r.startedAt.IsZero() || r.lastTime.Before(r.startedAt) || r.lastTime.After(o.now()) ||
		(a.plan.Mode == Hard && r.cleared != len(a.plan.Encounters)) || (a.plan.Mode == Normal && r.cleared < a.plan.SpecialFrom) {
		return CompletionRecord{}, fmt.Errorf("帝国竞技场记录缺少完整通关及有效计时")
	}
	elapsed := r.lastTime.Sub(r.startedAt).Milliseconds()
	if elapsed <= 0 || elapsed > math.MaxInt32 {
		return CompletionRecord{}, fmt.Errorf("帝国竞技场记录超出现用有符号毫秒范围")
	}
	current := ClearRecord{Mode: a.plan.Mode, ElapsedMS: uint32(elapsed), FinishedAt: r.lastTime.UTC()}
	ids := [2]string{a.characters[0].CharacterID, a.characters[1].CharacterID}
	for i, ch := range a.characters {
		current.Characters[i] = RecordCharacter{ID: ch.CharacterID, Name: ch.Name, Job: ch.Job, Grow: ch.Stats["grow_type"]}
	}
	var result CompletionRecord
	err := o.tx.WithinAccountTradeMailbox(ctx, a.accountID, ids[:], a.recipient, func(accounts repo.AccountRepository, chars repo.CharacterRepository, _ repo.InventoryRepository, _ repo.MailboxRepository) error {
		account, found, err := accounts.Load(ctx, a.accountID)
		if err != nil {
			return err
		}
		if !found || account.AccountID != a.accountID {
			return fmt.Errorf("帝国竞技场记录账号不存在")
		}
		state, err := o.loadDay(account, a.created)
		if err != nil {
			return err
		}
		if state.ActiveRun != a.token || state.ActiveRoster != ids || state.ServiceDay != a.plan.ServiceDay || state.Mode != a.plan.Mode || state.ConfigHash != a.plan.ConfigHash {
			return fmt.Errorf("帝国竞技场记录凭据已被新挑战替换")
		}
		if _, err = loadOwned(ctx, chars, a.accountID, ids); err != nil {
			return err
		}
		ledger, err := loadRecordLedger(account)
		if err != nil {
			return err
		}
		if old := ledger.Receipt; old != nil && old.Token == a.token {
			if old.Current.ElapsedMS != current.ElapsedMS || !old.Current.FinishedAt.Equal(current.FinishedAt) {
				return fmt.Errorf("帝国竞技场同场计时发生变化")
			}
			result = old.CompletionRecord
			return nil
		}
		i := int(current.Mode) - 1
		if old := ledger.Best[i]; old == nil || current.ElapsedMS < old.ElapsedMS {
			copy := current
			ledger.Best[i] = &copy
		}
		week := RecordWeek(current.FinishedAt)
		for old := range ledger.Weeks {
			if old != week && old != time.Unix(week, 0).AddDate(0, 0, -7).Unix() {
				delete(ledger.Weeks, old)
			}
		}
		if current.Mode == Hard {
			if old := ledger.Weeks[week]; old == nil || current.ElapsedMS < old.ElapsedMS {
				copy := current
				ledger.Weeks[week] = &copy
			}
		}
		result = CompletionRecord{Current: current, Best: *ledger.Best[i], Weekly: ledger.Weeks[week]}
		ledger.Receipt = &recordReceipt{Token: a.token, CompletionRecord: result}
		return saveMetadata(ctx, accounts, account, recordsKey, ledger, o.now())
	})
	if err != nil {
		return CompletionRecord{}, err
	}
	return result, nil
}
