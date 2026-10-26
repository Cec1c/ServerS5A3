package dnfbridge

const official249EpicProductionInfoMsgID = 1354

// v103 DNF.exe SHA256 6418373c...e566 registers class0/1354 at
// 0x00B218E8 -> 0x00B20CC0 (u32 prefix + 28-byte epic state).
// Its class0/1355 instead targets 0x02035A90, which reads only four bytes.
// Keep the vendor/90cn notice ID for every other profile.
func (s *Service) epicProductionInfoMsgID() uint16 {
	if s != nil && s.options.currentClientVersion == "official-24.9" {
		return official249EpicProductionInfoMsgID
	}
	return currentEpicProductionInfoMsgID
}
