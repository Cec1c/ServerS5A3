package onlineevent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

const PortedActivityPolicyScope = "server:ported-90-activity-policy:v1"

// 零结束时间表示长期开放；配置只在启动时读取，不因重启重置活动周期。
type ActivityWindow struct {
	Starts time.Time `json:"starts"`
	Ends   time.Time `json:"ends"`
}

func (w ActivityWindow) ActiveAt(now time.Time) bool {
	return !now.IsZero() && (w.Starts.IsZero() || !now.Before(w.Starts)) && (w.Ends.IsZero() || now.Before(w.Ends))
}

type PortedActivityPolicy struct {
	ReturneeOfflineDays       int            `json:"returnee_offline_days"`
	ReturneeDurationDays      int            `json:"returnee_duration_days"`
	ReturneeExperiencePercent int64          `json:"returnee_experience_percent"`
	LevelAiming               ActivityWindow `json:"level_aiming"`
	LaborDay                  ActivityWindow `json:"labor_day"`
	LuckyCharacter            ActivityWindow `json:"lucky_character"`
	LuckyAllProfessions       bool           `json:"lucky_all_professions"`
}

func DefaultPortedActivityPolicy() PortedActivityPolicy {
	return PortedActivityPolicy{ReturneeOfflineDays: 7, ReturneeDurationDays: 30, ReturneeExperiencePercent: 20,
		LaborDay: ActivityWindow{Starts: time.Unix(0, 0).UTC()}, LuckyAllProfessions: true}
}

func (p PortedActivityPolicy) Validate() error {
	if p.ReturneeOfflineDays < 1 || p.ReturneeOfflineDays > 3650 || p.ReturneeDurationDays < 1 || p.ReturneeDurationDays > 3650 || p.ReturneeExperiencePercent < 0 || p.ReturneeExperiencePercent > 1000 {
		return fmt.Errorf("回归活动资格参数无效")
	}
	for _, w := range []ActivityWindow{p.LevelAiming, p.LaborDay, p.LuckyCharacter} {
		if !w.Ends.IsZero() && !w.Ends.After(w.Starts) {
			return fmt.Errorf("活动结束时间必须晚于开始时间")
		}
	}
	if p.LaborDay.Starts.IsZero() {
		return fmt.Errorf("劳动节活动需要固定周期起点")
	}
	return nil
}

// 后续GM可保存完整策略到此scope/state；未配置时使用用户确认的试用规则，不写运行数据库。
func LoadPortedActivityPolicy(ctx context.Context, store repository.SettingsRepository) (PortedActivityPolicy, error) {
	p := DefaultPortedActivityPolicy()
	if store == nil {
		return p, nil
	}
	r, found, err := store.Load(ctx, PortedActivityPolicyScope)
	if err != nil {
		return p, err
	}
	if found {
		d := json.NewDecoder(strings.NewReader(r.Values["state"]))
		d.DisallowUnknownFields()
		if err = d.Decode(&p); err != nil {
			return p, err
		}
		if err = rejectTrailingJSON(d); err != nil {
			return p, err
		}
	}
	return p, p.Validate()
}
