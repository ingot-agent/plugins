# 第一个可运行的工具插件

本教程创建一个真正的 `greet` 工具，独立测试它，再由 Core Builder 把它、
`tool.runtime` 和一个只执行一次的演示宿主组合成 Image。宿主通过公开
`tool.Runtime` 调用工具，输出 `Hello, Ingot!` 后正常退出；不需要模型服务或 API Key。
最后说明如何把同一个工具加入浏览器 Agent。

教程面向当前 `>=0.3.0 <0.4.0` Builder 构造协议。工具和宿主的 Go 模块固定
SDK `v0.2.10`、ABI `v0.1.0`；它们的独立编译与完整组合验证是两项检查。
完整组合使用当前本地 `tool-runtime`、SDK 和 ABI checkout，不能据此宣布旧
`tool-runtime/v0.1.0` 具有当前分支的接口。版本选择见
[SDK 迁移说明](https://github.com/ingot-agent/sdk/blob/main/docs/MIGRATIONS.md)。

## 1. 准备目录和工具

需要 Go 1.24.2 或更新版本、当前 Core CLI，以及并排的 `plugins`、`sdk`、
`ingot-abi` 源码。Core 安装/编译见
[使用指南](https://github.com/ingot-agent/ingot/blob/main/docs/USAGE.md)。
不要把教程模块放进官方 plugins 仓库顶层：它们是独立第三方模块，目录安排如下。

```text
work/
  plugins/                 # 本仓库
  sdk/
  ingot-abi/
  first-plugin/
    go.work                # 第 5 步创建，仅供本地组合
    tool-greet/
      go.mod
      ingot.plugin.toml
      greet.go
      greet_test.go
    demo-host/
      go.mod
      ingot.plugin.toml
      host.go
    project/
      plugins.toml
    .ingot-demo/            # 独立测试 Home，由 CLI 创建，不提交
```

本教程的 shell 命令从 `first-plugin/` 执行，另有说明时除外。
POSIX shell 中使用 `GOWORK=off go ...`；PowerShell 则先设置
`$env:GOWORK = 'off'`，再执行 `go ...`。这用于独立 Go 检查；第 5 步中的
Builder 会另外从工作目录向上查找 `go.work` 中的本地 replacement。

## 2. 建立工具模块

保存 `tool-greet/go.mod`：

<!-- tutorial-file: tool-greet/go.mod -->

```go
module example.com/ingot/tool-greet

go 1.24.2

require (
    github.com/ingot-agent/ingot-abi v0.1.0
    github.com/ingot-agent/sdk v0.2.10
)
```

`example.com` 是本地练习身份；发布前改成你控制的真实 module path。
保存 `tool-greet/ingot.plugin.toml`：

<!-- tutorial-file: tool-greet/ingot.plugin.toml -->

```toml
manifest_version = 1
name = "tutorial.greet"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

这里的 `tutorial.greet` 是 Plugin 名，`default` 是局部 Component 名，
下面的 `greet` 是模型可见工具名。`config_package` 是 manifest v1 要求的
包位置，不会让 Builder 注入 `Config` 参数。

保存 `tool-greet/greet.go`：

<!-- tutorial-file: tool-greet/greet.go -->

```go
package greet

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "strings"

    ingotabi "github.com/ingot-agent/ingot-abi"
    "github.com/ingot-agent/sdk/content"
    "github.com/ingot-agent/sdk/tool"
)

type Dependencies struct{}
type Exports struct { Tools []tool.Tool }

func New(ctx context.Context, _ Dependencies) (Exports, ingotabi.Cleanup, error) {
    if err := ctx.Err(); err != nil {
        return Exports{}, nil, err
    }
    return Exports{Tools: []tool.Tool{greeter{}}}, nil, nil
}

type greeter struct{}
var _ tool.Tool = greeter{}

func (greeter) Definition() tool.Definition {
    return tool.Definition{
        Name: "greet",
        Description: "Return a greeting for a nonempty name.",
        InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["name"],"properties":{"name":{"type":"string","minLength":1}}}`),
    }
}

func (greeter) Invoke(ctx context.Context, call tool.Invocation) (tool.Result, error) {
    if err := ctx.Err(); err != nil {
        return tool.Result{}, err
    }
    var args struct { Name string `json:"name"` }
    decoder := json.NewDecoder(bytes.NewReader(call.Call.Arguments))
    decoder.DisallowUnknownFields()
    if err := decoder.Decode(&args); err != nil {
        return tool.Result{}, fmt.Errorf("decode greet arguments: %w", err)
    }
    if err := decoder.Decode(new(any)); err != io.EOF {
        return tool.Result{}, fmt.Errorf("greet arguments must contain one JSON object")
    }
    name := strings.TrimSpace(args.Name)
    if name == "" {
        return tool.Result{}, fmt.Errorf("greet name must not be blank")
    }
    return tool.Result{Content: content.FromText("Hello, " + name + "!")}, nil
}
```

`[]tool.Tool` 被收集进 `tool.runtime.Dependencies.Tools`。工具没有共享可变
状态、后台任务或外部资源，所以可并发调用，Cleanup 为 nil。
`Definition` 每次返回新的 Schema 字节；结果内容也归调用者所有。
它不需要 Session 的文件访问或 Interaction，因此无需注入这些能力。
如果后来增加工作区副作用，应从 `call.Scope` 解析 Workspace，不能用进程当前目录代替。

Runtime 在分派前做 JSON Schema 校验；工具自身继续验证参数，保证直接 Go
调用也有明确行为。工具内部错误使用自己的错误，不在已分派之后返回仅供
Runtime 分派前使用的 `tool.ErrInvalidArguments`。

## 3. 独立编译并直接调用

保存 `tool-greet/greet_test.go`：

<!-- tutorial-file: tool-greet/greet_test.go -->

```go
package greet_test

import (
    "context"
    "encoding/json"
    "errors"
    "testing"

    greet "example.com/ingot/tool-greet"
    "github.com/ingot-agent/sdk/tool"
)

func TestGreet(t *testing.T) {
    exports, cleanup, err := greet.New(context.Background(), greet.Dependencies{})
    if err != nil { t.Fatal(err) }
    if cleanup != nil { t.Cleanup(func() { _ = cleanup(context.Background()) }) }
    if len(exports.Tools) != 1 { t.Fatalf("tools = %d", len(exports.Tools)) }
    invoke := func(ctx context.Context, args string) (tool.Result, error) {
        return exports.Tools[0].Invoke(ctx, tool.Invocation{Call: tool.Call{
            ID: "test-1", Name: "greet", Arguments: json.RawMessage(args),
        }})
    }
    result, err := invoke(context.Background(), `{"name":"Ingot"}`)
    if err != nil { t.Fatal(err) }
    if len(result.Content) != 1 || result.Content[0].Text != "Hello, Ingot!" {
        t.Fatalf("unexpected result: %#v", result)
    }
    for _, input := range []string{`{}`, `null`, `{"name":" "}`, `{"name":1}`, `{"name":"a","extra":true}`, `{"name":"a"}{}`} {
        if _, err := invoke(context.Background(), input); err == nil {
            t.Errorf("accepted invalid input %s", input)
        }
    }
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    if _, err := invoke(ctx, `{"name":"Ingot"}`); !errors.Is(err, context.Canceled) {
        t.Fatalf("cancellation = %v", err)
    }
}
```

在 `tool-greet/` 执行：

```sh
GOWORK=off go mod tidy
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test -v ./...
```

成功应包含 `--- PASS: TestGreet`。提交生成的 `go.sum`，不要提交本地路径
replacement。具备对应平台 race/C 工具链时再运行 `GOWORK=off go test -race ./...`。
这里验证工具公开行为；下面验证 manifest、类型连线、Runtime 校验和生命周期。

## 4. 创建一次性演示宿主

宿主是另一个普通插件，没有导出能力，只消费 `tool.Runtime`。它不导入
工具或 `tool-runtime` 的实现包。保存 `demo-host/go.mod`：

<!-- tutorial-file: demo-host/go.mod -->

```go
module example.com/ingot/demo-host

go 1.24.2

require (
    github.com/ingot-agent/ingot-abi v0.1.0
    github.com/ingot-agent/sdk v0.2.10
)
```

保存 `demo-host/ingot.plugin.toml`：

<!-- tutorial-file: demo-host/ingot.plugin.toml -->

```toml
manifest_version = 1
name = "tutorial.host"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

保存 `demo-host/host.go`：

<!-- tutorial-file: demo-host/host.go -->

```go
package demohost

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"

    ingotabi "github.com/ingot-agent/ingot-abi"
    "github.com/ingot-agent/ingot-abi/invocation"
    "github.com/ingot-agent/ingot-abi/lifecycle"
    "github.com/ingot-agent/sdk/tool"
)

type Dependencies struct {
    Tools tool.Runtime
    Invocation invocation.Invocation
    Lifecycle lifecycle.Controller
}
type Exports struct{}

func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
    if deps.Tools == nil || deps.Invocation == nil || deps.Lifecycle == nil {
        return Exports{}, nil, fmt.Errorf("demo host dependencies are required")
    }
    if err := ctx.Err(); err != nil { return Exports{}, nil, err }
    if deps.Invocation.Mode() == invocation.ModeCheck {
        return Exports{}, nil, nil
    }
    runCtx, cancel := context.WithCancel(ctx)
    done := make(chan struct{})
    go func() {
        defer close(done)
        deps.Lifecycle.RequestShutdown(run(runCtx, deps.Tools))
    }()
    cleanup := func(ctx context.Context) error {
        cancel()
        select {
        case <-done: return nil
        case <-ctx.Done(): return ctx.Err()
        }
    }
    return Exports{}, cleanup, nil
}

