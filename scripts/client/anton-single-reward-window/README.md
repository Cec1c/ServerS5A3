# 单人安徒恩最终团本翻牌窗口

当前原生 `32B6530` 在 single=1 且 mode=0 时调用普通单人结算窗，团本奖励模型与该窗口不匹配。构建器将该分支转入同函数已有团本窗口检查，不改变收包、发奖或存档。

只接受 `build_client.py` 内明确列出的完整输入 SHA256；地址不能套用到其他客户端。输出必须是新的文件，不覆盖安装目录。当前累计输入长 112607232 字节，补丁只替换偏移 `0x2EB6551` 的 5 字节，不增节、不增长。

```text
python build_client.py <archived-input.exe> <new-candidate.exe>
python verify_client.py <archived-input.exe> <new-candidate.exe> <new-receipt.json>
```

依赖 Python、pefile 和 Unicorn。验证执行真实开窗路由、single/mode 判断、已开窗检查及团本开窗包装；频道访问器、缓存缺失查找、UI 叶函数明确使用夹具。300 组场景涵盖频道22至46、single开关、状态0/3/4、缓存命中/缺失及连续两次请求，检查栈平衡与ESI保持。未执行渲染器，不等于游戏实机验收。

实机需分别验证跳过动画与自然结束、奖励名称/数量、无重复普通空翻牌、回城与重登。发布必须基于所在服的最新累计源码和客户端重新构建、核验，源码或离线候选不代表上线。
