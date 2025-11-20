package cserver

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// LoggingMiddleware 记录请求日志的中间件
func LoggingMiddleware(logger *log.Logger) Middleware {
	return func(next Handler) Handler {
		return HandlerFunc(func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
			start := time.Now()
			logger.Printf("[REQUEST] Command: %d, CommandType: %d, DataSize: %d", command, commandType, len(data))

			response, err := next.Handle(ctx, command, commandType, data)

			duration := time.Since(start)
			if err != nil {
				logger.Printf("[ERROR] Command: %d, CommandType: %d, Duration: %v, Error: %v",
					command, commandType, duration, err)
			} else {
				logger.Printf("[SUCCESS] Command: %d, CommandType: %d, Duration: %v, ResponseSize: %d",
					command, commandType, duration, len(response))
			}

			return response, err
		})
	}
}

// RecoveryMiddleware 恢复panic的中间件
func RecoveryMiddleware(logger *log.Logger) Middleware {
	return func(next Handler) Handler {
		return HandlerFunc(func(ctx context.Context, command byte, commandType uint8, data []byte) (response []byte, err error) {
			defer func() {
				if r := recover(); r != nil {
					logger.Printf("[PANIC RECOVERED] Command: %d, CommandType: %d, Panic: %v", command, commandType, r)
					response = nil
					err = fmt.Errorf("internal server error: %v", r)
				}
			}()

			return next.Handle(ctx, command, commandType, data)
		})
	}
}

// TimeoutMiddleware 超时控制中间件
func TimeoutMiddleware(timeout time.Duration) Middleware {
	return func(next Handler) Handler {
		return HandlerFunc(func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
			// 创建带超时的上下文
			timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			// 使用channel来处理超时
			type result struct {
				response []byte
				err      error
			}

			resultChan := make(chan result, 1)

			go func() {
				// 在goroutine中添加panic恢复
				defer func() {
					if r := recover(); r != nil {
						resultChan <- result{
							response: nil,
							err:      fmt.Errorf("panic recovered in timeout middleware: %v", r),
						}
					}
				}()

				response, err := next.Handle(timeoutCtx, command, commandType, data)
				resultChan <- result{response: response, err: err}
			}()

			select {
			case res := <-resultChan:
				return res.response, res.err
			case <-timeoutCtx.Done():
				return nil, fmt.Errorf("request timeout after %v", timeout)
			}
		})
	}
}

// RateLimitMiddleware 限流中间件
func RateLimitMiddleware(requestsPerSecond int) Middleware {
	// 使用令牌桶算法实现限流
	tokens := make(chan struct{}, requestsPerSecond)

	// 初始化令牌桶
	for i := 0; i < requestsPerSecond; i++ {
		tokens <- struct{}{}
	}

	// 定期补充令牌
	go func() {
		ticker := time.NewTicker(time.Second / time.Duration(requestsPerSecond))
		defer ticker.Stop()

		for range ticker.C {
			select {
			case tokens <- struct{}{}:
			default:
				// 令牌桶已满，跳过
			}
		}
	}()

	return func(next Handler) Handler {
		return HandlerFunc(func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
			// 尝试获取令牌
			select {
			case <-tokens:
				// 获得令牌，继续处理
				return next.Handle(ctx, command, commandType, data)
			case <-time.After(100 * time.Millisecond):
				// 等待超时，拒绝请求
				return nil, fmt.Errorf("rate limit exceeded")
			}
		})
	}
}

// MetricsMiddleware 统计中间件
type MetricsCollector struct {
	mu            sync.RWMutex
	RequestCount  map[byte]int64
	ErrorCount    map[byte]int64
	TotalDuration map[byte]time.Duration
	AvgDuration   map[byte]time.Duration
}

func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		RequestCount:  make(map[byte]int64),
		ErrorCount:    make(map[byte]int64),
		TotalDuration: make(map[byte]time.Duration),
		AvgDuration:   make(map[byte]time.Duration),
	}
}

func (m *MetricsCollector) GetStats(command byte) (requests, errors int64, avgDuration time.Duration) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	requests = m.RequestCount[command]
	errors = m.ErrorCount[command]
	avgDuration = m.AvgDuration[command]

	return
}

func MetricsMiddleware(collector *MetricsCollector) Middleware {
	return func(next Handler) Handler {
		return HandlerFunc(func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
			start := time.Now()

			collector.mu.Lock()
			collector.RequestCount[command]++
			collector.mu.Unlock()

			response, err := next.Handle(ctx, command, commandType, data)

			duration := time.Since(start)

			collector.mu.Lock()
			if err != nil {
				collector.ErrorCount[command]++
			}

			collector.TotalDuration[command] += duration
			collector.AvgDuration[command] = collector.TotalDuration[command] / time.Duration(collector.RequestCount[command])
			collector.mu.Unlock()

			return response, err
		})
	}
}

// ChainMiddleware 链式组合多个中间件
func ChainMiddleware(middlewares ...Middleware) Middleware {
	return func(next Handler) Handler {
		// 从右到左应用中间件
		for i := len(middlewares) - 1; i >= 0; i-- {
			next = middlewares[i](next)
		}
		return next
	}
}

// GetAuthInfo 从上下文中获取认证信息
func GetAuthInfo(ctx context.Context) (interface{}, bool) {
	authInfo := ctx.Value(AuthInfoKey)
	if authInfo == nil {
		return nil, false
	}
	return authInfo, true
}

// GetRemoteAddr 从上下文中获取远程地址
func GetRemoteAddr(ctx context.Context) (string, bool) {
	addr := ctx.Value(RemoteAddrKey)
	if addr == nil {
		return "", false
	}
	if strAddr, ok := addr.(string); ok {
		return strAddr, true
	}
	return "", false
}

// RequireAuthMiddleware 要求认证的中间件
// 如果请求上下文中没有认证信息，则拒绝请求
func RequireAuthMiddleware() Middleware {
	return func(next Handler) Handler {
		return HandlerFunc(func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
			_, authenticated := GetAuthInfo(ctx)
			if !authenticated {
				return nil, fmt.Errorf("authentication required")
			}
			return next.Handle(ctx, command, commandType, data)
		})
	}
}

// RequireRoleMiddleware 要求特定角色的中间件
// authInfo 应该实现 HasRole(role string) bool 方法，或者可以自定义检查逻辑
func RequireRoleMiddleware(roleCheck func(authInfo interface{}) bool) Middleware {
	return func(next Handler) Handler {
		return HandlerFunc(func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
			authInfo, authenticated := GetAuthInfo(ctx)
			if !authenticated {
				return nil, fmt.Errorf("authentication required")
			}

			if !roleCheck(authInfo) {
				return nil, fmt.Errorf("insufficient permissions")
			}

			return next.Handle(ctx, command, commandType, data)
		})
	}
}

// ConnectionLoggingMiddleware 记录连接信息的中间件
func ConnectionLoggingMiddleware(logger *log.Logger) Middleware {
	return func(next Handler) Handler {
		return HandlerFunc(func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
			remoteAddr, _ := GetRemoteAddr(ctx)
			authInfo, authenticated := GetAuthInfo(ctx)

			if authenticated {
				logger.Printf("[CONN] RemoteAddr: %s, Authenticated: true, AuthInfo: %v, Command: %d, CommandType: %d",
					remoteAddr, authInfo, command, commandType)
			} else {
				logger.Printf("[CONN] RemoteAddr: %s, Authenticated: false, Command: %d, CommandType: %d",
					remoteAddr, command, commandType)
			}

			return next.Handle(ctx, command, commandType, data)
		})
	}
}
