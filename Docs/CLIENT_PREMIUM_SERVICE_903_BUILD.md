# 魔王契约 class1/op903 接收许可构建说明

固定基底为正式发行 `client-v163-pe-image-gap-20261024`。`scripts/client/premium-service-903/` 只生成并验证独立候选，不安装、不发布、不修改玩家通道。补丁说明见 [CLIENT_PREMIUM_SERVICE_903.md](CLIENT_PREMIUM_SERVICE_903.md)。

- 输入 EXE SHA256：`b9fec733823dc95e1d6fd24c139f47b00a2fbcff57f913612cdfa34101d83902`
- 输出 EXE SHA256：`c0f55b32f9e4680e5d5be418430c33e56b0fc955e958f6b642acac9a1da67b80`
- 输入、输出大小：111894528 字节。
- 构建器只依赖 Python 3.10+ 标准库；原生接收验证另需 `unicorn==2.1.4`。真实 EXE 不进入源码仓库。

`build_candidate.py` 接受固定输入、最新输出目录和回执路径；`verify_candidate.py` 接受输入、候选、候选回执及新的原生验证回执。构建器拒绝摘要不符、重复应用、旧指令变化、非空尾部空间及既有输出目录；验证器要求候选完整字节等于重新推导的唯一输出。

Windows 专项使用 `SEC_IMAGE_NO_EXECUTE` 映射候选，不执行游戏入口。没有真实输入时的条件跳过不算专项通过。接收验证执行真实许可门和分类分流，正文解码为显式跳过夹具；结果不能替代完整 903 回调、资源或游戏 GUI 验收。
