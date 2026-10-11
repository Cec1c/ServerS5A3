package onlineevent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const MonthlyPVFPath = "event/chn_event/chn_dailyattendanceanytimeevent.evt"
const MonthlyMetadataKey = "event_monthly_attendance_v1"
const MonthlyRequiredClears = 2

type MonthlyCatalog struct {
	Daily      [28]AttendancePVFItemReward
	Bonus      [3]AttendancePVFItemReward
	Thresholds [3]uint32
}

func LoadMonthlyCatalog(ctx context.Context, source dnfpvf.Source) (*MonthlyCatalog, error) {
	text, err := source.ReadText(MonthlyPVFPath)
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse(MonthlyPVFPath, text)
	if err != nil {
		return nil, err
	}
	catalog := &MonthlyCatalog{}
	all := make([]AttendancePVFItemReward, 0, 31)
	for _, spec := range []struct {
		name  string
		count int
	}{
		{"daily attendance reward", 28}, {"accumulate attendance reward", 3},
	} {
		values, err := attendanceIntegerSection(doc, spec.name, spec.count*3)
		if err != nil {
			return nil, err
		}
		for i := 0; i < spec.count; i++ {
			key, id, count := values[i*3], values[i*3+1], values[i*3+2]
			if id <= 0 || count <= 0 {
				return nil, fmt.Errorf("monthly reward %s[%d] is invalid", spec.name, i)
			}
			reward := AttendancePVFItemReward{ItemID: id, Count: count}
			if spec.count == 28 {
				if key != int64(i) {
					return nil, fmt.Errorf("monthly daily index=%d want=%d", key, i)
				}
				catalog.Daily[i] = reward
			} else {
				if key <= 0 || key > 28 || (i > 0 && uint32(key) <= catalog.Thresholds[i-1]) {
					return nil, fmt.Errorf("monthly bonus threshold=%d", key)
				}
				catalog.Thresholds[i] = uint32(key)
				catalog.Bonus[i] = reward
			}
			all = append(all, reward)
		}
	}
	paths, err := resolveAttendanceRewardDefinitions(ctx, source, all)
	if err != nil {
		return nil, err
	}
	attachAttendanceDefinitionPaths(catalog.Daily[:], paths)
	attachAttendanceDefinitionPaths(catalog.Bonus[:], paths)
	return catalog, nil
}

type monthlyCycle struct {
	Clears [31]uint8 `json:"clears"`
	Mailed uint32    `json:"mailed"`
	Bonus  uint8     `json:"bonus"`
}

type monthlyState map[string]*monthlyCycle

func monthlyDate(now time.Time) (string, int) {
	date := now.In(ChinaCalendar).Add(-6 * time.Hour)
	return date.Format("2006-01"), date.Day() - 1
}

