package guild

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestP0ArchiveExpiredHistoryCompactsAllBucketsAndPreservesActive(t *testing.T) {
	repos, o, baseNow := hongbaoGroup(t)
	ctx := context.Background()

	// 先发 12 个红包：10 个将结算并超过 24 小时展示期，1 个结算仍在展示期内，1 个未结算。
	var settledInvisibleIDs []uint32
	for i := 0; i < 10; i++ {
		res := hongbaoIssue(t, o, baseNow, fmt.Sprintf("old-%d", i), 103941, 5)
		hongbaoClaim(t, o, baseNow.Add(time.Minute), res.RecordID, 2+i)
		if _, err := o.Settle(ctx, res.RecordID, baseNow.Add(24*time.Hour)); err != nil {
			t.Fatalf("settle old hongbao %d: %v", res.RecordID, err)
		}
		settledInvisibleIDs = append(settledInvisibleIDs, res.RecordID)
	}
	// 在 baseNow+24h..55h 期间，前 10 个红包尚未达到 closedAt+48h（baseNow+72h）宽限截止，
	// 因此不会被内联归档；同时创建 1 个未结算红包与 1 个抢满 5 份并在 baseNow+55h 结算的红包（归档宽限至 baseNow+103h）。
	unsettled := hongbaoIssue(t, o, baseNow.Add(24*time.Hour), "unsettled-expired", 103941, 5)
	recentSettled := hongbaoIssue(t, o, baseNow.Add(54*time.Hour), "recent-settled", 103941, 5)
	for c := 15; c < 20; c++ {
		hongbaoClaim(t, o, baseNow.Add(55*time.Hour), recentSettled.RecordID, c)
	}
	if _, err := o.Settle(ctx, recentSettled.RecordID, baseNow.Add(55*time.Hour)); err != nil {
		t.Fatalf("settle recent visible hongbao: %v", err)
	}
	// 运行时间推进到 baseNow + 73h：前 10 个红包已过展示期与 24h 宽限（>=72h）应被启动批量归档，
	// recentSettled（展示至 79h、宽限至 103h）与 unsettled（未结算）必须留在主注册表。
	now := baseNow.Add(73 * time.Hour)

	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.ContractClaims = make(map[string]ContractClaimReceipt)
	for i := 1; i <= 200; i++ {
		id := fmt.Sprintf("%064x", i)
		claimAt := now.Add(-time.Duration(i+1) * 24 * time.Hour)
		reg.ContractClaims[id] = ContractClaimReceipt{
			Owner:   r.GuildOwner{AccountID: "a1", CharacterID: "1"},
			GuildID: 1,
			ItemID:  490001385,
			Count:   1,
			At:      claimAt,
			DayEnd:  itemexpiration.NextDailyDeleteAt(claimAt),
			Slots:   []int16{88},
		}
	}
	for i := 201; i <= 202; i++ {
		id := fmt.Sprintf("%064x", i)
		reg.ContractClaims[id] = ContractClaimReceipt{
			Owner:   r.GuildOwner{AccountID: fmt.Sprintf("a%d", i-199), CharacterID: fmt.Sprintf("%d", i-199)},
			GuildID: 1,
			ItemID:  490001385,
			Count:   1,
			At:      now.Add(-time.Minute),
			DayEnd:  itemexpiration.NextDailyDeleteAt(now.Add(-time.Minute)),
			Slots:   []int16{88},
		}
	}

	reg.Teleports = make(map[string]TeleportReceipt)
	locFrom := TeleportLocation{Town: 38, Area: 0, X: 100, Y: 200, Direction: 5}
	locTo := TeleportLocation{Town: 40, Area: 4, X: 500, Y: 300, Direction: 5}
	owner1 := r.GuildOwner{AccountID: "a1", CharacterID: "1"}
	joinedAt := reg.Guilds[1].Members["1"].JoinedAt.UTC()
	for i := 1; i <= 40; i++ {
		id := fmt.Sprintf("%064x", 1000+i)
		reg.Teleports[id] = TeleportReceipt{
			GuildID:      1,
			Initiator:    owner1,
			Participants: []TeleportParticipant{{Owner: owner1, From: locFrom}},
			Destination:  locTo,
			At:           now.Add(-36 * time.Hour),
			JoinedAt:     joinedAt,
		}
	}
	freshTeleportID := fmt.Sprintf("%064x", 2000)
	reg.Teleports[freshTeleportID] = TeleportReceipt{
		GuildID:      1,
		Initiator:    owner1,
		Participants: []TeleportParticipant{{Owner: owner1, From: locFrom}},
		Destination:  locTo,
		At:           now.Add(-2 * time.Hour),
		JoinedAt:     joinedAt,
	}

	reg.Departures = make(map[string]Departure)
	for i := 1; i <= 5; i++ {
		cid := fmt.Sprintf("90%d", i)
		at := now.Add(-48 * time.Hour)
		reg.Departures[cid] = Departure{
			Owner: r.GuildOwner{AccountID: "a" + cid, CharacterID: cid},
			At:    at,
			Until: itemexpiration.NextDailyDeleteAt(at),
		}
	}
	activeDepAt := now.Add(-time.Minute)
	reg.Departures["999"] = Departure{
		Owner: r.GuildOwner{AccountID: "a999", CharacterID: "999"},
		At:    activeDepAt,
		Until: itemexpiration.NextDailyDeleteAt(activeDepAt),
	}
	wantNextHongbaoID := reg.Hongbao.NextID

	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}

	archiver := repos.Guild.(r.GuildArchiveUnitOfWork)
	stats, err := ArchiveExpiredHistory(ctx, archiver, now)
	if err != nil {
		t.Fatalf("ArchiveExpiredHistory failed: %v", err)
	}
	if stats.ContractClaims != 200 || stats.Teleports != 40 || stats.Hongbao != 10 || stats.Departures != 5 {
		t.Fatalf("unexpected archive stats: %+v", stats)
	}
	if stats.BeforeBytes == 0 || stats.AfterBytes == 0 || stats.AfterBytes >= stats.BeforeBytes/2 {
		t.Fatalf("expected >50%% byte reduction, before=%d after=%d", stats.BeforeBytes, stats.AfterBytes)
	}

	afterReg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterReg.ContractClaims) != 2 {
		t.Fatalf("expected 2 active ContractClaims remaining, got %d", len(afterReg.ContractClaims))
	}
	if len(afterReg.Teleports) != 1 || afterReg.Teleports[freshTeleportID].GuildID != 1 {
		t.Fatalf("expected 1 fresh Teleport remaining, got %d", len(afterReg.Teleports))
	}
	if len(afterReg.Departures) != 1 || afterReg.Departures["999"].Owner.CharacterID != "999" {
		t.Fatalf("expected 1 active Departure remaining, got %+v", afterReg.Departures)
	}
	if afterReg.Hongbao == nil || afterReg.Hongbao.NextID != wantNextHongbaoID || len(afterReg.Hongbao.Records) != 2 {
		t.Fatalf("unexpected Hongbao state after archive: NextID=%d Records=%d", afterReg.Hongbao.NextID, len(afterReg.Hongbao.Records))
	}
	if _, ok := afterReg.Hongbao.Records[recentSettled.RecordID]; !ok {
		t.Fatal("recent visible settled hongbao must remain in main registry")
	}
	if _, ok := afterReg.Hongbao.Records[unsettled.RecordID]; !ok {
		t.Fatal("unsettled expired hongbao must remain in main registry")
	}

	// 验证二次调用为 0 写入幂等空操作。
	again, err := ArchiveExpiredHistory(ctx, archiver, now)
	if err != nil || again.TotalArchived() != 0 {
		t.Fatalf("second ArchiveExpiredHistory expected no-op, got stats=%+v err=%v", again, err)
	}

	// 验证已归档红包仍支持 Settle 幂等回放与 Claim 原语义（已领返回 AlreadyClaimed，未领返回 Unavailable）。
	for idx, id := range settledInvisibleIDs[:2] {
		replayed, err := o.Settle(ctx, id, now.Add(time.Hour))
		if err != nil || !replayed.Replayed || replayed.RecordID != id {
			t.Fatalf("Settle replay for archived hongbao %d failed: res=%+v err=%v", id, replayed, err)
		}
		if _, err := o.Claim(ctx, HongbaoClaimRequest{hongbaoRequest(2+idx, fmt.Sprintf("reclaim-old-%d", id)), id}, now.Add(time.Hour)); !errors.Is(err, ErrHongbaoAlreadyClaimed) {
			t.Fatalf("archived hongbao reclaim by existing claimer expected ErrHongbaoAlreadyClaimed, got %v", err)
		}
		if _, err := o.Claim(ctx, HongbaoClaimRequest{hongbaoRequest(20, fmt.Sprintf("newclaim-old-%d", id)), id}, now.Add(time.Hour)); !errors.Is(err, ErrHongbaoUnavailable) {
			t.Fatalf("archived hongbao claim by new member expected ErrHongbaoUnavailable, got %v", err)
		}
	}

	// 验证新发红包继续递增 NextID，不会与归档 ID 冲突；且当时间推进至 baseNow+104h（超过 recentSettled 48h 归档宽限 103h）时，
	// 发新红包会内联归档 recentSettled，并保持 recentSettled 仍可 Settle 回放。
	nextIssue := hongbaoIssue(t, o, baseNow.Add(104*time.Hour), "post-archive-new", 103941, 5)
	if nextIssue.RecordID != wantNextHongbaoID {
		t.Fatalf("new hongbao RecordID=%d want %d", nextIssue.RecordID, wantNextHongbaoID)
	}
	afterInline, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, stillInState := afterInline.Hongbao.Records[recentSettled.RecordID]; stillInState {
		t.Fatal("recentSettled should have been archived inline once past 48h archive grace window")
	}
	if replayedRecent, err := o.Settle(ctx, recentSettled.RecordID, baseNow.Add(105*time.Hour)); err != nil || !replayedRecent.Replayed {
		t.Fatalf("Settle replay for inline-archived hongbao failed: res=%+v err=%v", replayedRecent, err)
	}
}

