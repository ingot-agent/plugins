# tool.image

`tool.image` 提供 `view_image`，让模型读取本地图片。模块路径为
`github.com/ingot-agent/plugins/tool-image`，组件为 `default`（包 `.`），
兼容 Ingot `>=0.3.0 <0.4.0`，具体以 [manifest](ingot.plugin.toml) 为准。
本插件尚未发布，以下说明对应当前分支源码。

依赖 `workspace.Resolver` 和 `asset.Store`，导出 `[]tool.Tool`。
通常由 `session.sqlite` 和 `asset.local` 提供这两项能力，通过 `tool.runtime`
将工具提供给模型。插件没有配置文件、配置 Operation 或后台任务；资源限制为固定值。

## 使用

```json
{"path":"screenshots/page.png"}
```

`path` 是唯一参数，必须是非空字符串。接受工作区相对路径和宿主操作系统的
绝对路径，包括工作区外的图片。Windows 绝对路径示例：

```json
{"path":"D:\\pictures\\diagram.png"}
```

相对路径通过本次 `Invocation.Scope` 解析 Session 的工作区，不回退到进程当前目录。
相对路径中的 `..` 按普通文件路径规则处理，不做工作区隔离。绝对路径不需要
Session 已绑定工作区。文件系统符号链接按操作系统规则跟随，访问权限由宿主进程
和操作系统决定。Windows 的 `C:image.png`、`\image.png` 等不完整绝对路径被拒绝。
不下载 URL，也不展开 `~`、环境变量或通配符。

## 图片内容

支持 PNG、JPEG、WebP 和 GIF，按实际内容识别格式，不依赖文件扩展名。
成功结果包含尺寸说明和一个 `content.KindImage` 图片部分：

- PNG、JPEG 和静态 WebP 保留原始字节与文件名。
- GIF 提取第一帧并转为 PNG，标明首帧及其实际尺寸，文件名后缀改为 `.png`。
  对于帧区域小于 GIF 画布的图片，返回该帧区域；不播放或合成动画。
- 图片导入 `asset.Store` 后返回 `SourceAsset` 引用。源文件之后被修改或删除，
  不会改变本次结果中的图片。资产的持久化和生命周期由所选 Store 提供。

工具返回真实图片内容，不把 base64 放进文本，也不把 `file://` 路径交给模型服务。
需使用支持图片工具结果的模型适配器和具有视觉能力的上游模型；当前分支的
[Responses](../model-openai-responses/README.md) 和
[Chat Completions](../model-openai-compatible/README.md) 适配器已支持此映射。
本地图片由适配器读取资产字节，编码为 Base64 data URL，放入模型请求的图片字段。
Chat Completions 使用 `image_url.url`，Responses 使用 `input_image.image_url`；
不调用 Files API，不使用 `file_id`，无需额外的图片上传配置。
Base64 编码后的数据约为原始字节的 4/3，服务商或中转站的总请求体大小限制仍然适用。

## 限制与错误

每个输入文件和 GIF 转换结果最多 **20 MiB**，图片最多 **4000 万像素**。
读取前检查文件类型和大小，实际读取也有字节上限；先检查尺寸，再解码校验内容。
同一插件实例最多同时处理两张图片，其余调用可取消地等待。
GIF 只解码首帧；动画 WebP、SVG、PDF 等不受支持，也不执行自动缩放或转码。
资产存储和模型适配器设置的更小限制仍然生效。

缺失文件、无权限、目录/特殊文件、不支持或损坏的图片、超过限制等已知情况，
返回 `view_image error: ...` 文本结果，让模型可以调整路径或准备其他图片。
参数格式错误、工作区解析失败、其他 I/O 错误、资产保存失败及 context 取消
作为 Go 错误传播。未知参数和尾随 JSON 被拒绝。工具不会改写源文件。

## 加入项目和验证

在项目目录加入本地插件，再重建运行时：

```sh
ingot plugin add ./plugins/tool-image
ingot up -d -- web
```

路径应按实际 checkout 位置调整。组件图中需要提供 `asset.Store` 和
`workspace.Resolver`。加入插件需要重建；运行中的旧 Image 不会自动增加工具。

在本模块目录执行（PowerShell 先设置 `$env:GOWORK = 'off'`）：

```sh
GOWORK=off go mod tidy -diff
GOWORK=off go vet ./...
GOWORK=off go test -race ./...
```

测试覆盖四种图片格式、真实 MIME、不可变资产、工作区与绝对路径、字节/像素限制、
损坏内容、参数与存储错误，以及并发处理和取消。

返回 [插件文档索引](../docs/README.md)。