func run(ctx context.Context, runtime tool.Runtime) error {
    call := tool.Invocation{Call: tool.Call{
        ID: "demo-1", Name: "greet", Arguments: json.RawMessage(`{"name":"Ingot"}`),
    }}
    result, err := runtime.Call(ctx, call)
    if err != nil { return err }
    if len(result.Content) != 1 || result.Content[0].Text != "Hello, Ingot!" {
        return fmt.Errorf("unexpected greeting: %#v", result.Content)
    }
    fmt.Println(result.Content[0].Text)
    call.Call.Arguments = json.RawMessage(`{"name":42}`)
    if _, err := runtime.Call(ctx, call); !errors.Is(err, tool.ErrInvalidArguments) {
        return fmt.Errorf("expected schema rejection, got %v", err)
    }
    fmt.Println("invalid input rejected before dispatch")
    return nil
}
```

这个宿主遵守 `ModeCheck`，只在正常运行时启动任务。它通过
`lifecycle.Controller` 请求退出，Cleanup 取消并等待任务，不调用 `os.Exit`。
空 `Scope` 仅适用于本例不依赖 Session 的工具，不应照搬到读写文件或审批调用。
在 `demo-host/` 同样运行 `go mod tidy`、`go build ./...`、`go vet ./...`，使用
`GOWORK=off`。宿主的实际行为将在生成的 Image 中验证。

## 5. 组合与实际执行

回到 `first-plugin/`，保存专用 `go.work`。这是本地开发输入，不加入任何
插件的 `go.mod`：

<!-- tutorial-file: go.work -->

```go
go 1.24.2

