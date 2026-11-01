package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	event "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestReturneeNewbieBootstrapOnlyFreshAuthenticatedAccount(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	s := &Service{repositoryProvider: func() (repo.Group, bool) { return r, true }}
	q := &gameSession{accountID: "new", accountFromToken: true}
	enablePortedActivityTest(t, s, q)
	if !s.ensureAccountLevelRecords(ctx, r, q, "new", accountRecordBootstrapLoginSource) {
		t.Fatal("新账号档案未建")
	}
	a, _, _ := r.Account.Load(ctx, "new")
	if a.Metadata[event.NewbieEligibleMetadataKey] != "pending" {
		t.Fatal("首次账号没有待进入资格", a.Metadata)
	}
	if err := r.Account.Save(ctx, repo.AccountRecord{AccountID: "old", Metadata: map[string]string{"x": "keep"}}); err != nil {
		t.Fatal(err)
	}
	s.ensureAccountLevelRecords(ctx, r, q, "old", accountRecordBootstrapLoginSource)
	a, _, _ = r.Account.Load(ctx, "old")
	if len(a.Metadata) != 1 || a.Metadata["x"] != "keep" {
		t.Fatal("老账号被修改", a.Metadata)
	}
	if err := r.Character.Save(ctx, repo.CharacterRecord{AccountID: "orphan", CharacterID: "19"}); err != nil {
		t.Fatal(err)
	}
	s.ensureAccountLevelRecords(ctx, r, q, "orphan", accountRecordBootstrapLoginSource)
	a, _, _ = r.Account.Load(ctx, "orphan")
	if a.Metadata[event.NewbieEligibleMetadataKey] != "" {
		t.Fatal("有历史角色的补档账号被当作新手")
	}
	q.accountFromToken = false
	s.ensureAccountLevelRecords(ctx, r, q, "unverified", accountRecordBootstrapLoginSource)
	a, _, _ = r.Account.Load(ctx, "unverified")
	if a.Metadata[event.NewbieEligibleMetadataKey] != "" {
		t.Fatal("未认证账号获得新手标记")
	}
}

func TestReturneeNewbieNativeTypeZeroAndDistinctRewardRows(t *testing.T) {
	now := time.Now()
	c := &event.ReturneeCatalog{Newbie: &event.ReturneeCatalog{}}
	c.Missions[0] = event.ReturneeMission{ID: 47, Required: 1}
	c.Newbie.Missions[0] = event.ReturneeMission{ID: 40, Required: 1}
	c.Newbie.Attendance[0] = event.ReturneeMission{ID: 26, Required: 1}
	s := event.ReturneeState{Newbie: true, Started: now, ExpiresAt: now.Add(30 * 24 * time.Hour), AttendanceDays: []string{now.In(time.FixedZone("Asia/Shanghai", 8*3600)).Add(-6 * time.Hour).Format("2006-01-02")}}
	b := buildCurrentReturneeBodyAt(c, s, now)
	if binary.LittleEndian.Uint16(b[:]) != 0 || binary.LittleEndian.Uint32(b[16:]) != 40 || binary.LittleEndian.Uint32(b[176:]) != 26 || b[188] != 1 {
		t.Fatal("新手原生状态错误", b)
	}
	s.Newbie = false // 历史UserType=0不能误归为新手。
	b = buildCurrentReturneeBodyAt(c, s, now)
	if binary.LittleEndian.Uint16(b[:]) != 5 || binary.LittleEndian.Uint32(b[16:]) != 47 {
		t.Fatal("历史回归协议变化", b)
	}
	s.Newbie = true
	b = buildCurrentReturneeBodyAt(c, s, s.ExpiresAt)
	if binary.LittleEndian.Uint16(b[:]) != 6 || !bytes.Equal(b[16:288], make([]byte, 272)) {
		t.Fatal("过期仍有新手奖励", b)
	}
}
