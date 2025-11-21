# cserver - 高性能 TCP 服务器框架

## 概览

cserver 是一个基于 Go 的高性能 TCP 服务器框架，支持中间件、路由、认证等功能。

## 主要特性

- 🚀 **高性能**: 使用中间件预编译和原子操作，避免运行时开销
- 🔐 **内置认证**: 支持连接级认证和权限控制
- 🎯 **灵活路由**: 支持命令路由、路由组、链式调用
- 🔌 **中间件系统**: 全局/认证/路由级三级中间件支持
- 📊 **内置中间件**: 日志、恢复、超时、限流、指标统计
- ⚡ **标准化错误**: 完善的错误代码和错误处理机制
- 🔄 **智能客户端**: 带重试、超时控制、自动重连

## 快速开始

### 服务器端

```go
package main

import (
    "context"
    "log"
    "os"
    "time"
    
    "github.com/qorm/cserver"
)

func main() {
    // 创建服务器
    server := cserver.New(":8083")
    logger := log.New(os.Stdout, "[SERVER] ", log.LstdFlags)
    server.SetLogger(logger)

    // 添加全局中间件
    server.Use(
        cserver.LoggingMiddleware(logger),
        cserver.RecoveryMiddleware(logger),
    )

    // 添加认证中间件
    server.UseAuth(
        cserver.TimeoutMiddleware(10 * time.Second),
    )

    // 注册认证处理器（命令 0,0）
    server.Handle(0, 0, func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
        token := string(data)
        if token == "valid_token" {
            cserver.SetAuthInfo(ctx, map[string]string{"user": "admin"})
            return []byte("authenticated"), nil
        }
        return nil, cserver.ErrAuthFailed
    })

    // 注册业务处理器
    server.HandleAuth(1, 0, func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
        authInfo, _ := cserver.GetAuthInfo(ctx)
        return []byte("Hello, " + authInfo.(map[string]string)["user"]), nil
    })

    server.Start()
    defer server.Stop()
}
```

### 客户端

```go
package main

import (
    "context"
    "log"
    "time"
    
    "github.com/qorm/cserver"
)

func main() {
    client := cserver.NewClient(
        ":8083",
        cserver.WithReadTimeout(10*time.Second),
        cserver.WithMaxRetries(3),
    )

    ctx := context.Background()
    if err := client.Connect(ctx); err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    // 认证
    if err := client.Authenticate(ctx, []byte("valid_token")); err != nil {
        log.Fatal(err)
    }

    // 发送请求
    response, err := client.SendRequest(ctx, 1, 0, []byte("data"))
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("Response: %s", response)
}
```

## 架构设计

### 中间件预编译

路由注册时预编译中间件链，避免每次请求重新构建：

```
注册时:
  handler -> 应用路由中间件 -> 预编译全局链 -> 预编译认证链

运行时:
  请求到达 -> 根据认证状态选择链 -> 直接执行（无构建开销）
```

### 三级中间件系统

1. **全局中间件**: 应用到所有请求
2. **认证中间件**: 仅应用到已认证请求
3. **路由中间件**: 仅应用到特定路由

```go
// 全局 - 日志、恢复等
server.Use(LoggingMiddleware, RecoveryMiddleware)

// 认证后 - 限流、超时等
server.UseAuth(RateLimitMiddleware, TimeoutMiddleware)

// 路由级 - 特定逻辑
server.Handle(cmd, cmdType, handler, CustomMiddleware)
```

## 高级用法

### 路由组

```go
// 创建共享中间件的路由组
group := server.NewGroup()
group.Use(cserver.RateLimitMiddleware(100))

group.Handle(10, 0, handler1)
group.HandleAuth(11, 0, handler2)
```

### 路由构建器

```go
// 链式调用
server.NewRoute(20, 0).
    Use(middleware1).
    Use(middleware2).
    Handler(myHandler)
```

### 自定义中间件

```go
func MyMiddleware() cserver.Middleware {
    return func(next cserver.HandlerFunc) cserver.HandlerFunc {
        return func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
            // 前置处理
            start := time.Now()
            
            // 调用下一个处理器
            response, err := next(ctx, command, commandType, data)
            
            // 后置处理
            log.Printf("Duration: %v", time.Since(start))
            
            return response, err
        }
    }
}
```

