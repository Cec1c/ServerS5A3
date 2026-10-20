# op40 严格零尾兼容

当前 upper 入口接受 `x:u16,y:u16` 或恰好追加 `zero:u16` 的六字节请求；非零尾、短包、长包仍拒绝。所有 active、所选角色归属、lifecycle 与 town-return 守卫保持，completed 的首次/迟到死亡仍拒绝。legacy 原有零尾裁剪保持。

原生依据为精确 v150 EXE SHA256 `f781c423eaad8ccdb0327a864d525f0ee0436663399eb809a09aa08bf390e61b`：`2771A62` 构造 op40，非 15h 分支 `2771B02` 追加零，`2771F95` 经真实 u16 writer 写出；另一路 `2D70011` 只有两个坐标。非零扩展可能来自相关对象，不将它解释成无用填充。原始指令验证的模式查询/对象/坐标来源是夹具，未运行 GUI/真实发送；不能据此确定现场模式。

完整 handler 回归使用内存 repository、合成普通副本与 fake timer，无私有 PVF、EXE、数据库或绝对路径依赖。新增测试覆盖两种合法形状、长度/非零尾/class、owner/completed、重复死亡计时器代次、复活扣币、保存失败及恢复。current/legacy 两入口分别在 `DNFBRIDGE_PARTY_DEATH_RETURN_ROOM_TRANSITION` 空值/keep/legacy 下验证准入与十秒回城冻结原点，测试自行设置并恢复环境。无并行全局环境写入。

在 `go-server` 使用任意可用 Go 命令运行：

```text
go test -buildvcs=false -count=1 -timeout=180s -run '^Test(R146Death|R134Completed|CurrentDungeon(DieCharacter|Death|UseCoin)|LegacyDungeonDieCharacter)' ./internal/services/dnfbridge
```

重复 op40 保持既有 rearm/重复通知语义，只有一个活计时器，旧 callback 失效；不是新通知去重或 nonce 幂等方案。已复活后在 active 内到达的相同 op40 仍不能区分新死亡与旧声明，本次没有建立新的生命证明。完成后保留的既有死亡 owner 可复活的 R134 规则不变。本项不修复 P126 迟到死亡，不证明史诗未拾提示或客户端界面恢复。
