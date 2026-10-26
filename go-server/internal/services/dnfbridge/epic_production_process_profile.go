package dnfbridge

// Fixed v103 (6418373c...e566), class1/1417: B21342/B21351/B2135B
// read charge, weekly used count, and big-success effect flag respectively.
// B1C800 and B1E110 compare/subtract the count against config weekly limit
// plus the state's extra allowance; it is not the big-success boolean.
func (s *Service) buildCurrentEpicProductionProcessSuccessBody(result currentEpicProductionProcessResult) []byte {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return buildCurrentEpicProductionProcessSuccessBody(result)
	}
	var body packetWriter
	body.writeUint32(result.ChargePoint)
	// commitCurrentEpicProductionProcess bounds the next committed count to U8
	// before consuming materials. Serialize that transaction result, not a reload.
	body.writeByte(byte(result.WeeklyCount))
	if result.BigSuccess {
		body.writeByte(1)
	} else {
		body.writeByte(0)
	}
	return body.bytes()
}
