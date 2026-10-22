# op40 严格零尾兼容

当前 upper 死亡声明接受两种形状：两个 u16 坐标（4B），或坐标后恰好追加一个零 u16（6B）。非零扩展、短包、长包均拒绝。legacy 原有严格零尾裁剪保持；所有 active、角色归属、lifecycle 与回城守卫保持。

精确 v150 SHA256 `f781c423eaad8ccdb0327a864d525f0ee0436663399eb809a09aa08bf390e61b` 的 `2771A62` sender 在非15h分支 `2771B02` 追加零，`2771F95` 经真实 u16 writer 写入；另一路 `2D70011` 只写两个坐标。非零扩展可能携带相关对象，不能当填充丢弃。证据来自有界离线原始指令执行；模式/对象/坐标来源为夹具，未验证真实客户端调度、socket 或 GUI。

公开 master 基底没有已接受死亡跨 completed 状态后继续复活的能力。此次只改 op40 包形状，不移入该额外能力；测试明确保留 completed/op40 与 completed/op41 拒绝。active 重复死亡保持既有 rearm/重发通知行为，只有一个活计时器，旧回调失效；不是新增通知幂等或死亡代次证明。

完整入口测试使用公开仓库已有内存 repository、合成普通副本和 fake timer，无私有资源、绝对路径、外部数据库依赖。current/legacy 两入口都覆盖环境 `DNFBRIDGE_PARTY_DEATH_RETURN_ROOM_TRANSITION` 空值/keep/legacy，使用 `t.Setenv` 自动恢复。

从 `go-server` 运行：

```text
go test -buildvcs=false -count=1 -timeout=180s -run '^Test(R148Death|CurrentDungeon(DieCharacter|Death|UseCoin)|LegacyDungeonDieCharacter)' ./internal/services/dnfbridge
```

测试覆盖4B/6zero、非法包/class、owner、完成态、复活扣币、保存失败及恢复、计时器代次与冻结城镇原点。P126迟到死亡、active复活后旧声明与新死亡区分、史诗未拾提示和客户端界面仍未解决。