use (
    ./tool-greet
    ./demo-host
    ../plugins/tool-runtime
)

replace github.com/ingot-agent/sdk => ../sdk
replace github.com/ingot-agent/ingot-abi => ../ingot-abi
```

Builder 的 Go 子进程使用 `GOWORK=off`；Builder 自己从**当前目录向上找到的
第一个 `go.work`**提取这些 replacement，记录本地内容摘要并复制到构建目录。
仅设置环境变量 `GOWORK` 指向别处，不会改变这个查找过程。不要从具有不同
`go.work` 的目录执行以下命令。

保存 `project/plugins.toml`，三个路径相对于此文件：

<!-- tutorial-file: project/plugins.toml -->

```toml
plugins_version = 1

[[plugins]]
module = "example.com/ingot/tool-greet"
path = "../tool-greet"

[[plugins]]
module = "github.com/ingot-agent/plugins/tool-runtime"
path = "../../plugins/tool-runtime"

[[plugins]]
module = "example.com/ingot/demo-host"
path = "../demo-host"
```

执行：

```sh
ingot --home ./.ingot-demo setup
ingot --home ./.ingot-demo project resolve -f ./project/plugins.toml
ingot --home ./.ingot-demo project show -f ./project/plugins.toml
ingot --home ./.ingot-demo build greeting -f ./project/plugins.toml --locked
ingot --home ./.ingot-demo start greeting --foreground
```

首次 resolve 会下载依赖；build 使用 lock 和已解析的模块缓存，生成代码、
编译并运行 `--ingot-check`，成功后绑定名为 `greeting` 的 Runtime。
最后一条命令的工具输出应为：

```text
Hello, Ingot!
invalid input rejected before dispatch
```

进程应以 0 退出。这同时证明公开 Tool 被收集、`tool.Runtime` 唯一解析、
Schema 拒绝无效输入、ABI 宿主能力注入和正常结束流程。CLI 可另有 Runtime
初始化/构建信息，不要求完整 stdout 与这两行逐字相等。

修改源码后，重新 resolve，再 build/start；`--locked` 会拒绝已变化的本地
内容。锁记录 checkout 内容身份，不是可用于第三方下载的公开 module 版本。

## 6. 放入浏览器 Agent

使用[基础聊天 recipe](../recipes.md)创建完整浏览器项目，并在其 recipe 中
加入以下条目，路径按项目位置调整：

```toml
[[plugins]]
module = "example.com/ingot/tool-greet"
path = "../tool-greet"
```

这个组合不需要演示宿主 `tutorial.host`：它在一次调用后主动结束进程，
因此只用于上面的独立验证。浏览器项目已经有 `app.backend` 负责运行生命周期。
从正确的开发 workspace resolve、build/up，配置模型后要求模型调用 `greet`
并给出 `{"name":"Ingot"}`。工具卡片应显示问候语；模型是否遵循请求取决于
所选模型，确定性验证仍以上面的宿主为准。浏览器没有公开的任意工具调用 API。

## 7. 常见失败与发布前检查

| 现象 | 检查 |
|---|---|
| constructor contract 报错 | 必须是命名 `Dependencies`/`Exports` 和 `New(context.Context, Dependencies) (Exports, ingotabi.Cleanup, error)` |
| 缺少 `tool.Runtime` | recipe 是否包含 `tool-runtime`，其导出接口是否来自同一 SDK module |
| duplicate tool / ambiguous provider | 工具名必须唯一；不要重复加入另一 `tool.Runtime` 实现 |
| 运行时没有输出且一直运行 | recipe 是否包含 `demo-host`；仅工具和工具 Runtime 不会主动发起调用 |
| package/type 不存在 | checkout 与 SDK 版本是否匹配；检查本地 replacement 是否实际进入 lock |
| local source digest/stale lock | 修改之后重新 resolve，审查新 lock，再 build |
| SDK 本地测试通过，独立模块失败 | 去掉 workspace 后检查真实 `go.mod` 依赖；不能把本地结果冒充发布版本验证 |

发布自己的模块前补齐 README、许可证和变更记录，用你控制的 module path
重新 tidy/test，并在不受祖先 `go.work` 影响的目录用真实发布 tag 重新组合。
第三方插件不需要加入本仓库；如果贡献官方模块，还必须遵守
[仓库规则](../../CONTRIBUTING.md)及[发布流程](../../RELEASE.md)。

本教程在 2026-09-22 Windows/amd64、Go 1.26.3 上提取所有 Go 文件，独立
编译/测试后完成本地组合和实际执行。具体检查范围随
[recipes 核验记录](../recipes.md#本次核验范围)一同维护；不把本地运行等同于
所有平台、race 或远端发布版本的通过。

维护者可使用 [verify_first_plugin.py](verify_first_plugin.py) 自动提取页面中
标记的九个文件，避免另存一套会与文档分叉的示例源代码。Python 3.11+，从
本仓库根目录执行（Windows 可将 `python` 替换成 `py`）：

```sh
python docs/tutorials/verify_first_plugin.py --output /absolute/path/to/empty-scratch --ingot /absolute/path/to/ingot
```

`--output` 必须不存在或为空；省略时创建临时目录，脚本保留文件供检查。
默认 SDK/ABI 在本仓库旁，可用 `--sdk`/`--abi` 改为明确路径。不传 `--ingot`
时只执行独立 Go 检查；传入后还会创建专用 Home、构建并运行示例 Image。
工具自身测试和编译使用发布依赖，完整组合的 SDK/ABI 使用明确的本地来源。
