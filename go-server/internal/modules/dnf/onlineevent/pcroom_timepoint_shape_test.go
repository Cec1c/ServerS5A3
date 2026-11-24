package onlineevent

import (
	"context"
	"strings"
	"testing"
)

type pcroomTextSource map[string]string

func (s pcroomTextSource) ReadText(path string) (string, error) {
	return s[path], nil
}

// 2026-11-03 90CN 上线的 V204 PVF（C1A4073D）把每日奖励改成 5 档。原生面板
// 只有 01/02/04/08 四个每日按钮和 4 位领取掩码（Docs/ONLINE_PCROOM_REWARDS.md），
// 所以加载器必须拒绝；服务端调用方负责把拒绝收敛成活动不可用，而不是断线。
func TestPCRoomTimePointRejectsFiveDailyStages(t *testing.T) {
	const v204 = "\r\n[daily reward autoget]\r\n`false`\r\n[period reward loop]\r\n4\r\n[daily reward loop]\r\n1\r\n" +
		"[daily reward items]\r\n" +
		"1 990027322 1 1200000 `true` `true` 1 10151892 1 1200000 `true` `true` " +
		"1 490009860 5 2400000 `true` `true` 1 990027323 1 2400000 `true` `true` " +
		"1 490008359 1 4200000 `true` `true`\r\n[/daily reward items]\r\n" +
		"[period reward item]\r\n1 490009862 1 5 `true` 2 490010497 5 10 `true` 3 490009866 1 15 `true` 4 490009867 1 20 `true`\r\n[/period reward item]\r\n" +
		"[ui file]\r\n`UI/Event/PCRoomTimePoint/PCRoomTimePointRewardItemInfoWindow_Chn.xui`\r\n"
	_, err := LoadPCRoomTimePointCatalog(context.Background(), pcroomTextSource{PCRoomTimePointPVFPath: v204})
	if err == nil || !strings.Contains(err.Error(), "pcroom [daily reward items] has invalid shape") {
		t.Fatalf("five daily stages err=%v, want invalid shape", err)
	}
}
