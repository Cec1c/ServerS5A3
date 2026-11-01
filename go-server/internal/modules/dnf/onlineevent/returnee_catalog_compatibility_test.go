package onlineevent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestReturneeNativeCurrentConditionsAndUnknownRejection(t *testing.T) {
	var text strings.Builder
	text.WriteString("[mission list]\n")
	conditions := []string{"move village", "join guild", "clear quest", "level up count", "disjoint item", "use cube skill", "dimension dungeon clear", "mission clear count", "mission clear count", "mission clear count"}
	for i, condition := range conditions {
		fmt.Fprintf(&text, "[mission]\n[index]\n%d\n[condition]\n[%s]\n1\n", i+1, condition)
		if condition == "move village" {
			text.WriteString("[condition value]\n-1\n")
		}
		text.WriteString("[/condition]\n[reward item]\n100 1\n[/mission]\n")
	}
	text.WriteString("[/mission list]\n[mission group]\n[mission]\n[group]\n1\n[mission list]\n1 2 3 4 5 6 7 8 9 10\n[/mission list]\n[/mission]\n[/mission group]\n[reward mail]\n`测试邮件` `测试正文`\n")
	source := attendanceCatalogTestSource{"etc/seriawelcome.etc": text.String()}
	c, err := LoadReturneeCatalog(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.PendingConditions) != 0 || c.Missions[0].ConditionValue != -1 {
		t.Fatalf("条件差异未保留：%+v", c)
	}
	now := time.Date(2026, 10, 29, 7, 0, 0, 0, ChinaCalendar)
	state := ReturneeState{Started: now, ExpiresAt: now.Add(ReturneeDuration)}
	state.Observe(c, "1", ReturneeObservation{Guild: true, Quests: 100}, now, false)
	if state.Progress[1] != 1 || state.Progress[0] != 0 || state.Progress[5] != 0 || state.Progress[6] != 0 {
		t.Fatalf("公会可完成，访问/无色/异界不能凭空完成：%v", state.Progress)
	}
	bad := attendanceCatalogTestSource{"etc/seriawelcome.etc": strings.ReplaceAll(text.String(), "use cube skill", "unknown condition")}
	if _, err := LoadReturneeCatalog(context.Background(), bad); err == nil {
		t.Fatal("未知条件被静默接受")
	}
}