### 认证流程

```go
// 1. 启用认证
server.EnableAuth()

// 2. 注册认证处理器（命令 0,0）
server.Handle(0, 0, func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
    token := string(data)
    user := validateToken(token)
    if user != nil {
        // 设置认证信息
        cserver.SetAuthInfo(ctx, user)
        return []byte("ok"), nil
    }
    return nil, cserver.ErrAuthFailed
})

// 3. 在处理器中获取认证信息
server.HandleAuth(1, 0, func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
    user, _ := cserver.GetAuthInfo(ctx)
    // 使用 user 信息...
})
```

## 内置中间件

### LoggingMiddleware
记录请求和响应，包括命令、耗时、错误等。

### RecoveryMiddleware
捕获 panic 并转换为错误响应，防止服务崩溃。

### TimeoutMiddleware
为请求设置超时时间，超时自动返回错误。

### RateLimitMiddleware
基于令牌桶算法的限流中间件。

### MetricsMiddleware
收集请求统计信息（次数、错误、平均耗时等）。

## 错误处理

### 标准错误

```go
// 使用预定义错误
return nil, cserver.ErrNotAuthenticated
return nil, cserver.ErrTimeout
return nil, cserver.ErrRateLimit

// 创建自定义错误
return nil, cserver.NewError(
    cserver.ErrCodeInvalidRequest,
    "invalid parameters",
    fmt.Errorf("missing field: name"),
)
```

### 错误代码

| 代码 | 常量 | 说明 |
|-----|------|------|
| 0 | ErrCodeUnknown | 未知错误 |
| 1 | ErrCodeNotAuthenticated | 未认证 |
| 2 | ErrCodeAuthFailed | 认证失败 |
| 3 | ErrCodeNoHandler | 无处理器 |
| 4 | ErrCodeTimeout | 超时 |
| 5 | ErrCodeRateLimit | 限流 |
| 6 | ErrCodeInvalidRequest | 无效请求 |
| 7 | ErrCodeInternalError | 内部错误 |
| 8 | ErrCodeConnectionClosed | 连接关闭 |
| 9 | ErrCodeMaxConnections | 达到最大连接数 |
| 10 | ErrCodeBadProtocol | 协议错误 |

### 错误检查

```go
if cserver.IsServerError(err) {
    code := cserver.GetErrorCode(err)
    log.Printf("Error code: %d", code)
}
```

## 配置选项

### 服务器配置

```go
server.SetTimeouts(30*time.Second, 30*time.Second)  // 读写超时
server.SetMaxConnections(1000)                      // 最大连接数
server.EnableAuth()                                  // 启用认证
server.SetDefaultHandler(handler)                    // 默认处理器
```

### 客户端配置

```go
client := cserver.NewClient(
    addr,
    cserver.WithReadTimeout(30*time.Second),
    cserver.WithWriteTimeout(30*time.Second),
    cserver.WithConnectTimeout(10*time.Second),
    cserver.WithMaxRetries(3),
    cserver.WithRetryInterval(time.Second),
)
```

## 性能优化

### 1. 中间件预编译
路由注册时预编译中间件链，运行时直接执行，避免每次请求构建。

### 2. 原子操作
使用 `atomic.Int32` 进行连接计数，避免锁竞争。

### 3. sync.Map
路由表使用 `sync.Map`，支持高并发读取。

### 4. 连接复用
客户端支持连接复用和自动重连，减少连接建立开销。

### 5. 零拷贝
尽可能减少数据拷贝，直接传递切片引用。

## 兼容性说明

**本版本不考虑向后兼容**，进行了以下重大改进：

1. 移除 `Handler` 接口，统一使用 `HandlerFunc`
2. 中间件系统重构，支持预编译
3. 错误类型标准化，使用 `ServerError`
4. 客户端 API 简化，移除旧方法
5. 路由 API 现代化，支持链式调用

## 示例

查看 `example/` 目录：
- `example/auth_test/` - 认证示例
- `example/middleware_usage/` - 中间件示例

## License

MIT License