func TestP0ArchivedReceiptsPreserveReplayAndCrossMapConflicts(t *testing.T) {
	repos, q, now := teleportFixture(t, 2)
	ctx := context.Background()

	// 1. 提交第 1 次传送，25 小时后提交第 2 次传送触发内联归档。
	first, err := CommitTeleport(ctx, repos.Guild, q, now)
	if err != nil {
		t.Fatalf("first CommitTeleport failed: %v", err)
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	// 补足传送次数并把角色搬回起点以执行第 2 次新传送。
	reg.Guilds[1].Benefits[7] = Benefit{Type: 7, Value: 5}
	reg.Guilds[1].Mileage = 500000
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	for _, p := range q.Participants {
		ch, _, _ := repos.Character.Load(ctx, p.Owner.CharacterID)
		ch.Stats["town_id"], ch.Stats["area_id"] = p.From.Town, p.From.Area
		ch.Stats["pos_x"], ch.Stats["pos_y"], ch.Stats["direction"] = p.From.X, p.From.Y, p.From.Direction
		if err := repos.Character.Save(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}
	later := now.Add(25 * time.Hour)
	q2 := q
	q2.RequestID = fmt.Sprintf("%064x", 102)
	if _, err := CommitTeleport(ctx, repos.Guild, q2, later); err != nil {
		t.Fatalf("second CommitTeleport failed: %v", err)
	}

	// 验证 q.RequestID 已从主注册表迁入归档，q2.RequestID 留在主注册表。
	reg, err = Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := reg.Teleports[q.RequestID]; exists {
		t.Fatal("old teleport receipt should have been archived out of main registry")
	}
	if _, exists := reg.Teleports[q2.RequestID]; !exists {
		t.Fatal("fresh teleport receipt should remain in main registry")
	}

	// 2. 重放已归档的 q.RequestID（相同参数）：不得扣减次数，不得移动角色。
	ch1, _, _ := repos.Character.Load(ctx, q.Initiator.CharacterID)
	ch1.Stats["pos_x"] = 777
	if err := repos.Character.Save(ctx, ch1); err != nil {
		t.Fatal(err)
	}
	usesBefore := reg.Guilds[1].Benefits[7].Value
	replayed, err := CommitTeleport(ctx, repos.Guild, q, later.Add(time.Minute))
	if err != nil || !teleportReceiptEqual(replayed, first) {
		t.Fatalf("archived teleport replay mismatch: got=%+v want=%+v err=%v", replayed, first, err)
	}
	ch1, _, _ = repos.Character.Load(ctx, q.Initiator.CharacterID)
	if ch1.Stats["pos_x"] != 777 {
		t.Fatal("archived teleport replay unexpectedly moved character")
	}
	reg, _ = Load(ctx, repos.Settings)
	if reg.Guilds[1].Benefits[7].Value != usesBefore {
		t.Fatal("archived teleport replay unexpectedly spent teleport use")
	}

	// 3. 用已归档的 q.RequestID 改目标位置重放：必须报 ErrPurchaseIdentity。
	mutated := q
	mutated.Destination.X += 10
	if _, err := CommitTeleport(ctx, repos.Guild, mutated, later.Add(2*time.Minute)); !errors.Is(err, ErrPurchaseIdentity) {
		t.Fatalf("mutated replay of archived teleport expected ErrPurchaseIdentity, got %v", err)
	}

	// 4. 跨表冲突校验：在 PurchaseBenefit / PurchaseCargo / ExpandMembers 复用已归档的 teleport requestID 必须报 ErrPurchaseIdentity。
	catalog := ContentCatalog{
		CargoLimit: 112,
		Contents: map[uint32]Content{
			1: {ID: 1, Type: 1, Value: 10, Days: 1, Price: 100},
			4: {ID: 4, Type: 4, Value: 8, Days: -1, Price: 100},
			6: {ID: 6, Type: 6, Value: 500, Days: -1, Price: 100},
		},
	}
	purchaseQuery := CargoPurchase{
		Owner:           q.Initiator,
		RequestID:       q.RequestID,
		ContentID:       1,
		InitialCapacity: 16,
		Revalidate:      func() bool { return true },
	}
	if _, err := PurchaseBenefit(ctx, repos.Guild, purchaseQuery, catalog, later); !errors.Is(err, ErrPurchaseIdentity) {
		t.Fatalf("PurchaseBenefit reusing archived receipt expected ErrPurchaseIdentity, got %v", err)
	}
	purchaseQuery.ContentID = 4
	if _, err := PurchaseCargo(ctx, repos.Guild, purchaseQuery, catalog, later); !errors.Is(err, ErrPurchaseIdentity) {
		t.Fatalf("PurchaseCargo reusing archived receipt expected ErrPurchaseIdentity, got %v", err)
	}
	purchaseQuery.ContentID = 6
	if _, err := PurchaseMemberExpansion(ctx, repos.Guild, purchaseQuery, catalog, later); !errors.Is(err, ErrPurchaseIdentity) {
		t.Fatalf("PurchaseMemberExpansion reusing archived receipt expected ErrPurchaseIdentity, got %v", err)
	}

	// 5. 归档冲突保护：已存在不同载荷的归档键绝不能被静默覆盖。
	conflicting := ArchivedReceipt{
		Version:   1,
		Kind:      ArchivedReceiptKindTeleport,
		RequestID: q.RequestID,
		Teleport:  &TeleportReceipt{GuildID: 1, Initiator: q.Initiator, Participants: q.Participants, Destination: mutated.Destination, At: first.At, JoinedAt: first.JoinedAt},
	}
	if err := saveArchivedReceipt(ctx, repos.Settings, conflicting, later); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("conflicting archive overwrite expected ErrInvalidRegistry, got %v", err)
	}
}

func TestP0ArchiveExpiredHistoryConflictingReceiptAndHongbaoNeverOverwritten(t *testing.T) {
	t.Run("conflicting_receipt_preserved_and_registry_unchanged", func(t *testing.T) {
		ctx, repos := context.Background(), guildTestGroup(t, 2)
		reg, _, _, owner := textRegistry(t)
		now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
		oldAt := now.Add(-48 * time.Hour)
		reqID := fmt.Sprintf("%064x", 0x55)
		reg.ContractClaims = map[string]ContractClaimReceipt{
			reqID: {
				Owner:   owner,
				GuildID: 1,
				ItemID:  490001385,
				Count:   1,
				At:      oldAt,
				DayEnd:  itemexpiration.NextDailyDeleteAt(oldAt),
				Slots:   []int16{88},
			},
		}
		if err := Save(ctx, repos.Settings, reg, now); err != nil {
			t.Fatal(err)
		}
		// 预先写入同键但不同载荷的既有归档（例如 slot 不同）。
		existing := ArchivedReceipt{
			Version:   1,
			Kind:      ArchivedReceiptKindContractClaim,
			RequestID: reqID,
			ContractClaim: &ContractClaimReceipt{
				Owner:   owner,
				GuildID: 1,
				ItemID:  490001385,
				Count:   1,
				At:      oldAt,
				DayEnd:  itemexpiration.NextDailyDeleteAt(oldAt),
				Slots:   []int16{77},
			},
		}
		if err := saveArchivedReceipt(ctx, repos.Settings, existing, now); err != nil {
			t.Fatal(err)
		}
		beforeArchive, _, _ := repos.Settings.Load(ctx, r.GuildReceiptArchiveScope(reqID))
		beforeReg, _, _ := repos.Settings.Load(ctx, RegistryScope)

		archiver := repos.Guild.(r.GuildArchiveUnitOfWork)
		if _, err := ArchiveExpiredHistory(ctx, archiver, now); !errors.Is(err, ErrInvalidRegistry) {
			t.Fatalf("conflicting receipt archive expected ErrInvalidRegistry, got %v", err)
		}
		afterArchive, _, _ := repos.Settings.Load(ctx, r.GuildReceiptArchiveScope(reqID))
		afterReg, _, _ := repos.Settings.Load(ctx, RegistryScope)
		if !reflect.DeepEqual(beforeArchive, afterArchive) {
			t.Fatal("existing conflicting receipt archive was overwritten")
		}
		if !reflect.DeepEqual(beforeReg, afterReg) {
			t.Fatal("main registry changed despite conflicting receipt archive")
		}
	})

	t.Run("conflicting_hongbao_preserved_and_registry_unchanged", func(t *testing.T) {
		repos, o, baseNow := hongbaoGroup(t)
		ctx := context.Background()
		issued := hongbaoIssue(t, o, baseNow, "conflict-hb", 103941, 5)
		hongbaoClaim(t, o, baseNow.Add(time.Minute), issued.RecordID, 2)
		if _, err := o.Settle(ctx, issued.RecordID, baseNow.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		reg, err := Load(ctx, repos.Settings)
		if err != nil {
			t.Fatal(err)
		}
		hb := reg.Hongbao.Records[issued.RecordID].copy()
		conflictHB := hb.copy()
		conflictHB.SenderName = "既有归档发送者"
		if err := saveArchivedHongbaoRecord(ctx, repos.Settings, conflictHB, baseNow.Add(25*time.Hour)); err != nil {
			t.Fatal(err)
		}
		beforeArchive, _, _ := repos.Settings.Load(ctx, r.GuildHongbaoArchiveScope(issued.RecordID))
		beforeReg, _, _ := repos.Settings.Load(ctx, RegistryScope)

		archiver := repos.Guild.(r.GuildArchiveUnitOfWork)
		if _, err := ArchiveExpiredHistory(ctx, archiver, baseNow.Add(73*time.Hour)); !errors.Is(err, ErrHongbaoState) {
			t.Fatalf("conflicting hongbao archive expected ErrHongbaoState, got %v", err)
		}
		afterArchive, _, _ := repos.Settings.Load(ctx, r.GuildHongbaoArchiveScope(issued.RecordID))
		afterReg, _, _ := repos.Settings.Load(ctx, RegistryScope)
		if !reflect.DeepEqual(beforeArchive, afterArchive) {
			t.Fatal("existing conflicting hongbao archive was overwritten")
		}
		if !reflect.DeepEqual(beforeReg, afterReg) {
			t.Fatal("main registry changed despite conflicting hongbao archive")
		}
	})
}

func TestP0ArchiveExpiredHistoryConcurrentSameKeyAndRollbackBarrier(t *testing.T) {
	t.Run("rollback_barrier_never_loses_receipt_between_main_and_archive", func(t *testing.T) {
		repos, q, now := teleportFixture(t, 2)
		ctx := context.Background()
		first, err := CommitTeleport(ctx, repos.Guild, q, now)
		if err != nil {
			t.Fatal(err)
		}
		reg, _ := Load(ctx, repos.Settings)
		reg.Guilds[1].Benefits[7] = Benefit{Type: 7, Value: 5}
		if err := Save(ctx, repos.Settings, reg, now); err != nil {
			t.Fatal(err)
		}
		for _, p := range q.Participants {
			ch, _, _ := repos.Character.Load(ctx, p.Owner.CharacterID)
			ch.Stats["town_id"], ch.Stats["area_id"] = p.From.Town, p.From.Area
			ch.Stats["pos_x"], ch.Stats["pos_y"], ch.Stats["direction"] = p.From.X, p.From.Y, p.From.Direction
			if err := repos.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
		}

		// 确定性屏障交错：
		// 事务 B（CommitTeleport q2）进入 WithinGuildTravel 后已将旧回执 q 写入内联归档，
		// 在 Revalidate 第 2 次检查处暂停并唤醒并发归档任务 A（ArchiveExpiredHistory），
		// 在确定性信号下 B 主动失败回滚；通过原子标志校验 A 绝不会在 B 回滚前进入 WithinGuildArchive，
		// 最终主表或归档中必保留一份有效回执（无 sleep 调度假设）。
		later := now.Add(25 * time.Hour)
		bStagedArchive := make(chan struct{})
		aLaunched := make(chan struct{})
		var bRollbackRequested atomic.Bool
		var aObservedBeforeRollback atomic.Bool
		revalCalls := 0
		q2 := q
		q2.RequestID = fmt.Sprintf("%064x", 202)
		q2.Revalidate = func() bool {
			revalCalls++
			if revalCalls == 2 {
				close(bStagedArchive)
				<-aLaunched
				bRollbackRequested.Store(true)
				return false // 触发 B 事务完整回滚
			}
			return true
		}

		bDone := make(chan error, 1)
		go func() {
			_, commitErr := CommitTeleport(ctx, repos.Guild, q2, later)
			bDone <- commitErr
		}()
		<-bStagedArchive

		archiver := observingGuildArchiveUOW{
			GuildArchiveUnitOfWork: repos.Guild.(r.GuildArchiveUnitOfWork),
			onEnter: func() {
				if !bRollbackRequested.Load() {
					aObservedBeforeRollback.Store(true)
				}
			},
		}
		aDone := make(chan error, 1)
		go func() {
			close(aLaunched)
			_, archErr := ArchiveExpiredHistory(ctx, archiver, later)
			aDone <- archErr
		}()

		if bErr := <-bDone; !errors.Is(bErr, ErrTeleportUnavailable) {
			t.Fatalf("expected B rollback ErrTeleportUnavailable, got %v", bErr)
		}
		if aErr := <-aDone; aErr != nil {
			t.Fatalf("ArchiveExpiredHistory after B rollback failed: %v", aErr)
		}
		if aObservedBeforeRollback.Load() {
			t.Fatal("ArchiveExpiredHistory entered locked archive transaction before B finished rollback")
		}

		// 断言回执绝对未丢失：主表与归档中至少保留一份（此处应已由 A 安全归档），且 q 重放仍幂等。
		afterReg, err := Load(ctx, repos.Settings)
		if err != nil {
			t.Fatal(err)
		}
		_, inMain := afterReg.Teleports[q.RequestID]
		archived, inArchive, err := LoadArchivedReceipt(ctx, repos.Settings, q.RequestID)
		if err != nil {
			t.Fatal(err)
		}
		if !inMain && !inArchive {
			t.Fatal("receipt lost from both main registry and archive after concurrent rollback")
		}
		if inArchive && (archived.Teleport == nil || !teleportReceiptEqual(*archived.Teleport, first)) {
			t.Fatalf("archived receipt corrupted: %+v", archived)
		}
		if _, inMainQ2 := afterReg.Teleports[q2.RequestID]; inMainQ2 {
			t.Fatal("rolled-back q2 leaked into main registry")
		}
	})

	t.Run("concurrent_same_key_archive_workers_serialize_cleanly", func(t *testing.T) {
		repos, o, baseNow := hongbaoGroup(t)
		ctx := context.Background()
		issued := hongbaoIssue(t, o, baseNow, "conc-hb", 103941, 5)
		hongbaoClaim(t, o, baseNow.Add(time.Minute), issued.RecordID, 2)
		if _, err := o.Settle(ctx, issued.RecordID, baseNow.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		now := baseNow.Add(73 * time.Hour)
		reg, err := Load(ctx, repos.Settings)
		if err != nil {
			t.Fatal(err)
		}
		oldAt := now.Add(-48 * time.Hour)
		reqID := fmt.Sprintf("%064x", 0x88)
		reg.ContractClaims = map[string]ContractClaimReceipt{
			reqID: {
				Owner:   r.GuildOwner{AccountID: "a1", CharacterID: "1"},
				GuildID: 1,
				ItemID:  490001385,
				Count:   1,
				At:      oldAt,
				DayEnd:  itemexpiration.NextDailyDeleteAt(oldAt),
				Slots:   []int16{88},
			},
		}
		if err := Save(ctx, repos.Settings, reg, now); err != nil {
			t.Fatal(err)
		}

		archiver := repos.Guild.(r.GuildArchiveUnitOfWork)
		const workers = 8
		start := make(chan struct{})
		results := make(chan ArchiveHistoryStats, workers)
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				st, err := ArchiveExpiredHistory(ctx, archiver, now)
				if err != nil {
					errs <- err
					return
				}
				results <- st
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		close(results)
		for err := range errs {
			t.Fatalf("concurrent ArchiveExpiredHistory error: %v", err)
		}
		var totalClaims, totalHongbao int
		for st := range results {
			totalClaims += st.ContractClaims
			totalHongbao += st.Hongbao
		}
		if totalClaims != 1 || totalHongbao != 1 {
			t.Fatalf("expected exact once archival across %d workers, got claims=%d hongbao=%d", workers, totalClaims, totalHongbao)
		}
		if archived, found, err := LoadArchivedReceipt(ctx, repos.Settings, reqID); err != nil || !found || archived.Kind != ArchivedReceiptKindContractClaim {
			t.Fatalf("receipt archive missing after concurrent workers: found=%v err=%v", found, err)
		}
		if replayed, err := o.Settle(ctx, issued.RecordID, now.Add(time.Hour)); err != nil || !replayed.Replayed {
			t.Fatalf("hongbao archive replay failed after concurrent workers: res=%+v err=%v", replayed, err)
		}
	})
}

type observingGuildArchiveUOW struct {
	r.GuildArchiveUnitOfWork
	onEnter func()
}

func (u observingGuildArchiveUOW) WithinGuildArchive(ctx context.Context, apply func(r.SettingsRepository) error) error {
	return u.GuildArchiveUnitOfWork.WithinGuildArchive(ctx, func(txSettings r.SettingsRepository) error {
		if u.onEnter != nil {
			u.onEnter()
		}
		return apply(txSettings)
	})
}
