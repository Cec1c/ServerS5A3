# 队伍房间登记后的累计空来源保护

先用`../party-initial-room-registration/build_candidate.py`从固定244b5c1e输入生成213d3e29候选。该目录再保留模型登记辅助代码，将空来源保护放到另一段已验证RX空白区，最终生成SHA256 `9276348410bbf955f41a4d9e407f5288db883ff6edd68e66f30c83f56dfe3250`、112607232字节的累计客户端。旧独立空来源配方与模型配方占用同一旧辅助区，不能直接互相覆盖。

在本目录中运行，输出均需为不存在的新文件：

```text
python -B build_candidate.py --input <213d3e29客户端> --output <新的累计EXE> --manifest <新的manifest.json>
python -B verify_candidate.py --input <213d3e29客户端> --candidate <新的累计EXE> --manifest <manifest.json> --report <新的verification.json> --unicorn-path <安装Unicorn的目录>
```

验证读取磁盘候选并独立解码跳转、校验和和四个修改范围，模型补丁三段字节须保持。12组配对用例、24次Unicorn运行覆盖三组标志、空/有效来源、空效果返回及原生缺失owner分支；检查thiscall参数、寄存器、栈、返回及原公共收尾。拒绝错误输入、重复补丁、范围外改动和损坏跳转/校验和。

`native_window.py`复用独立空来源的Native夹具并改用累计构建器。对象、owner查询、正常效果创建、行访问器、停止地址等均为显式适配，详见验证器的`STUBS`。不执行GPU渲染或完整对象回调，也不据此登记游戏验收。工具不访问网络、不安装、不发布、不启动游戏；输入EXE及依赖另行提供。
