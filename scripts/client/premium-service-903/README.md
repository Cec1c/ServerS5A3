# 魔王契约 class1/op903 接收许可

固定基底为正式发行 `client-v163-pe-image-gap-20261024`。本目录只生成并验证独立候选，不安装、不发布、不修改玩家通道。补丁说明见 [CLIENT_PREMIUM_SERVICE_903.md](../../../Docs/CLIENT_PREMIUM_SERVICE_903.md)。

- 输入 EXE SHA256：`b9fec733823dc95e1d6fd24c139f47b00a2fbcff57f913612cdfa34101d83902`
- 输出 EXE SHA256：`c0f55b32f9e4680e5d5be418430c33e56b0fc955e958f6b642acac9a1da67b80`
- 输入、输出大小：111894528 字节。
- 构建器仅依赖 Python 3.10+ 标准库；原生接收验证另需 `unicorn==2.1.4`。真实 EXE 不进源码仓库。

```text
python -B scripts/client/premium-service-903/build_candidate.py --input <v163-DNF.exe> --out-dir <新的候选目录>
python -B scripts/client/premium-service-903/verify_candidate.py --input <v163-DNF.exe> --candidate <候选目录/DNF.exe> --receipt <新的原生验证回执.json>
```

构建器拒绝摘要不符、重复应用、旧指令变化、非空尾部空间及既有输出目录；验证器要求候选完整字节等于重新推导的唯一输出。不要使用 `-O`。

在 Windows 中设置 `DNF_PREMIUM903_V163` 为原始 EXE 路径、`DNF_PREMIUM903_CANDIDATE` 为候选路径，再运行构建保护与操作系统不执行映像检查：

```text
python -B -m unittest discover -s scripts/client/premium-service-903 -p "test_*.py" -v
```

临时目录由调用方的 `TEMP/TMP` 指定。没有真实输入时的跳过不算专项通过。Windows 检查使用 `SEC_IMAGE_NO_EXECUTE`，不执行游戏入口。接收验证执行真实许可门和分类分流，正文解码为显式跳过夹具；结果不能替代完整903回调、资源或游戏GUI验收。
