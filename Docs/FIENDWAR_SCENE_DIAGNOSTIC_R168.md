# R168 超时空场景诊断：run/频道识别与换房 finish

基底：R165-A 22f93dc329747287172b71097c2898e146067515。本候选只调整超时空诊断与对应测试/说明，不改移动、出生、组队、结算或 PVF。

## 现场问题

R166 的 game-fiendwar-scene-diagnostic 只接受 Dungeon.ID == 4953/4978。2026-10-21 20:45 的完整窗口实际出现 4975（作战区域-10）、4964（作战区域-14）等 FiendWar run，却没有 4953/4978 runtime，因此三条诊断路径都被硬编码门禁提前挡掉。空日志不能作为“没有进图”的证据。

## 修改

- 诊断身份条件改为：当前频道是已证 FiendWar 频道类型 42，且 runtime 的 raidEntry 存在、ChannelType 与当前频道一致、ChannelID 与当前频道一致、RunID 非空。不再枚举地图 ID。
- 每场景最多记录一次 post_start_placement、一次 finish_loading_observed、一次成功发送的 finish_loading_placement、两条 client_position；场景键继续包含 dungeon/maze/room/map/seed 与 raid instance/run。
- sendCurrentPostFinishLoadingPlayerState 在 postFinishLoadingPlayerStateSent 为 true 时提前返回；该分支只记录 finish_loading_observed，并明确写 placement_sent=false，不写 actor_slot/placement_seed。它不占用成功发送事件的限频字段。
- 正常首房或真正完成发送的路径才记录 finish_loading_placement，写 placement_sent=true；该事件与 observed 各自每房间最多一条，因此观察到请求不会吞掉后续真实成功事件。
- 日志仍不包含包体、账号凭据或任意错误内容，只保留连接、频道、角色、stage、dungeon/maze/room/map/seed、raid key/instance/run、ready 标志和两个限频坐标。

## 边界

这不是移动或组队修复，不改变 4975/4964 的选图、坐标、出生资源、准入或结算。回归正例表覆盖 4953/4978/4964/4975 四图；非 FiendWar 频道、缺少 run、频道不一致或空 RunID 均不记录。负例各自换新场景并清诊断状态，避免被先前已消耗的限频假绿。

本候选按主线程约束未运行 Go、未构建、未部署；格式化、编译、目标测试和累计验证由主线程串行执行。

本阶段没有增加 scene_entry：现有提交点分布在 leader、prepared follower 与 op29 尾段，单纯从诊断层无法在不触碰业务路径和未验证分支的前提下证明唯一提交时刻，因此不扩。

## 文件

- go-server/internal/services/dnfbridge/fiendwar_scene_diagnostic.go
- go-server/internal/services/dnfbridge/scene_finish_loading.go
- go-server/internal/services/dnfbridge/fiendwar_scene_diagnostic_test.go
- Docs/FIENDWAR_SCENE_DIAGNOSTIC_R168.md
- changlog.md
