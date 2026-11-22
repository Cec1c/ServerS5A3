package dnfbridge

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/wedding"
	"longheng.io/server/internal/platform/config"
)

type failingWriteConn struct {
	bufferConn
	err error
}

func (c *failingWriteConn) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	return c.bufferConn.Write(p)
}

func countSilentMode8Frames(t *testing.T, raw []byte) int {
	t.Helper()
	count := 0
	for len(raw) > 0 {
		pkt, rest := splitCurrentGameServerUpperPacketAuto(t, raw)
		raw = rest
		if pkt.Header.Classification == 0 && pkt.Header.MsgID == 2 && len(pkt.Body) > 0 && pkt.Body[0] == 8 {
			count++
		}
	}
	return count
}

func setupP1WeddingGuildRosterFixture(t *testing.T) (*townPartyActorHPFixture, dnfrepo.Group, dnfrepo.CharacterRecord) {
	t.Helper()
	f, repos := guildMemberProfileFixture(t)
	s := f.service
	ctx := context.Background()

	coherent := memory.NewMemoryGroup()
	for _, id := range []string{"29", "17"} {
		c, found, err := repos.Character.Load(ctx, id)
		if err != nil || !found {
			t.Fatalf("load char %s: found=%v err=%v", id, found, err)
		}
		if err := coherent.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	registry, found, err := repos.Settings.Load(ctx, dnfrepo.GuildRegistryScope)
	if err != nil || !found {
		t.Fatalf("load registry: found=%v err=%v", found, err)
	}
	if err := coherent.Settings.Save(ctx, registry); err != nil {
		t.Fatal(err)
	}
	viewer, _, _ := coherent.Character.Load(ctx, "29")
	viewer.Stats["wedding_v1_phase"] = int64(wedding.Reserved)
	viewer.Stats["wedding_v1_partner"] = 30
	viewer.Stats["wedding_v1_ceremony_at"] = s.gameplayNow().Unix() + 300
	viewer.Stats["wedding_v1_ceremony_channel"] = int64(currentTownPresenceChannelID(f.target))
	viewer.Stats["wedding_v1_ceremony_area"] = 8
	if err := dnfrepo.SaveCharacterFields(ctx, coherent.Character, viewer, dnfrepo.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	repos.Character, repos.Settings, repos.Guild = coherent.Character, coherent.Settings, coherent.Guild
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	return f, repos, viewer
}

func TestP1WeddingGuildRosterPrimeDeduplicatesAndReprimesOnChange(t *testing.T) {
	f, _, viewer := setupP1WeddingGuildRosterFixture(t)
	s := f.service
	ctx := context.Background()

	// 1. 首次请求公会列表：应为在线成员 17 发送 1 条静默 mode8 预热包。
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("first roster request failed: %v", err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 1 {
		t.Fatalf("first roster request expected 1 mode8 prime frame, got %d", got)
	}

	// 2. 再次刷新（成员 17 会话/世代/频道/外观均未变化）：不得重复发 mode8 预热包。
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("second roster request failed: %v", err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
		t.Fatalf("repeat roster request without peer change expected 0 mode8 frames, got %d", got)
	}

	// 3. 成员 17 外观版本变更后：必须重新发 1 条 mode8 预热包。
	actorSession := s.gameSessions[17]
	actorSession.crossSceneAppearanceMu.Lock()
	actorSession.crossSceneAppearanceRevision++
	actorSession.crossSceneAppearanceMu.Unlock()
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("roster request after appearance change failed: %v", err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 1 {
		t.Fatalf("after actor appearance change expected 1 mode8 frame, got %d", got)
	}

	// 4. 成员 17 频道变更后：必须重新发 1 条 mode8 预热包。
	actorSession.channel.ID = 12
	actorSession.connectionTownActorOwnerChannel = 12
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("roster request after actor channel change failed: %v", err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 1 {
		t.Fatalf("after actor channel change expected 1 mode8 frame, got %d", got)
	}

	// 5. 成员 17 角色绑定世代变更后（如重新选角复用同会话）：必须重新发 1 条 mode8 预热包。
	s.mu.Lock()
	actorSession.characterGeneration++
	s.mu.Unlock()
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("roster request after actor generation change failed: %v", err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 1 {
		t.Fatalf("after actor generation change expected 1 mode8 frame, got %d", got)
	}

	// 6. 成员 17 目标会话替换为新 *gameSession 后（即便频道、外观版本、世代数值一致）：必须重新发 1 条 mode8 预热包。
	oldGen := actorSession.characterGeneration
	oldRev := currentCrossSceneAppearanceRevision(actorSession)
	replacement := &gameSession{
		conn:                            &bufferConn{},
		connID:                          "town-party-actor-hp-peer-replaced",
		accountID:                       actorSession.accountID,
		channel:                         actorSession.channel,
		residentChannel:                 actorSession.residentChannel,
		selectedCharacterID:             17,
		characterGeneration:             oldGen,
		townSceneReadyCharacterID:       17,
		townActorOwnerChannel:           actorSession.townActorOwnerChannel,
		connectionTownActorOwnerChannel: actorSession.connectionTownActorOwnerChannel,
		crossSceneAppearanceRevision:    oldRev,
	}
	s.registerLiveGameSession(replacement)
	s.mu.Lock()
	s.gameSessions[17] = replacement
	s.mu.Unlock()
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("roster request after actor session replacement failed: %v", err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 1 {
		t.Fatalf("after actor session replacement expected 1 mode8 frame, got %d", got)
	}

	// 7. 观察者会话 reset 后（如切角色/重进场）：缓存清空，必须重新发 1 条 mode8。
	resetCurrentGuildPresenceRefresh(f.target, false)
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("roster request after viewer reset failed: %v", err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 1 {
		t.Fatalf("after viewer reset expected 1 mode8 frame, got %d", got)
	}

	// 8. 已取消的 ctx 应立即退出，不执行后续成员预热。
	resetCurrentGuildPresenceRefresh(f.target, false)
	bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
	if !ok {
		t.Fatal("missing bound snapshot")
	}
	display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	f.targetConn.write.Reset()
	if primeErr := s.primeCurrentWeddingGuildRoster(cancelled, f.target, viewer, display); primeErr == nil {
		t.Fatal("expected context error on cancelled primeCurrentWeddingGuildRoster")
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
		t.Fatalf("cancelled context unexpectedly sent %d mode8 frames", got)
	}
}

func TestP1WeddingGuildRosterPrimeNonWrittenOutcomesDoNotCache(t *testing.T) {
	t.Run("diagnostic_suppress_filter_not_cached", func(t *testing.T) {
		resetUpperSendFilterCache()
		resetVendorAbsentBlockCache()
		defer func() {
			resetUpperSendFilterCache()
			resetVendorAbsentBlockCache()
		}()
		t.Setenv(upperSendFilterEnv, "0:2")
		t.Setenv(vendorAbsentSendBlockEnvKey, "off")

		f, _, viewer := setupP1WeddingGuildRosterFixture(t)
		s := f.service
		ctx := context.Background()
		bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
		if !ok {
			t.Fatal("missing bound snapshot")
		}
		display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
		if err != nil {
			t.Fatal(err)
		}

		f.targetConn.write.Reset()
		if err := s.primeCurrentWeddingGuildRoster(ctx, f.target, viewer, display); err != nil {
			t.Fatalf("suppressed prime returned unexpected error: %v", err)
		}
		if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
			t.Fatalf("suppressed prime wrote %d mode8 frames, want 0", got)
		}
		f.target.guild.mu.Lock()
		cached := len(f.target.guild.weddingPrimedPeers)
		f.target.guild.mu.Unlock()
		if cached != 0 {
			t.Fatalf("suppressed send must not populate weddingPrimedPeers, got %d entries", cached)
		}

		// 解除诊断过滤后，下一次调用必须实际写出 1 条 mode8 并建立缓存。
		resetUpperSendFilterCache()
		t.Setenv(upperSendFilterEnv, "")
		f.targetConn.write.Reset()
		if err := s.primeCurrentWeddingGuildRoster(ctx, f.target, viewer, display); err != nil {
			t.Fatalf("post-unsuppress prime failed: %v", err)
		}
		if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 1 {
			t.Fatalf("post-unsuppress prime expected 1 mode8 frame, got %d", got)
		}
		f.targetConn.write.Reset()
		if err := s.primeCurrentWeddingGuildRoster(ctx, f.target, viewer, display); err != nil {
			t.Fatalf("cached repeat prime failed: %v", err)
		}
		if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
			t.Fatalf("repeat prime after actual write expected 0 mode8 frames, got %d", got)
		}
	})

	t.Run("vendor_absent_block_not_cached", func(t *testing.T) {
		resetUpperSendFilterCache()
		resetVendorAbsentBlockCache()
		defer func() {
			resetUpperSendFilterCache()
			resetVendorAbsentBlockCache()
		}()
		t.Setenv(upperSendFilterEnv, "")
		t.Setenv(vendorAbsentSendBlockEnvKey, "0:2")

		f, _, viewer := setupP1WeddingGuildRosterFixture(t)
		s := f.service
		ctx := context.Background()
		bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
		if !ok {
			t.Fatal("missing bound snapshot")
		}
		display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
		if err != nil {
			t.Fatal(err)
		}

		f.targetConn.write.Reset()
		if err := s.primeCurrentWeddingGuildRoster(ctx, f.target, viewer, display); err != nil {
			t.Fatalf("vendor-blocked prime returned unexpected error: %v", err)
		}
		if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
			t.Fatalf("vendor-blocked prime wrote %d mode8 frames, want 0", got)
		}
		f.target.guild.mu.Lock()
		cached := len(f.target.guild.weddingPrimedPeers)
		f.target.guild.mu.Unlock()
		if cached != 0 {
			t.Fatalf("vendor-blocked send must not populate weddingPrimedPeers, got %d entries", cached)
		}
	})

	t.Run("scene_batch_buffered_not_cached_until_direct_write", func(t *testing.T) {
		resetUpperSendFilterCache()
		resetVendorAbsentBlockCache()
		defer func() {
			resetUpperSendFilterCache()
			resetVendorAbsentBlockCache()
		}()
		t.Setenv(upperSendFilterEnv, "")
		t.Setenv(vendorAbsentSendBlockEnvKey, "off")

		f, _, viewer := setupP1WeddingGuildRosterFixture(t)
		s := f.service
		ctx := context.Background()
		bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
		if !ok {
			t.Fatal("missing bound snapshot")
		}
		display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
		if err != nil {
			t.Fatal(err)
		}

		f.target.wireMu.Lock()
		f.target.channelSceneBatch = &currentChannelSceneBatch{started: time.Now()}
		f.target.wireMu.Unlock()

		f.targetConn.write.Reset()
		if err := s.primeCurrentWeddingGuildRoster(ctx, f.target, viewer, display); err != nil {
			t.Fatalf("buffered prime returned unexpected error: %v", err)
		}
		if f.targetConn.write.Len() != 0 {
			t.Fatalf("buffered prime unexpectedly wrote %d bytes directly to socket", f.targetConn.write.Len())
		}
		f.target.wireMu.Lock()
		batchFrames := f.target.channelSceneBatch.frames
		f.target.channelSceneBatch = nil
		f.target.wireMu.Unlock()
		if batchFrames != 1 {
			t.Fatalf("expected 1 buffered mode8 frame in channelSceneBatch, got %d", batchFrames)
		}
		f.target.guild.mu.Lock()
		cached := len(f.target.guild.weddingPrimedPeers)
		f.target.guild.mu.Unlock()
		if cached != 0 {
			t.Fatalf("buffered send must not populate weddingPrimedPeers, got %d entries", cached)
		}

		// 退出批次缓冲后再次调用，必须真正写出到连接并登记缓存。
		f.targetConn.write.Reset()
		if err := s.primeCurrentWeddingGuildRoster(ctx, f.target, viewer, display); err != nil {
			t.Fatalf("post-batch direct prime failed: %v", err)
		}
		if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 1 {
			t.Fatalf("post-batch direct prime expected 1 mode8 frame, got %d", got)
		}
		f.targetConn.write.Reset()
		if err := s.primeCurrentWeddingGuildRoster(ctx, f.target, viewer, display); err != nil {
			t.Fatalf("repeat prime after direct write failed: %v", err)
		}
		if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
			t.Fatalf("repeat prime after direct write expected 0 mode8 frames, got %d", got)
		}
	})

	t.Run("socket_write_failure_not_cached", func(t *testing.T) {
		resetUpperSendFilterCache()
		resetVendorAbsentBlockCache()
		defer func() {
			resetUpperSendFilterCache()
			resetVendorAbsentBlockCache()
		}()
		t.Setenv(upperSendFilterEnv, "")
		t.Setenv(vendorAbsentSendBlockEnvKey, "off")

		f, _, viewer := setupP1WeddingGuildRosterFixture(t)
		s := f.service
		ctx := context.Background()
		bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
		if !ok {
			t.Fatal("missing bound snapshot")
		}
		display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
		if err != nil {
			t.Fatal(err)
		}

		failConn := &failingWriteConn{err: errors.New("injected socket write failure")}
		f.target.conn = failConn
		if err := s.primeCurrentWeddingGuildRoster(ctx, f.target, viewer, display); err == nil {
			t.Fatal("expected write error from failingWriteConn")
		}
		f.target.guild.mu.Lock()
		cached := len(f.target.guild.weddingPrimedPeers)
		f.target.guild.mu.Unlock()
		if cached != 0 {
			t.Fatalf("failed write must not populate weddingPrimedPeers, got %d entries", cached)
		}

		// 验证直接调用 sendCurrentGuildMemberProfileWithPolicyDelivered 在写失败时亦返回 delivered=false 且 err!=nil。
		f2, _, _ := setupP1WeddingGuildRosterFixture(t)
		f2.target.conn = &failingWriteConn{err: errors.New("injected socket write failure 2")}
		access, err := f2.service.loadCurrentGuildMemberProfileAccess(ctx, f2.target, currentGuildMemberProfileRequest{
			channel:   byte(currentTownPresenceChannelID(f2.actor)),
			character: 17,
			directory: f2.service.currentSceneActorRoute(),
		})
		if err != nil {
			t.Fatalf("load access: %v", err)
		}
		delivered, _, sendErr := f2.service.sendCurrentGuildMemberProfileWithPolicyDelivered(ctx, access, true)
		if sendErr == nil || delivered {
			t.Fatalf("failed socket write expected delivered=false and non-nil err, got delivered=%v err=%v", delivered, sendErr)
		}
	})

	t.Run("upper_write_outcome_distinguishes_filtered_buffered_written_failed", func(t *testing.T) {
		resetUpperSendFilterCache()
		resetVendorAbsentBlockCache()
		defer func() {
			resetUpperSendFilterCache()
			resetVendorAbsentBlockCache()
		}()
		t.Setenv(upperSendFilterEnv, "0:601")
		t.Setenv(vendorAbsentSendBlockEnvKey, "off")

		s := &Service{}
		buf := &bufferConn{}
		sess := &gameSession{conn: buf, connID: "outcome-check"}

		if outcome, err := s.writeGameUpperPacketLockedWithOutcome(sess, anchorUpperPacket(601, 0, []byte{1})); err != nil || outcome != upperPacketFiltered {
			t.Fatalf("filtered outcome=%v err=%v, want upperPacketFiltered/nil", outcome, err)
		}
		sess.channelSceneBatch = &currentChannelSceneBatch{started: time.Now()}
		if outcome, err := s.writeGameUpperPacketLockedWithOutcome(sess, anchorUpperPacket(602, 0, []byte{2})); err != nil || outcome != upperPacketBuffered {
			t.Fatalf("scene batch outcome=%v err=%v, want upperPacketBuffered/nil", outcome, err)
		}
		sess.channelSceneBatch = nil
		sess.nativeResultDelivery = &nativeResultDelivery{key: nativeResultDeliveryKey{opcode: 26}, started: time.Now(), lastProgress: time.Now()}
		if outcome, err := s.writeGameUpperPacketLockedWithOutcome(sess, anchorUpperPacket(14, 0, []byte{3})); err != nil || outcome != upperPacketBuffered {
			t.Fatalf("native result delivery outcome=%v err=%v, want upperPacketBuffered/nil", outcome, err)
		}
		sess.nativeResultDelivery = nil
		if outcome, err := s.writeGameUpperPacketLockedWithOutcome(sess, anchorUpperPacket(602, 0, []byte{4})); err != nil || outcome != upperPacketWritten {
			t.Fatalf("direct write outcome=%v err=%v, want upperPacketWritten/nil", outcome, err)
		}
		sess.conn = net.Conn(&failingWriteConn{err: errors.New("boom")})
		if outcome, err := s.writeGameUpperPacketLockedWithOutcome(sess, anchorUpperPacket(602, 0, []byte{5})); err == nil || outcome != upperPacketNotWritten {
			t.Fatalf("failed write outcome=%v err=%v, want upperPacketNotWritten/non-nil", outcome, err)
		}
	})
}

func TestP1GuildPresencePersistMinIntervalOptionParsing(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"off", 0},
		{"false", 0},
		{"none", 0},
		{"180", 180 * time.Second},
		{"3600", 3600 * time.Second},
		{"-1", 0},
		{"3601", 0},
		{"invalid", 0},
	} {
		t.Run("raw="+tc.raw, func(t *testing.T) {
			t.Setenv(guildPresencePersistMinIntervalEnv, tc.raw)
			if got := optionsFromConfig(config.ServiceConfig{}).guildPresencePersistMinInterval; got != tc.want {
				t.Fatalf("raw=%q got=%v want=%v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestP1GuildPresencePersistMinIntervalThrottlesSafelyAndRespectsBoundaries(t *testing.T) {
	s, peers, repos, _ := guildPresenceFixture(t)
	loc := time.FixedZone("UTC+8", 8*3600)
	// 周一 05:59:00 +08:00：距新一周排行边界 06:00 仅 60 秒。
	t0 := time.Date(2026, 11, 16, 5, 59, 0, 0, loc).UTC()
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return t0 }}
	s.options.guildPresencePersistMinInterval = 180 * time.Second

	// 确保成员 "2" 的 JoinedAt 早于 t0。
	reg, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	m2 := reg.Guilds[1].Members["2"]
	m2.JoinedAt = t0.Add(-24 * time.Hour)
	m2.LastOnlineAt = t0.Add(-2 * time.Hour)
	m2.RankingPresence = nil
	reg.Guilds[1].Members["2"] = m2
	if err := dnfguild.Save(context.Background(), repos.Settings, reg, t0); err != nil {
		t.Fatal(err)
	}

	counter := &countingGuildPresenceUOW{
		GuildUnitOfWork:     repos.Guild,
		GuildReadUnitOfWork: repos.Guild.(dnfrepo.GuildReadUnitOfWork),
	}
	repos.Guild = counter
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	peers[0].guild.memberListRequested = true
	peers[1].channel.ID = 12
	peers[0].conn.(*bufferConn).write.Reset()
	owner2 := dnfrepo.GuildOwner{CharacterID: "2", AccountID: "a2"}

	// 1. 首次观察（RankingPresence 为空且距上次 >180s）：必须落库写事务。
	s.persistCurrentGuildPresence(currentGuildPresenceObservation{owner: owner2, at: t0})
	if counter.withinPresenceCalls.Load() != 1 {
		t.Fatalf("first observation expected 1 write tx, got %d", counter.withinPresenceCalls.Load())
	}
	if got := guildPresenceLastSeen(t, repos, "2"); !got.Equal(t0) {
		t.Fatalf("first observation LastOnlineAt=%v want %v", got, t0)
	}

	// 2. 30 秒后（同一排行周、未满 180s）：跳过写事务，但仍必须广播实时在线刷新（解析 opcode 140 与在线频道/离线时长字段）。
	peers[0].conn.(*bufferConn).write.Reset()
	s.persistCurrentGuildPresence(currentGuildPresenceObservation{owner: owner2, at: t0.Add(30 * time.Second)})
	if counter.withinPresenceCalls.Load() != 1 {
		t.Fatalf("throttled observation unexpectedly acquired write tx: count=%d", counter.withinPresenceCalls.Load())
	}
	if channel, elapsed := guildPresenceWireRow(t, guildReply(t, peers[0], 140), 2); channel != 12 || elapsed != 0 {
		t.Fatalf("throttled broadcast opcode=140 member 2 channel=%d elapsed=%d, want channel=12 elapsed=0", channel, elapsed)
	}

	// 3. 70 秒后（周一 06:00:10 +08:00，虽未满 180s 但跨过周一 06:00 排行周边界）：
	// 绝不能被节流，必须落库记录新一周排行在场位。
	tCrossWeek := t0.Add(70 * time.Second)
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return tCrossWeek }}
	s.persistCurrentGuildPresence(currentGuildPresenceObservation{owner: owner2, at: tCrossWeek})
	if counter.withinPresenceCalls.Load() != 2 {
		t.Fatalf("cross-week observation must not be throttled: expected 2 write txs, got %d", counter.withinPresenceCalls.Load())
	}
	if got := guildPresenceLastSeen(t, repos, "2"); !got.Equal(tCrossWeek) {
		t.Fatalf("cross-week LastOnlineAt=%v want %v", got, tCrossWeek)
	}

	// 4. 再过 180 秒（达到最小间隔）：必须落库写事务。
	tAfterInterval := tCrossWeek.Add(180 * time.Second)
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return tAfterInterval }}
	s.persistCurrentGuildPresence(currentGuildPresenceObservation{owner: owner2, at: tAfterInterval})
	if counter.withinPresenceCalls.Load() != 3 {
		t.Fatalf("post-interval observation expected 3 write txs, got %d", counter.withinPresenceCalls.Load())
	}

	// 5. 乱序观察（at 早于当前 LastOnlineAt、位于同一已记录排行周内）：
	// 应安全跳过写事务、不得倒写 LastOnlineAt，且仍广播实时在线状态。
	peers[0].conn.(*bufferConn).write.Reset()
	s.persistCurrentGuildPresence(currentGuildPresenceObservation{owner: owner2, at: tAfterInterval.Add(-30 * time.Second)})
	if counter.withinPresenceCalls.Load() != 3 {
		t.Fatalf("out-of-order same-week observation unexpectedly acquired write tx: count=%d", counter.withinPresenceCalls.Load())
	}
	if got := guildPresenceLastSeen(t, repos, "2"); !got.Equal(tAfterInterval) {
		t.Fatalf("out-of-order observation mutated LastOnlineAt=%v want %v", got, tAfterInterval)
	}
	if channel, elapsed := guildPresenceWireRow(t, guildReply(t, peers[0], 140), 2); channel != 12 || elapsed != 0 {
		t.Fatalf("out-of-order broadcast opcode=140 member 2 channel=%d elapsed=%d, want channel=12 elapsed=0", channel, elapsed)
	}

	// 6a. 观察携带错误账号（与公会成员账号不匹配）：预检直接拒绝，不拿写锁、不广播、不篡改 LastOnlineAt。
	peers[0].conn.(*bufferConn).write.Reset()
	wrongOwner := dnfrepo.GuildOwner{CharacterID: "2", AccountID: "wrong-account"}
	s.persistCurrentGuildPresence(currentGuildPresenceObservation{owner: wrongOwner, at: tAfterInterval.Add(15 * time.Second)})
	if counter.withinPresenceCalls.Load() != 3 {
		t.Fatalf("mismatched guild member account should skip write tx; got %d", counter.withinPresenceCalls.Load())
	}
	if peers[0].conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("mismatched guild member account must not broadcast presence refresh, wrote %d bytes", peers[0].conn.(*bufferConn).write.Len())
	}
	if got := guildPresenceLastSeen(t, repos, "2"); !got.Equal(tAfterInterval) {
		t.Fatalf("wrong-account observation mutated LastOnlineAt=%v want %v", got, tAfterInterval)
	}

	// 6b. 角色表归属账号与公会记录不一致（即使在节流窗口内）：不得命中节流捷径，必须退回权威写事务校验且不广播、不篡改 LastOnlineAt。
	char2Orig, found, err := repos.Character.Load(context.Background(), "2")
	if err != nil || !found {
		t.Fatalf("load char 2: found=%v err=%v", found, err)
	}
	char2WrongAccount := char2Orig
	char2WrongAccount.AccountID = "reassigned-account"
	if err := repos.Character.Save(context.Background(), char2WrongAccount); err != nil {
		t.Fatal(err)
	}
	peers[0].conn.(*bufferConn).write.Reset()
	s.persistCurrentGuildPresence(currentGuildPresenceObservation{owner: owner2, at: tAfterInterval.Add(20 * time.Second)})
	if counter.withinPresenceCalls.Load() != 4 {
		t.Fatalf("character table account mismatch must not take throttled shortcut; expected 4 write txs, got %d", counter.withinPresenceCalls.Load())
	}
	if peers[0].conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("character table account mismatch must not broadcast presence refresh, wrote %d bytes", peers[0].conn.(*bufferConn).write.Len())
	}
	if got := guildPresenceLastSeen(t, repos, "2"); !got.Equal(tAfterInterval) {
		t.Fatalf("character table account mismatch mutated LastOnlineAt=%v want %v", got, tAfterInterval)
	}
	// 恢复角色表原归属账号以便继续后续边界验证。
	if err := repos.Character.Save(context.Background(), char2Orig); err != nil {
		t.Fatal(err)
	}

	// 7. 纯排行月切换边界（2026-12-01 为周二，06:00+08:00 跨月不跨周一）：
	// 先在 05:59:30+08:00 落库，再在 40 秒后（<180s）06:00:10+08:00 观察，绝不能被节流。
	tBeforeMonth := time.Date(2026, 12, 1, 5, 59, 30, 0, loc).UTC()
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return tBeforeMonth }}
	s.persistCurrentGuildPresence(currentGuildPresenceObservation{owner: owner2, at: tBeforeMonth})
	if counter.withinPresenceCalls.Load() != 5 {
		t.Fatalf("pre-month-boundary observation expected 5 write txs, got %d", counter.withinPresenceCalls.Load())
	}
	tAfterMonth := tBeforeMonth.Add(40 * time.Second)
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return tAfterMonth }}
	s.persistCurrentGuildPresence(currentGuildPresenceObservation{owner: owner2, at: tAfterMonth})
	if counter.withinPresenceCalls.Load() != 6 {
		t.Fatalf("cross-month observation must not be throttled: expected 6 write txs, got %d", counter.withinPresenceCalls.Load())
	}
	if got := guildPresenceLastSeen(t, repos, "2"); !got.Equal(tAfterMonth) {
		t.Fatalf("cross-month LastOnlineAt=%v want %v", got, tAfterMonth)
	}
	regAfterMonth, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if p := regAfterMonth.Guilds[1].Members["2"].RankingPresence; p == nil || !p.Month.Equal(dnfguild.RankingMonthStart(tAfterMonth)) {
		t.Fatalf("cross-month RankingPresence=%+v want month %v", p, dnfguild.RankingMonthStart(tAfterMonth))
	}

	// 8. 已软删角色（delete_flag=1）即使在节流窗口内也不得走节流捷径，必须退回权威写事务校验。
	char2, found, err := repos.Character.Load(context.Background(), "2")
	if err != nil || !found {
		t.Fatalf("load char 2: found=%v err=%v", found, err)
	}
	if char2.Stats == nil {
		char2.Stats = map[string]int64{}
	}
	char2.Stats["delete_flag"] = 1
	if err := repos.Character.Save(context.Background(), char2); err != nil {
		t.Fatal(err)
	}
	s.persistCurrentGuildPresence(currentGuildPresenceObservation{owner: owner2, at: tAfterMonth.Add(10 * time.Second)})
	if counter.withinPresenceCalls.Load() != 7 {
		t.Fatalf("deleted character must not take throttled shortcut; expected 7 write tx calls, got %d", counter.withinPresenceCalls.Load())
	}
}
