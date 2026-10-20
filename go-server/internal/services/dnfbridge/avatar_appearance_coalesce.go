package dnfbridge

import (
	"fmt"
	"time"
)

const (
	// currentAvatarAppearanceCoalesceWindow 是"连点"判定窗口的默认值。窗口内的第一次
	// 时装穿脱仍旧同步下发外观刷新，紧随其后的多次只排一次"尾随刷新"，避免每点一件
	// 都让客户端重建一次本地角色（线上实测：穿脱时装时每次 op19 都会紧跟一个
	// class0/op2 的 473 字节角色体，连续穿几件就卡得厉害）。
	//
	// 2026-09-28 第一轮：这条合并曾**只**作用在
	// DNFBRIDGE_EQUIP_APPEARANCE_REFRESH=mode1 回退档（mode0 当时每次穿脱都同步发，
	// 因为那批的报障是"模型不实时刷新"，延迟合并会让"点一下立刻看到"落空）。
	//
	// 2026-09-28 第二轮（操作者："现在穿戴时装模型会跟着变 但是我对穿上脱下的速度
	// 不太满意 能优化吗"）：mode0 已经是**真正刷新模型**的那条包，所以不能再靠
	// mode1/mode0 切换省包，改用 **leading + trailing**：窗口内的第一次照旧同步发
	// （单件穿脱仍然是"点一下立刻看到"，一个包都不多），窗口内紧随其后的几次合并成
	// 窗口末尾的一次尾随刷新。N 连点 ⇒ 最多 2 个 mode0，而不是 N 个。
	// 窗口可用 DNFBRIDGE_EQUIP_APPEARANCE_COALESCE_MS 调整，用
	// DNFBRIDGE_EQUIP_APPEARANCE_COALESCE=off 一键关掉（逐字节回到第一轮）。
	// 证据与档位口径见 equip_move_refresh.go 的 equipAppearanceCoalesceEnv 长注。
	currentAvatarAppearanceCoalesceWindow = 200 * time.Millisecond
	// currentAvatarAppearanceTrailingDelay 是尾随刷新的默认延迟：最后一次穿脱之后
	// 再补一次刷新，保证最终外观正确（这正是之前"时装消失"的修复不能丢的部分）。
	currentAvatarAppearanceTrailingDelay = 200 * time.Millisecond
)

// avatarAppearanceCoalesceWindow 回答当前的连点窗口：优先取
// DNFBRIDGE_EQUIP_APPEARANCE_COALESCE_MS，未配置/非法时回落到内置默认。
// 零值口径与 options.go 的其它开关一致：options 零值 = 默认 200 ms。
func (s *Service) avatarAppearanceCoalesceWindow() time.Duration {
	if s == nil || s.options.avatarAppearanceCoalesceWindowMillis <= 0 {
		return currentAvatarAppearanceCoalesceWindow
	}
	return time.Duration(s.options.avatarAppearanceCoalesceWindowMillis) * time.Millisecond
}

// avatarAppearanceBurstCoalesceEnabled 回答 mode0 档是否启用 leading+trailing 合并。
// 默认启用；只有显式写 DNFBRIDGE_EQUIP_APPEARANCE_COALESCE=off 才关掉，
// 关掉后 mode0 档逐字节回到 2026-09-28 第一轮（每次穿脱都同步发一个 mode0）。
// mode1 回退档不受这个开关影响：它一直有自己的 200 ms 合并（见调用点注释）。
func (s *Service) avatarAppearanceBurstCoalesceEnabled() bool {
	return s != nil && !s.options.avatarAppearanceCoalesceOff
}

// shouldCoalesceAvatarAppearance 记录本次外观刷新时间，并回答"这次是否属于连点"。
// 无论同步还是尾随都会调用它，所以窗口是跟着真实发送刷新的。
func (s *Service) shouldCoalesceAvatarAppearance(session *gameSession) bool {
	if s == nil || session == nil {
		return false
	}
	now := time.Now()
	window := s.avatarAppearanceCoalesceWindow()
	session.avatarAppearanceMu.Lock()
	defer session.avatarAppearanceMu.Unlock()
	coalesce := !session.avatarAppearanceLastAt.IsZero() &&
		now.Sub(session.avatarAppearanceLastAt) < window
	session.avatarAppearanceLastAt = now
	return coalesce
}