func loadMonthlyState(account dnfrepo.AccountRecord) (monthlyState, error) {
	state := monthlyState{}
	if raw := account.Metadata[MonthlyMetadataKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil || state == nil {
			return nil, fmt.Errorf("monthly attendance state is invalid")
		}
	}
	for month, cycle := range state {
		date, err := time.Parse("2006-01", month)
		if err != nil || cycle == nil || cycle.Bonus > 7 || cycle.Mailed>>31 != 0 {
			return nil, fmt.Errorf("monthly attendance cycle is invalid")
		}
		days := time.Date(date.Year(), date.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		count := 0
		for day, clears := range cycle.Clears {
			if clears > MonthlyRequiredClears || (day >= days && clears != 0) || (cycle.Mailed&(1<<day) != 0 && clears != MonthlyRequiredClears) {
				return nil, fmt.Errorf("monthly attendance day is invalid")
			}
			if clears == MonthlyRequiredClears {
				count++
			}
		}
		if count > 28 {
			return nil, fmt.Errorf("monthly attendance total exceeds 28")
		}
	}
	return state, nil
}

func monthlyDays(cycle *monthlyCycle) uint32 {
	var count uint32
	if cycle != nil {
		for _, clears := range cycle.Clears {
			if clears == MonthlyRequiredClears {
				count++
			}
		}
	}
	return count
}

// RecordMonthlyClear runs inside the existing idempotent dungeon settlement.
// Delivery is retried from this durable state after a disconnect or restart.
func RecordMonthlyClear(account *dnfrepo.AccountRecord, now time.Time) error {
	if account == nil || account.AccountID == "" || now.IsZero() {
		return ErrOwnerUnavailable
	}
	state, err := loadMonthlyState(*account)
	if err != nil {
		return err
	}
	month, day := monthlyDate(now)
	cycle := state[month]
	if cycle == nil {
		cycle = &monthlyCycle{}
		state[month] = cycle
	}
	if cycle.Clears[day] >= MonthlyRequiredClears || monthlyDays(cycle) >= 28 {
		return nil
	}
	cycle.Clears[day]++
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if account.Metadata == nil {
		account.Metadata = map[string]string{}
	}
	account.Metadata[MonthlyMetadataKey] = string(raw)
	return nil
}

type MonthlySnapshot struct {
	Days    uint32
	Bonus   [3]uint32
	Clears  uint32
	MailIDs []string
}

type MonthlyCommand struct {
	AccountID, CharacterID string
	Now                    time.Time
	ClaimBonus             bool
	Validate               func() bool
	Deliver                func(dnfrepo.MailboxRepository, AttendancePVFItemReward, string) (string, error)
}

func SettleMonthlyAttendance(ctx context.Context, repos dnfrepo.Group, catalog *MonthlyCatalog, cmd MonthlyCommand) (MonthlySnapshot, error) {
	if repos.MailboxAssets == nil || catalog == nil || cmd.AccountID == "" || cmd.CharacterID == "" || cmd.Now.IsZero() || cmd.Deliver == nil {
		return MonthlySnapshot{}, ErrOwnerUnavailable
	}
	var result MonthlySnapshot
	uow, ok := repos.MailboxAssets.(dnfrepo.AccountMailboxUnitOfWork)
	if !ok {
		return MonthlySnapshot{}, ErrOwnerUnavailable
	}
	err := uow.WithinAccountMailbox(ctx, cmd.AccountID, cmd.CharacterID, func(
		accounts dnfrepo.AccountRepository, mailboxes dnfrepo.MailboxRepository,
	) error {
		if cmd.Validate != nil && !cmd.Validate() {
			return ErrOwnerUnavailable
		}
		account, found, err := accounts.Load(ctx, cmd.AccountID)
		if err != nil {
			return err
		}
		if !found || account.AccountID != cmd.AccountID {
			return fmt.Errorf("monthly attendance account missing")
		}
		state, err := loadMonthlyState(account)
		if err != nil {
			return err
		}
		months := make([]string, 0, len(state))
		for month := range state {
			months = append(months, month)
		}
		sort.Strings(months)
		changed := false
		for _, month := range months {
			cycle := state[month]
			rewardIndex := 0
			for day, clears := range cycle.Clears {
				if clears != MonthlyRequiredClears {
					continue
				}
				if cycle.Mailed&(1<<day) == 0 {
					key := MonthlyMetadataKey + ":" + month + ":day:" + strconv.Itoa(day+1)
					mailID, err := cmd.Deliver(mailboxes, catalog.Daily[rewardIndex], key)
					if err != nil {
						return err
					}
					if mailID == "" {
						return fmt.Errorf("monthly daily reward mail missing")
					}
					cycle.Mailed |= 1 << day
					changed = true
					result.MailIDs = append(result.MailIDs, mailID)
				}
				rewardIndex++
			}
		}
		month, day := monthlyDate(cmd.Now)
		if cycle := state[month]; cycle != nil {
			result.Days = monthlyDays(cycle)
			result.Clears = uint32(cycle.Clears[day])
			for i, threshold := range catalog.Thresholds {
				if cycle.Bonus&(1<<i) != 0 {
					result.Bonus[i] = 2
					continue
				}
				if result.Days < threshold {
					continue
				}
				result.Bonus[i] = 1
				if cmd.ClaimBonus {
					key := MonthlyMetadataKey + ":" + month + ":bonus:" + strconv.Itoa(i)
					mailID, err := cmd.Deliver(mailboxes, catalog.Bonus[i], key)
					if err != nil {
						return err
					}
					if mailID == "" {
						return fmt.Errorf("monthly bonus reward mail missing")
					}
					cycle.Bonus |= 1 << i
					changed = true
					result.Bonus[i] = 2
					result.MailIDs = append(result.MailIDs, mailID)
				}
			}
		}
		if !changed {
			return nil
		}
		if cmd.Validate != nil && !cmd.Validate() {
			return ErrOwnerUnavailable
		}
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		return dnfrepo.SaveAccountMetadataEntry(ctx, accounts, account, MonthlyMetadataKey, string(raw), cmd.Now)
	})
	if err != nil {
		return MonthlySnapshot{}, err
	}
	return result, nil
}
