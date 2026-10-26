# D09 安徒恩阻截 phase0 双牌时序

## R182 当前证据与修正

普通安徒恩phase0的小队牌和kind3沿用同一已打开面板，不为kind3再发送587 state4。v151 `202FCC8..202FCF4` 在面板`+420 != 0`时调用`192EEA0`更新；`32B6575..32B658B`确认重复state4跳过首次打开函数，但其外层仍有其他状态重置和容器清理。重连新会话仍须先发送state4，随后才发已提交kind3。

真实PVF回归`TestR182AntonPhase0SquadRetainsOpenScreenAndRestoresNewConnection`在修改前观察到kind3前多出一条state4，失败于预期0/实际1。修改只覆盖普通安徒恩phase0，保留灭杀、单人和卢克行为。验证结果由本轮回执记录；完整原生动画、手动翻牌仍需实机验收。

下文历史“自然659”的名称不作为原生触发原因的证明。实际`18FBF20`按`this+3FD`发送零值，其caller `23FB410`还有输入检查；`18FBED0`按参数与`this+3FC`决定发送1并清两个标志。协议零值不能唯一证明自然电影结束。kind3分支页签5使用的真实资源是`REWARD_BOX_GLITTER`，不能把页签5命名为团队页。历史结论与上述冲突时以上述实际字节为准。

## r167 当前结论

正常安徒恩 phase0 有两段展示：party 牌与 squad/team 牌。旧实现把 party 自动选择截止固定在 CompletedAt + 2*MovieEndSeconds；CompletedAt 是服务端清关时刻，实际牌面却要等客户端结果电影结束后发送自然 659 才下发。现场晚到时会出现“自动先提交、牌面后显示”，首轮因此可能直接看到已发奖结果。

r167 只收窄这一时序：

- 成功发送 party 600 后记录一次 PartyShownAt，作为实际牌面下发锚点。
- 自动 party 选择的实际截止为 max(AutoSelectAt, PartyShownAt + MovieEndSeconds)。
- 尚未收到任何真实 party 牌面时，不在 AutoSelectAt 直接提交；自动投影先在 AutoSelectAt 下发未提交的 party 牌面，再保留一个有界电影窗后回退提交，避免无人响应时永久卡住。
- party 提交后仍等待一个电影窗再自动生成/显示 squad；手动 660 的完成时刻继续作为其 party 展示窗口起点。

未修改奖励表、资产事务、最低参与人数、owner、组成员、周账本、659/660 请求格式、587/600 包布局或 phase1。未删除 kind0/1/3 任一种已存在的牌面类型。

## 自然 659/660 链路

当前 v151 正常路径为：

1. 清关后服务端发送结果 601 和阶段动画 587 {3,0}。
2. 客户端播放结果电影；自然 659 在电影完成后发送，body 为 {0}。
3. 服务端处理 handleCurrentAntonMovieCompletion：回 659 ACK、587 {4,0}，以及 kind1 item list 和 kind0 gold list 两套 600。牌面 owner 只取同组成员，最多四人。
4. 玩家选择 party 牌时发送 660 {1, card}；服务端授予一张 party 卡并回 600 kind1 和 660 ACK。自然 660 没有 squad 选择分支。
5. squad/team 牌由服务端在 party 提交后自动冻结并以 600 kind3 显示。当前实现没有把 660 解释为第二段手动选择；“后轮不能手动”未被本补丁证明为原生缺陷，也未新增未证协议。

## 真实入口证据

- 实际 PVF SHA256 25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348；v151 SHA256 2def3cc35c1ee00a9c697fe81d1f31259860468d042d6d477cd3b4be08f0e6eb。
- v151 静态：659 注册 0x15707D3->0x15705F0，660 注册 0x15707EB->0x156D500；自然 659 发送路径含 0x18FBF29 与零值字段 0x18FBF3A，660 写入器 0x00F19600。NOTI600 解析 kind、owner、cardtype、itemcount 及每项 8 字节描述。
- 原生 0x1929A60 执行映射：kind0/1→类别0，kind2→1，kind3→2；kind0 被消费者正常接受，不能从没有 gold pool 推导出删除 kind0 合法。
- r165 现场只读摘录 H:/codex-work/20261021/r165/raid-rewards-worker/evidence/live-log-findings.md 已指出：自动选牌仅以 AutoSelectAt=CompletedAt+2*movie 为门，没有绑定实际牌面下发时刻，晚到结果会造成先提交后显示。

## 回归与边界

- 新增 go-server/internal/services/dnfbridge/r167_anton_phase0_show_timing_test.go：真实 PVF 下验证自动投影先发未提交 party 牌面，party 在 PartyShownAt + movie 才提交；晚到自然 659 同样先显示再提交；无客户端响应时在 AutoSelectAt + movie 有界回退。
- 原有真实 PVF 单/双人、4 人同组、4+1 多组、自然 660 单卡、重连不重发、squad 容量及 R158 迟到路径继续保留；测试夹具显式模拟“完成时刻后一个电影窗的 659 展示”，专用 r167 用例会清空该锚点以覆盖晚到/缺失路径。
- 未运行 Go 测试或构建，未部署、未 push/merge；未执行 GUI/动画验收。主线程需串行运行 go test/go build。
- PartyShownAt 是 flow 级首个 party 下发锚点，不是每个分组或每个连接独立时钟；多组同时展示时，一个组的首个 600 会作为整个 flow 的窗口起点。该边界需在实机分组场景复核。

## 主线程定向验证命令

从 H:/codex-work/20261021/r167/anton-worker/go-server 执行：

$env:DNFBRIDGE_REAL_PVF_SMOKE='H:/codex-work/assets/Script.pvf'
go test -buildvcs=false -count=1 ./internal/services/dnfbridge -run 'TestRealPVFAntonPhase0|TestRealPVFAntonAutomatic|TestRealPVFAntonFlow' -v

完整 bridge 回归与全仓构建按项目约定由主线程串行执行；本阶段不提供通过结论。
