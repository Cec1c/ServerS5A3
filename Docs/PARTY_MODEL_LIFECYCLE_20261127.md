# 组队与队友模型生命周期服务端修复

本文记录公开服务端基线上与组队、队友模型、原生连接准入和 UDP 中继相关的修复。客户端二进制与离线原生配方由独立修改集维护；本批只修改服务端 Go 路径和公开说明，不包含私服配置、测试地址、玩家数据或部署状态。

## 已公开基线

PR 123 已公开队伍 ID 生命周期、队长转让、端口隔离、有向中继转发、加载准备屏障、25 秒超时处置、按加载周期缓存角色/装备资料以及延迟社交通知。已有的 `party-initial-room-registration`、`party-room-lifecycle-registration` 和 `party-model-readback` 目录继续作为原生模型配方的公开入口。

本批不重复引入这些内容，也不重放已取消的 PR 119 实验。

## 本批新增

- 跨区域、同频道的组队邀请和加入在提交成员关系后、发送 op11/op9 前创建非副本成员的城镇 actor。创建 actor 时不提前发送 party op9，避免端点建立之前出现半份队友状态。
- 邀请接受、队伍目录加入和强制组队统一从发送时读取的同一份权威队伍快照准备 actor 并扇出名单，避免并发入队发生在调用方状态与发送快照之间时先发名单、后建 actor。只有本地名单写入成功的成员继续参与后续区域 actor 投影；副本成员仍走已有的 run-key 准入和幂等门，不混入城镇重建。
- 按接收者准备城镇 actor 保持尽力而为。单个 actor 构建或写入失败不会阻断队伍成立与权威名单发送，因此该接收者仍可能缺少后续模型恢复所需的 CUser，需要在客户端实测中继续观察。
- 客户端重新上报 CS op2 端口后，服务端按成员逐个发送完整队伍快照，再补发 actor 投影。UDP 腿的身份比较只使用角色、会话代次和 TCP 观测地址；作为说明信息的自报端口只更新到现有腿，不因端口重报拆除已学习的中继连接。
- 原生连接准入可按有界轮次重试。每轮使用新 nonce，并保留角色、会话代次、队伍修订和原始请求时间的二次检查；提交阶段按队伍人数获得有界宽限，避免极接近查询截止时间的成功结果被立即判定过期。
- 加载准备从实际准备开始计时。瞬时失败在 250 毫秒后重试，准备超过 5 秒记录慢成员，整体仍受 25 秒权威截止时间约束；超时退出的成员会清掉指向该运行时的待落位记录。
- `partyLoadingCharacterRepository` 转发窄表 `LoadCharacterMirrorStats` 和 `LoadCharacterExtraStats`。已完成的加载周期缓存仍可复用，缓存项尚未完成时回落到原仓储接口。
- 跨场景资料批次按成员数放大总预算并保留单角色读取、单接收者写入的边界；资料身份检查改用只读身份投影。
- 初始组队读条屏障尚未放行时，迟到的回城确认、回城请求和放弃游戏请求保留原状态并记录原因，避免在队友 actor 尚未完成前提前拆场景。

相关原因代码和 `town_return_pending`、`town_return_req_msg_id`、`town_scene_tail_deferred` 会出现在既有启动器诊断上下文中。该上下文只读当前服务端状态，不新增命令入口。

- 团本改名只向修改者补发资料，再广播名单；重入按成员定向刷新外观签名与资料，清除旧视图。资料准备预算随人数增长，预算过期仍交付权威名单。
- 普通加载完成的回城路径在无活动副本时结算回城状态，与 op35/op37 的确认处理一致。

## 原生配方依赖

本次服务端修复不修改客户端代码。公开配方入口及其直接依赖为：

- `scripts/client/party-initial-room-registration/build_candidate.py`
- `scripts/client/party-initial-room-registration/verify_candidate.py`
- `scripts/client/party-room-lifecycle-registration/build_candidate.py`
- `scripts/client/party-room-lifecycle-registration/verify_candidate.py`
- `scripts/client/quiet-profile-refresh/build_candidate.py`
- `scripts/client/dungeon-peer-retire/helpers.py`
- `scripts/client/party-model-readback/collect.py`

其中初始落位构建器引用后两项 helper，生命周期构建器引用初始落位构建器。当前公开基线已包含这些文件；主发布记录负责给出对应的 Git blob 对照。

## 范围边界

本批排除只用于私服的强制 UDP 测试开关和地址、安徒恩本地模拟、队伍击杀经验批处理、账号/活动/Wave 修改、私有 EXE/PVF、运行配置和部署回执。

## 验证入口与结果

本批在 Linux 环境串行完成相关定向回归、vet 与完整服务端构建。可复用的回归入口为：

```text
go test ./internal/services/dnfbridge -run 'Test(CrossArea|ManagedPartySnapshots|PartyUDPRelayReportTrafficTickOnlyWarnsOnDelta|OrdinaryPartyNativeAdmission|PartyNativeConnection|PartyLoadingPreparation|PartyPreflight|CompletedTownReturn|StaleBackToVillage|LatePartyEntry|LauncherDiagnostics|NativePartyConnection)' -count=1
go vet ./internal/services/dnfbridge
```

完整结果和输入摘要由本次发布回执记录。离线回归不替代四人首次进图、跨区域邀请、读条超时、回城和重进的画面验收。
