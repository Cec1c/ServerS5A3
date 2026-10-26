package dnfbridge

// currentAttendanceMarbleMonsterExperienceBonus applies only the numeric
// effects proved by event/attendancemarble.evt. Buff 30 doubles monster EXP;
// Buff 32 adds 50 percent. The bonuses are additive when both markers are
// active, matching the event's separate monster/clear descriptions.
func currentAttendanceMarbleMonsterExperienceBonus(mask byte, base uint32) uint32 {
	percent := int64(0)
	if mask&(1<<(attendanceMarbleBuffMonsterExp-29)) != 0 {
		percent += 100
	}
	if mask&(1<<(attendanceMarbleBuffClearHunt-29)) != 0 {
		percent += 50
	}
	return currentPercentageBonusUint32(base, percent)
}

// currentAttendanceMarbleClearExperienceBonus applies the event's numeric
// clear EXP effects. Buffs 31 and 32 each provide +100 percent clear EXP.
func currentAttendanceMarbleClearExperienceBonus(mask byte, base uint32) uint32 {
	percent := int64(0)
	if mask&(1<<(attendanceMarbleBuffClearExp-29)) != 0 {
		percent += 100
	}
	if mask&(1<<(attendanceMarbleBuffClearHunt-29)) != 0 {
		percent += 100
	}
	return currentPercentageBonusUint32(base, percent)
}
