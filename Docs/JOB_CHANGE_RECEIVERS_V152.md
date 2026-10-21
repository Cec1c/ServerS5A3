# DNF 客户端 v152：转职书接收开放（验收版）

基于最新已发布的 `client-v151-aiken-receiver-20261021`，仅新增以下客户端接收组合：

| 分类 | 编号 | 原生处理器 |
| --- | --- | --- |
| class1 | 502 | 0x010AB4C0 |
| class1 | 785 | 0x010AB690 |
| class0 | 1150 | 0x010AB7D0 |

按 class 与编号精确放行，不修改共用位图。其他组合继续调用原接收链，包括另有处理器的 class0/785。原有 v151 艾肯升级及更早版本接收适配保留。

输入 v151：111845376 字节，SHA256 `2def3cc35c1ee00a9c697fe81d1f31259860468d042d6d477cd3b4be08f0e6eb`。

输出 v152：111849472 字节，SHA256 `44aecee7c48bc1c89582ef1cd95694950569951caeef3b22ec3478c917b8ce7e`。

新增 74 字节接收适配器，位于新 `.jobgate` 节。接收调用点转到新适配器，其余既有节内容不变；PE 头的节数、代码大小、映像大小和校验和同步更新。独立验证回执随包提供。

验证结果：每个镜像执行 131830 个真实调用点组合，包含 class0、class1 各 65536 编号及 class2、class255 各 379 个探针。差异恰好为上述三项由拒绝变允许，无既有准入被移除。检查返回值、栈清理及非易失寄存器；官方基线 ZIP/内嵌 EXE 摘要、候选复建一致性、错误基线拒绝、Windows ImageHlp 校验和均通过。原生回调之后的完整 UI 未在模拟中执行。

本包包含 DNF.exe、补丁源码、验证器和发布清单。沿用 v151 实际配套 PVF：`25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348`；频道摘要：`8a207391716a5c8363c1b292d14588256cdddc0e251ead59ac87d686b6eb7eae`。本包不含 PVF，也不更新服务器或登录器的签名更新通道。

验收范围：客户端现在可以接收上述三个组合；转职书实际使用、职业状态刷新及重新登录后的保存仍需配套服务端与游戏内验收。此次未执行完整转职 GUI 或修改转职消耗/资格规则。固定开放孵化234属于另一项服务端修改，不在此EXE中。

离线复建（Python 3）：

```text
python scripts/patch_job_receivers.py --input exact-v151.exe --output rebuilt-v152.exe --receipt rebuilt.json
python scripts/verify-v152.py --baseline exact-v151.exe --candidate rebuilt-v152.exe --receipt rebuilt.json --json-out verification.json
```

补丁脚本仅接受上述精确 v151，输出与回执路径须为新文件。原生验证器使用 Unicorn，运行参数见 `python scripts/verify-v152.py --help`。验证器为离线模拟，不启动游戏。