// avatarAppearanceTrailingDelay 回答"最后一次穿脱之后等多久补上最终外观"。
//
// 未配置 DNFBRIDGE_EQUIP_APPEARANCE_TRAILING_MS 时**逐字节等于改动前**（= 连点窗口）。
// 之所以要能和窗口拆开：窗口回答的是"两次穿脱隔多久还算同一次连点"，尾随延迟回答的是
// "收手之后多久看到最终外观"。两者绑在一起时，把窗口调大到能盖住操作者真实的
// 0.45–3 s 间隔（2026-09-28 实测），最终外观就会被同样拖到 3 s 才出现，得不偿失；
// 拆开之后可以配成「窗口 3000 ms（整身换装算一次连点）+ 尾随 250 ms（收手就立刻看到）」。
func (s *Service) avatarAppearanceTrailingDelay() time.Duration {
	if s != nil && s.options.avatarAppearanceTrailingMillis > 0 {
		return time.Duration(s.options.avatarAppearanceTrailingMillis) * time.Millisecond
	}
	if window := s.avatarAppearanceCoalesceWindow(); window > 0 {
		return window
	}
	return currentAvatarAppearanceTrailingDelay
}

// scheduleTrailingAvatarAppearance 排一次尾随外观刷新；同名定时器覆盖前一次，
// 所以一次连点只会在末尾发一帧。定时器队列不可用时返回 false，调用方回退成同步发送，
// 绝不出现"完全不刷新"的回退。
func (s *Service) scheduleTrailingAvatarAppearance(session *gameSession, operation string) bool {
	if s == nil || session == nil || session.connID == "" {
		return false
	}
	queue := s.ensureGameplayTimeQueue()
	if queue == nil {
		return false
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return false
	}
	name := fmt.Sprintf("avatar-appearance-trailing-%s", session.connID)
	session.avatarAppearanceMu.Lock()
	session.avatarAppearanceTimerSerial++
	serial := session.avatarAppearanceTimerSerial
	session.avatarAppearanceTimer = name
	session.avatarAppearanceMu.Unlock()
	delay := s.avatarAppearanceTrailingDelay()
	if delay <= 0 {
		delay = currentAvatarAppearanceTrailingDelay
	}
	queue.ScheduleAfter(name, delay, func(time.Time) {
		session.equipmentProjectionMu.Lock()
		defer session.equipmentProjectionMu.Unlock()
		session.avatarAppearanceMu.Lock()
		if session.avatarAppearanceTimerSerial != serial {
			session.avatarAppearanceMu.Unlock()
			return
		}
		session.avatarAppearanceTimer = ""
		session.avatarAppearanceMu.Unlock()
		if !s.boundGameSessionCharacterCurrent(identity) || !currentTownSelfActorAppearanceReady(session) {
			s.logWarn("dnfbridge coalesced avatar appearance refresh deferred",
				"conn_id", session.connID,
				"operation", operation,
				"char_id", session.selectedCharacterID)
			return
		}
		sent, err := s.sendSelectedActorMode0AppearanceRefreshGuarded(session, operation, "coalesced_avatar_wear_burst_trailing", func() bool {
			session.avatarAppearanceMu.Lock()
			current := session.avatarAppearanceTimerSerial == serial
			session.avatarAppearanceMu.Unlock()
			return current && s.boundGameSessionCharacterCurrent(identity) && currentTownSelfActorAppearanceReady(session)
		})
		if err != nil {
			s.logWarn("dnfbridge coalesced avatar appearance refresh deferred",
				"conn_id", session.connID,
				"operation", operation,
				"char_id", session.selectedCharacterID,
				"error", err)
			return
		}
		if !sent {
			return
		}
		s.logPacketEvent("game-avatar-appearance-coalesced-send",
			"conn_id", session.connID,
			"operation", operation,
			"char_id", session.selectedCharacterID,
			"reason", "trailing_refresh_after_avatar_wear_burst")
	})
	return true
}

// stopAvatarAppearanceCoalesce 在会话断开/切换角色时取消待发的尾随刷新，
// 避免给已经关掉的连接发包。
func (s *Service) stopAvatarAppearanceCoalesce(session *gameSession, reason string) {
	if s == nil || session == nil {
		return
	}
	session.avatarAppearanceMu.Lock()
	name := session.avatarAppearanceTimer
	session.avatarAppearanceTimerSerial++
	session.avatarAppearanceTimer = ""
	session.avatarAppearanceLastAt = time.Time{}
	session.avatarAppearanceMu.Unlock()
	if name == "" {
		return
	}
	if queue := s.ensureGameplayTimeQueue(); queue != nil {
		queue.Cancel(name)
	}
	s.logPacketEvent("game-avatar-appearance-coalesce-stopped",
		"conn_id", session.connID,
		"char_id", session.selectedCharacterID,
		"timer_name", name,
		"reason", reason)
}
