# V164 疲劳药次数接收兼容

服务端已有疲劳药每日次数持久化和 class0/541 投影；V164 的实际接收链仍拒绝该通知。旧 V153 补丁不能直接应用于 V164，后者已串联更多接收能力。

本补丁固定 V164 全摘要，在现有 `.r195rx` 段空闲范围增加33字节 helper，仅接受 class0/541，其余请求回到原接收链。保持所有段的原始地址、文件长度、PE 映像覆盖修复、自动资料刷新静默及其他既有补丁；不改原生次数 reader、PVF、dstr 或 NPK。

```text
python scripts/client/patch_fatigue_count_v164.py --input <V164原件> --output <独立输出DNF.exe>
```

测试变量 `DNF_FATIGUE_V164_CLIENT` 指向原件，安装 Unicorn 2.1.4 后在 `scripts/client/` 执行 `python -m unittest test_patch_fatigue_count_v164 -v`。验证四段精确改动、全部 class0/class1 操作号与 ABI、其他分类控制、原件/候选 Windows `SEC_IMAGE_NO_EXECUTE` 映射以及错误输入和覆盖拒绝。全哈匹配前不使用记录中的地址。

依次完成上游 MR、客户端双端 Release、玩家实际签名通道及完整下载读回，再上线疲劳次数服务端。实测物品416用后2/3、1/3、0/3，重登保持、06点重置，同时复测公会选角及主动/自动角色资料查看。
