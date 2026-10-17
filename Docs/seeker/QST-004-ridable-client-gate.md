# QST-004：296原生生成通知准入候选

固定输入V128 SHA256为`ea2f763927c8c993cc9f5cf41eb85ea37d5a50760ca310706497e5419bbc5912`。其NOTI296有完整20字节reader和共享START_MAP对象队列，但接收位关闭。此候选仅开放该操作号并重新计算PE checksum。

输出SHA256为`07aa4a463682c095858f1afe74d54d45ea8bd71d37d761dfcb4b4ad8733aafc8`，111808512字节。只改变offset393的checksum字节及offset111747877的接收位。输入文件保持原样；错误摘要、截断和重复应用拒绝。

## 生成与验证

依赖Python、pefile、Unicorn。使用完整摘要匹配的实际EXE：

```text
python deploy/client/ridable_spawn_gate.py <V128-DNF.exe> <new-DNF.exe> --receipt <new-build.json>
# 为当前shell设置 DNF_RIDABLE_CLIENT_FIXTURE=<V128-DNF.exe>
python -m unittest discover -s deploy/client -p test_ridable_spawn_gate.py -v
```

3项测试通过：完整原内容保持和独立checksum复核、输入负例、65536操作号原生准入前后对照。只有296变化，316/317仍不准入，475及原有补丁保持。

在候选上追加35项有限原生测试通过，包括316发送、296读取/消费、Monster定义键、host切换，以及实际Go builder导出的三条20字节包。包体依次为五个u32：坐标X/Y、Monster定义编号、阵营、创建类型；61235使用类型0。仅当前名册host指向本地成员时入队，非host不生成。native reader消耗20字节且目标创建参数与Go来源一致。外部渲染、分配及非相关UI使用明确桩。

## 交付状态与验收目标

此候选未发布，未改变测试服玩家资源。服务端目前未接入296发送；是否再生及其调度、重复3报告、过门迟到和缓存重进须由生命周期方案确定。state2在客户端本地立即上车，不等待服务端应答，因此开放296不能单独称为上车修复。

生命周期方案确认后，再按双Release、实际玩家通道、完整下载核验、依赖服务端部署的顺序交同步角色。用户实测正常上车/下车、销毁后生成、重复进出和组队切主，逐项记录。北门与南门/APC问题分别验收。
