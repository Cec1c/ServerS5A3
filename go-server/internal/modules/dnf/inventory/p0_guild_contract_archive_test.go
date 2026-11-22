package inventory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestP0ContractClaimInlineArchiveAndPostArchiveReplay(t *testing.T) {
	repos, o, q, now := contractClaimFixture(t)
	ctx := context.Background()

	// 1. 第 1 天领取契约奖励（RequestID = 1）。
	first, err := o.ClaimGuildContract(ctx, q, now)
	if err != nil {
		t.Fatalf("day 1 ClaimGuildContract failed: %v", err)
	}

	// 2. 跨过次日 06:00 服务日边界后用新 RequestID = 2 领取：
	// 第 1 天已过期回执应被内联归档出主注册表，当日新回执留在主注册表中。
	bag, _, _ := repos.Inventory.Load(ctx, "1")
	delete(bag.Slots, "0:88")
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	day2 := first.DayEnd.Add(time.Minute)
	q2 := q
	q2.RequestID = fmt.Sprintf("%064x", 2)
	second, err := o.ClaimGuildContract(ctx, q2, day2)
	if err != nil {
		t.Fatalf("day 2 ClaimGuildContract failed: %v", err)
	}

	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, inState := reg.ContractClaims[q.RequestID]; inState {
		t.Fatal("expired day-1 contract claim should have been archived out of main registry")
	}
	if _, inState := reg.ContractClaims[q2.RequestID]; !inState {
		t.Fatal("active day-2 contract claim must remain in main registry")
	}
	archived, found, err := guild.LoadArchivedReceipt(ctx, repos.Settings, q.RequestID)
	if err != nil || !found || archived.Kind != guild.ArchivedReceiptKindContractClaim || archived.ContractClaim == nil {
		t.Fatalf("expected archived contract claim, found=%v item=%+v err=%v", found, archived, err)
	}

	// 3. 在第 2 天重放已归档的 RequestID = 1（同角色同公会）：必须幂等返回第 1 天原始回执且不再发道具。
	bagBeforeReplay, _, _ := repos.Inventory.Load(ctx, "1")
	replayed, err := o.ClaimGuildContract(ctx, q, day2.Add(time.Minute))
	if err != nil || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("archived contract claim replay mismatch: got=%+v want=%+v err=%v", replayed, first, err)
	}
	bagAfterReplay, _, _ := repos.Inventory.Load(ctx, "1")
	if !reflect.DeepEqual(bagBeforeReplay, bagAfterReplay) {
		t.Fatal("archived contract claim replay modified inventory")
	}

	// 4. 在归档后的第 3 天测试保存失败回滚：归档与主注册表及背包必须全部原子回滚。
	day3 := second.DayEnd.Add(time.Minute)
	bag, _, _ = repos.Inventory.Load(ctx, "1")
	delete(bag.Slots, "0:88")
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	regBeforeFail, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	oFail, _ := NewOwner(repos)
	oFail.guild = mileageFailingUow{repos.Guild, true}
	q3 := q
	q3.RequestID = fmt.Sprintf("%064x", 3)
	if _, err := oFail.ClaimGuildContract(ctx, q3, day3); err == nil {
		t.Fatal("expected failing registry save error")
	}
	regAfterFail, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	if !reflect.DeepEqual(regBeforeFail, regAfterFail) {
		t.Fatal("failed claim mutated guild registry")
	}
	if _, archivedFound, err := guild.LoadArchivedReceipt(ctx, repos.Settings, q2.RequestID); err != nil || archivedFound {
		t.Fatalf("failed claim leaked inline archive for day-2 receipt: found=%v err=%v", archivedFound, err)
	}

	// 5. 换公会后重放已归档的 RequestID = 1：必须报 ErrPurchaseIdentity。
	reg, _ = guild.Load(ctx, repos.Settings)
	g := reg.Guilds[1]
	delete(reg.Guilds, 1)
	g.ID = 2
	reg.Guilds[2] = g
	reg.NextID = 3
	if err := guild.Save(ctx, repos.Settings, reg, day3); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ClaimGuildContract(ctx, q, day3); !errors.Is(err, guild.ErrPurchaseIdentity) {
		t.Fatalf("replaying archived contract claim after guild switch expected ErrPurchaseIdentity, got %v", err)
	}

	// 6. 校验 CommitTeleport 复用已归档的契约 RequestID = 1 也必须报 ErrPurchaseIdentity。
	ch, _, _ := repos.Character.Load(ctx, "1")
	ch.Level = 50
	ch.Stats["town_id"], ch.Stats["area_id"], ch.Stats["pos_x"], ch.Stats["pos_y"], ch.Stats["direction"] = 38, 0, 100, 200, 5
	if err := repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	reg, _ = guild.Load(ctx, repos.Settings)
	reg.Guilds[2].Benefits[7] = guild.Benefit{Type: 7, Value: 2}
	if err := guild.Save(ctx, repos.Settings, reg, day3); err != nil {
		t.Fatal(err)
	}
	tq := guild.TeleportQuery{
		RequestID:    q.RequestID,
		GuildID:      2,
		Initiator:    q.Owner,
		Participants: []guild.TeleportParticipant{{Owner: q.Owner, From: guild.CharacterTeleportLocation(ch)}},
		Destination:  guild.TeleportLocation{Town: 40, Area: 4, X: 500, Y: 300, Direction: 5},
		MinimumLevel: 20,
		JoinedAt:     reg.Guilds[2].Members[q.Owner.CharacterID].JoinedAt,
		Revalidate:   func() bool { return true },
	}
	if _, err := guild.CommitTeleport(ctx, repos.Guild, tq, day3); !errors.Is(err, guild.ErrPurchaseIdentity) {
		t.Fatalf("CommitTeleport reusing archived contract receipt expected ErrPurchaseIdentity, got %v", err)
	}
}

