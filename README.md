# CServer - 基于 CHead 协议的可注入复用 TCP 服务器

CServer 是一个基于自定义协议头部 CHead 的高性能、可扩展的 TCP 服务器框架。它提供了中间件支持、请求路由、连接管理等功能，适用于构建各种 TCP 应用服务。

## 特性

- 🚀 **高性能**: 基于 Go 的并发模型，支持大量并发连接
- 🔌 **可注入**: 支持依赖注入，便于测试和模块化开发
- 🔄 **可复用**: 模块化设计，组件可在不同项目间复用
- 🛠️ **中间件支持**: 内置多种中间件，支持自定义中间件
- 📊 **协议标准**: 基于 CHead 协议，提供结构化的消息传输
- 🔒 **连接管理**: 支持连接数限制、超时控制
- 📝 **详细日志**: 完整的请求响应日志记录
- 🧪 **易测试**: 提供客户端工具，便于集成测试

## 快速开始

### 安装

```bash
go mod init your-project
go mod edit -replace github.com/qorm/cserver=./path/to/cserver
go mod edit -replace github.com/qorm/chead=./path/to/chead
```

### 创建服务器

```go
package main

import (
    "context"
    "log"
    "os"
    "strings"
    "time"
    
    "github.com/qorm/cserver"
)

func main() {
    // 创建服务器
    server := cserver.NewServer(":8080")
    
    // 设置配置
    server.SetTimeouts(30*time.Second, 30*time.Second)
    server.SetMaxConnections(100)
    
    // 添加中间件
    logger := log.New(os.Stdout, "[SERVER] ", log.LstdFlags)
    server.Use(cserver.LoggingMiddleware(logger))
    server.Use(cserver.RateLimitMiddleware(10)) // 每秒最多10个请求
    
    // 注册处理器
    server.RegisterHandlerFunc(1, func(ctx context.Context, command byte, data []byte) ([]byte, error) {
        return []byte(strings.ToUpper(string(data))), nil
    })
    
    // 启动服务器
    if err := server.Start(); err != nil {
        log.Fatal(err)
    }
    defer server.Stop()
    
    // 等待...
    select {}
}
```

### 创建客户端

```go
package main

import (
    "context"
    "fmt"
    "github.com/qorm/cserver"
)

func main() {
    client := cserver.NewClient()
    defer client.Close()
    
    if err := client.Connect("localhost:8080"); err != nil {
        panic(err)
    }
    
    ctx := context.Background()
    response, err := client.SendRequest(ctx, 1, 0, []byte("hello world"))
    if err != nil {
        panic(err)
    }
    
    fmt.Printf("Response: %s\n", response) // Output: HELLO WORLD
}
```

## 协议说明

CServer 使用 CHead 协议进行通信，协议格式如下：

```
| 1 byte | 1 byte | 4 bytes |  N bytes  |
| High   | Low    | Length  | Data      |
```

- **High**: 包含协议版本和命令信息
- **Low**: 包含请求方向、响应类型和命令类型
- **Length**: 数据长度（大端序）
- **Data**: 实际数据

## 中间件

### 内置中间件

1. **日志中间件** - 记录请求响应信息
```go
server.Use(cserver.LoggingMiddleware(logger))
```

2. **限流中间件** - 控制请求频率
```go
server.Use(cserver.RateLimitMiddleware(10)) // 每秒最多10个请求
```

3. **恢复中间件** - 捕获 panic，防止服务器崩溃
```go
server.Use(cserver.RecoveryMiddleware(logger))
```

4. **超时中间件** - 控制处理超时
```go
server.Use(cserver.TimeoutMiddleware(5 * time.Second))
```

5. **认证中间件** - 简单的token认证
```go
server.Use(cserver.AuthMiddleware(validateTokenFunc))
```

### 自定义中间件

```go
func CustomMiddleware() cserver.Middleware {
    return func(next cserver.Handler) cserver.Handler {
        return cserver.HandlerFunc(func(ctx context.Context, command byte, data []byte) ([]byte, error) {
            // 前置处理
            response, err := next.Handle(ctx, command, data)
            // 后置处理
            return response, err
        })
    }
}
```

## 处理器

### 注册处理器

```go
// 方式1：使用 HandlerFunc
server.RegisterHandlerFunc(1, func(ctx context.Context, command byte, data []byte) ([]byte, error) {
    return processCommand1(ctx, data)
})

// 方式2：实现 Handler 接口
type MyHandler struct{}

func (h *MyHandler) Handle(ctx context.Context, command byte, data []byte) ([]byte, error) {
    return processData(data), nil
}

server.RegisterHandler(2, &MyHandler{})
```

### 默认处理器

```go
server.SetDefaultHandler(cserver.HandlerFunc(func(ctx context.Context, command byte, data []byte) ([]byte, error) {
    return nil, fmt.Errorf("unknown command: %d", command)
}))
```

## 配置选项

### 超时设置
```go
server.SetTimeouts(
    30*time.Second, // 读超时
    30*time.Second, // 写超时
)
```

### 连接限制
```go
server.SetMaxConnections(1000) // 最大并发连接数
```

### 日志设置
```go
logger := log.New(os.Stdout, "[MYAPP] ", log.LstdFlags)
server.SetLogger(logger)
```

## 客户端使用

### 基本用法
```go
client := cserver.NewClient()
client.SetTimeouts(10*time.Second, 10*time.Second)

// 连接
err := client.Connect("localhost:8080")

// 发送需要响应的请求
response, err := client.SendRequest(ctx, command, commandType, data)

// 发送不需要响应的请求
err = client.SendRequestNoResponse(ctx, command, commandType, data)
```

## 错误处理

服务器会为以下情况发送错误响应：
- 无效的协议头部
- 未注册的命令（且无默认处理器）
- 处理器返回错误
- 请求超时

错误响应使用特殊的命令号 255 和命令类型 31。

## 性能优化

1. **连接池**: 客户端可以复用连接
2. **批量处理**: 在处理器中实现批量逻辑
3. **异步处理**: 对于不需要响应的请求，使用 `SendRequestNoResponse`
4. **中间件顺序**: 将高频使用的中间件放在前面

## 监控指标

```go
// 获取当前连接数
connCount := server.GetConnectionCount()
```

## 测试

运行测试：
```bash
go test -v ./...
```

测试覆盖以下场景：
- 基本请求响应
- 中间件链执行
- 错误处理
- 限流功能
- 超时处理

## 许可证

请参考项目根目录的 LICENSE 文件。

## 贡献

欢迎提交 Issue 和 Pull Request！