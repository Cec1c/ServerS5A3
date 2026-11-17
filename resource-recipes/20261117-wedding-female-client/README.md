# 正式客户端婚姻请求衍生配方

推荐从已公开的V215 ZIP与66482976 PVF执行 `scripts/client/build_selected_feedback.py`，该入口自动生成中间元数据。底层输入必须为下表中一份精确 DNF.exe 与同代 source.json、三件正式 DLL/NPK 依赖。累计版本保留帝国双角色补丁；不能用早期衍生包覆盖累计版本。

| 基底 SHA256 | 衍生 SHA256 | 匹配 PVF SHA256 |
| --- | --- | --- |
| `28e04d1d1c148c841318d533ed8ccb3e426ba5a5f24328b3c913b81303b12a84` | `f16d3bf48b6b802378fb066eaab96add8132c6406d7941a6d8d60328e47f6d8b` | `664829766ab016ad6935370281bb24820e40990a6db4d607607aa67b49d8da4b` |
| `40657c19955dc2f594fc2221ef91044170d86406ba6fd193cdb1839877f28cb0` | `58e51fba3258a9499e77caedaea16a6d9f38904054e10edd91564e24894547c4` | `664829766ab016ad6935370281bb24820e40990a6db4d607607aa67b49d8da4b` |

```text
python build.py --base <解压目录> --output <不存在的输出目录>
```

仅将文件偏移 `0x016cf5a9` 的 `75` 改成 `eb` 并重算 PE CheckSum。跳过客户端同性弹窗，由服务端 `DNFBRIDGE_WEDDING_GENDER_POLICY` 裁决；女女策略为 `deny_male_male`。默认服务端策略仍为 `opposite_only`。

输出为独立 EXE、固定时间戳 gzip、完整正式 zip 和精确回执。为复现已冻结的压缩摘要，28e 路线使用 gzip level 1，406 路线使用 level 9；二者均校验精确输出摘要、长度和 PE CheckSum。归档中的 source.json 另记录父输入与待验收状态。

测试同时设置 `WEDDING_CLIENT_BASE=<28e DNF.exe路径>` 与 `WEDDING_TAG_BUFF_CLIENT_BASE=<406 DNF.exe路径>`，执行 `python -m unittest discover -s . -p "test_*.py" -v`；需要 capstone 与 pefile，已安装 Unicorn 时同时执行指令仿真，否则验证精确无条件分支语义。工具不执行 EXE，游戏内双新娘显示仍需上线后验收。
