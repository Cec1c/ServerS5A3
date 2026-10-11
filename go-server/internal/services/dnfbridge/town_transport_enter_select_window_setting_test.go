package dnfbridge

import (
	"os"
	"testing"
	"time"
)

// 本文件补两项与实现耦合的判据（引用实现里的新符号，所以与
// town_transport_enter_select_window_test.go 分开放 —— 那个文件刻意只用字面量开关名，
// 为的是"在改动前的树上照样编译并 FAIL"这条真伪判据）：
//
//  1. 开关 DNFBRIDGE_TOWN_TRANSPORT_ENTER_SELECT_WINDOW_MS 的逐档语义
//     （空/非法 = 修复档 500 ms；正整数 = 该毫秒数；legacy/off/false/no = 回退档 0）；
//     默认 500 ms 的取证依据 = "提交 -> op15"实测最小 2054.7 ms（4 倍以上余量），
//     见 `_dist/enter-select-latch-arming-investigation.md` §3；
//  2. 布防点（town_move.go）在两个 transport 入口上都同时记下挂账时刻，
//     且普通城镇过图不布防、也不留时刻（否则窗口判据会把陈旧时刻当成本次布防）。

func TestTownTransportEnterSelectWindowSetting(t *testing.T) {
	tests := []struct {
		name  string
		value string
		unset bool
		empty bool
		want  time.Duration
	}{
		{name: "unset defaults to the fix (500ms)", unset: true, want: 500 * time.Millisecond},
		{name: "empty value defaults to the fix (500ms)", empty: true, want: 500 * time.Millisecond},
		{name: "positive integer picks that millisecond window", value: "200", want: 200 * time.Millisecond},
		{name: "1ms is the falsification-test window", value: "1", want: time.Millisecond},
		{name: "3000ms matches the sibling backToVillage window", value: "3000", want: 3 * time.Second},
		// 默认值 500 ms 的取证依据：全部 5 个带毫秒时间戳的"提交 -> op15"间隔最小 2054.7 ms，
		// 唯一带时间戳的吞包样本是 2361.3 ms（见 _dist/enter-select-latch-arming-investigation.md §3）。
		// 也就是说 500 ms 距最近的实测样本有 4 倍以上余量 —— 历史 20/20 样本都不会被它吞掉。
		{name: "the shipped default stays 4x below the closest observed commit->op15", value: "500", want: 500 * time.Millisecond},
		{name: "invalid text defaults to the fix", value: "abc", want: 500 * time.Millisecond},
		{name: "zero is not a positive integer so it defaults to the fix", value: "0", want: 500 * time.Millisecond},
		{name: "negative defaults to the fix", value: "-5", want: 500 * time.Millisecond},
		{name: "legacy is the unbounded-swallow rollback", value: "legacy", want: 0},
		{name: "off is the rollback", value: "off", want: 0},
		{name: "false is the rollback", value: "false", want: 0},
		{name: "no is the rollback", value: "no", want: 0},
		{name: "rollback literals are case-insensitive", value: "LeGaCy", want: 0},
		{name: "rollback literals are trimmed", value: "  off  ", want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			switch {
			case test.unset:
				// t.Setenv 先把原值登记进 cleanup，再显式删掉 ⇒ 本用例真的在"未设置"下跑，
				// 结束后由 cleanup 还原（Go 没有 t.Unsetenv）。
				t.Setenv(townTransportEnterSelectWindowEnv, "")
				if err := os.Unsetenv(townTransportEnterSelectWindowEnv); err != nil {
					t.Fatal(err)
				}
			case test.empty:
				t.Setenv(townTransportEnterSelectWindowEnv, "")
			default:
				t.Setenv(townTransportEnterSelectWindowEnv, test.value)
			}
			if got := currentTownTransportEnterSelectWindow(); got != test.want {
				t.Fatalf("currentTownTransportEnterSelectWindow() = %v, want %v (env=%q unset=%t empty=%t)",
					got, test.want, test.value, test.unset, test.empty)
			}
		})
	}
}

func TestTownTransportGateArmingRecordsArmedAtOnBothTransportEntryPoints(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{name: "world map / town transfer station", body: buildTownMapStationRequest(38, 0, 1519, 489, 5)},
		{name: "teleport array", body: buildTownTeleportArrayRequest(38, 0, 1519, 489, 5)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, session, _, _ := townTransportGateFixture(t)
			service.markCurrentTownSceneReady(session)
			before := time.Now()
			if err := service.handleTownSetUserArea(session, test.body); err != nil {
				t.Fatal(err)
			}
			if !session.townTransportEnterSelectPending {
				t.Fatalf("op36 body=%x 必须布防城镇 transport 挂账（town_move.go）", test.body)
			}
			armedAt := session.townTransportEnterSelectArmedAt
			if armedAt.IsZero() {
				t.Fatal("布防必须同时记下 townTransportEnterSelectArmedAt：" +
					"零值时刻会被 currentTownTransportEnterSelectWindow 判成已过期，窗口形同虚设")
			}
			if armedAt.Before(before) || time.Since(armedAt) > 5*time.Second {
				t.Fatalf("布防时刻不属于本次提交：before=%v armed=%v now=%v", before, armedAt, time.Now())
			}
		})
	}
	t.Run("ordinary town move arms nothing", func(t *testing.T) {
		service, session, _, _ := townTransportGateFixture(t)
		service.markCurrentTownSceneReady(session)
		if err := service.handleTownSetUserArea(session, buildTownMoveRequest(38, 0, 900, 250, 5)); err != nil {
			t.Fatal(err)
		}
		if session.townTransportEnterSelectPending || !session.townTransportEnterSelectArmedAt.IsZero() {
			t.Fatalf("普通城镇过图既不得布防也不得留挂账时刻：pending=%t armed_at=%v",
				session.townTransportEnterSelectPending, session.townTransportEnterSelectArmedAt)
		}
	})
}
