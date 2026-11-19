---
category: 功能修复
---

# 游戏主服高频 SQL 窄化、生产诊断日志分级与组队 UDP 套接字缓冲区治理

## 变更

- 在 mysql_guild_snapshot_revision.go 实现同一 REPEATABLE READ 事务快照下的两阶段公会注册表指纹读取，命中未变更指纹时免除 dnf_setting_values LONGTEXT 溢出页读取，同时防护 TRUNCATE 后孤儿侧车行并保留缺表/权限回退。
- 在 mysql_character_wedding_read.go 将角色婚姻窄读拆分为 dnf_characters 主键点查与 dnf_character_stats 聚簇索引范围扫描，消除每次查询的 Using temporary; Using filesort，并保持事务内 FOR UPDATE 锁语义与空属性行兼容。
- 在 dnfbridge 新增 DNFBRIDGE_VERBOSE_DIAGNOSTICS 分级策略：结构体字面量零值保持全量诊断以兼容既有单测，optionsFromConfig 生产默认抑制高频逐帧/逐怪明细，保留 >=20ms 慢城镇构造、组队/团本拒绝与失败事件，并对未知 opcode 执行有界首次采样（上限 256）。
- 在 party_udp_relay.go 与 party_udp_relay_probe_listener.go 双路径统一接入 DNFBRIDGE_PARTY_UDP_SOCKET_BUFFER_BYTES（默认请求 2 MiB），显式设置读写缓冲区并通过 getsockopt 读回实际 SO_RCVBUF/SO_SNDBUF 字节数与单次截断告警。

## 验证

- 开发机 101 Linux 环境执行 go mod verify、go vet、正式 DNF90Server (-tags timetzdata) 构建、dnfbridge 定向回归测试与真实 MariaDB 11.8.8 集成测试通过；未在生产环境部署。
