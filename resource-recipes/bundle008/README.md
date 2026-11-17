# 历史008资源基线

T17公开的是历史九文件、六索引、119→120频道的身份合同及只读核验。它使用原历史摘要，不把后续客户端或PVF当成008原件。实际成品发行范围与合同源码分别管理。

`deploy/remote/bundle008.py` 记录九文件的路径、长度、SHA256，六项GM索引摘要和频道身份。`channel.override.etc` 是精确120频道表，SHA256为 `5630b099ef98e322806c35cec25be7c7aae699ebfbc8f16e60f1a70d12bfe7d7`。原119表摘要为 `d701a4d9…`，准确移除10501–10507，加入10075及10191–10197。

```text
python deploy/remote/bundle008.py --root <原九文件目录> --channel resource-recipes/bundle008/channel.override.etc --indexes <原六索引目录>
```

工具要求九个唯一规范路径、精确原件和完整索引，逐文件读取摘要；只输出核验JSON，不上传、不修改运行配置或服务。历史资源和频道不能拆开认定为完整008组合。当前版本应使用当前批准的客户端/PVF配对，不能以历史核验通过覆盖后续修改。