// TestP0ContractClaimStaleServiceDayNewRequestBlockedAfterArchive 覆盖跨 06:00+08:00 交错场景：
// 角色在 06:00 前已领取当天契约奖励，06:00 后另一请求归档了前一天回执，
// 随后携带 06:00 前旧时间的**新 requestID**进入事务——必须仍返回 ErrContractAlreadyClaimed 且不发奖；
// 同时旧 requestID 仍可正常幂等回放，而新服务日（06:00 后时间）的新 requestID 可正常领取。
func TestP0ContractClaimStaleServiceDayNewRequestBlockedAfterArchive(t *testing.T) {
	repos, o, q, _ := contractClaimFixture(t)
	ctx := context.Background()
	loc := time.FixedZone("UTC+8", 8*3600)
	tBefore6 := time.Date(2026, 11, 21, 5, 59, 50, 0, loc).UTC()
	tStaleBefore6 := time.Date(2026, 11, 21, 5, 59, 55, 0, loc).UTC()
	tAfter6 := time.Date(2026, 11, 21, 6, 0, 10, 0, loc).UTC()

	// 确保契约权益覆盖跨日窗口。
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Benefits[21] = guild.Benefit{Type: 21, Value: 300, ExpiresAt: tAfter6.Add(48 * time.Hour)}
	if err := guild.Save(ctx, repos.Settings, reg, tBefore6); err != nil {
		t.Fatal(err)
	}

	// 1. 05:59:50+08:00 首次领取（req1）。
	first, err := o.ClaimGuildContract(ctx, q, tBefore6)
	if err != nil {
		t.Fatalf("pre-06:00 claim failed: %v", err)
	}
	// 清空槽位模拟玩家消耗或腾出背包空间。
	bag, _, _ := repos.Inventory.Load(ctx, "1")
	delete(bag.Slots, "0:88")
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}

	// 2. 06:00:10+08:00 归档前一天已过期的 req1（主表 ContractClaims 清空，写入 receipt + contract-day 归档）。
	archiver := repos.Guild.(r.GuildArchiveUnitOfWork)
	if stats, err := guild.ArchiveExpiredHistory(ctx, archiver, tAfter6); err != nil || stats.ContractClaims != 1 {
		t.Fatalf("post-06:00 archive failed: stats=%+v err=%v", stats, err)
	}
	regAfterArchive, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(regAfterArchive.ContractClaims) != 0 {
		t.Fatalf("expected 0 ContractClaims in main registry after archive, got %d", len(regAfterArchive.ContractClaims))
	}

	// 3. 携带 05:59:55+08:00 旧时间的新 requestID 进入事务：必须被 contract-day 归档拦截，返回 ErrContractAlreadyClaimed。
	bagBeforeStale, _, _ := repos.Inventory.Load(ctx, "1")
	regBeforeStale, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	qStaleNew := q
	qStaleNew.RequestID = fmt.Sprintf("%064x", 0xdead01)
	if _, err := o.ClaimGuildContract(ctx, qStaleNew, tStaleBefore6); !errors.Is(err, guild.ErrContractAlreadyClaimed) {
		t.Fatalf("stale-day new requestID after archive expected ErrContractAlreadyClaimed, got %v", err)
	}
	bagAfterStale, _, _ := repos.Inventory.Load(ctx, "1")
	regAfterStale, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	if !reflect.DeepEqual(bagBeforeStale, bagAfterStale) || !reflect.DeepEqual(regBeforeStale, regAfterStale) {
		t.Fatal("rejected stale-day claim mutated inventory or registry")
	}

	// 4. 旧 requestID（req1）携带旧时间重放：仍必须幂等返回原始回执且不二次发奖。
	replayed, err := o.ClaimGuildContract(ctx, q, tStaleBefore6)
	if err != nil || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("old requestID replay with stale time failed: got=%+v want=%+v err=%v", replayed, first, err)
	}
	bagAfterReplay, _, _ := repos.Inventory.Load(ctx, "1")
	if !reflect.DeepEqual(bagBeforeStale, bagAfterReplay) {
		t.Fatal("old requestID replay mutated inventory")
	}

	// 5. 新服务日（06:00:10+08:00）的新 requestID：正常领取当日新礼盒。
	qNewDay := q
	qNewDay.RequestID = fmt.Sprintf("%064x", 0x2222)
	second, err := o.ClaimGuildContract(ctx, qNewDay, tAfter6)
	if err != nil || second.DayEnd.Equal(first.DayEnd) {
		t.Fatalf("new service day claim failed: second=%+v first=%+v err=%v", second, first, err)
	}
}
