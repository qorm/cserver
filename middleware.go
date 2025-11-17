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

// AuthMiddleware 简单的认证中间件示例
func AuthMiddleware(validTokens map[string]bool) Middleware {
	mu := sync.RWMutex{}

	return func(next Handler) Handler {
		return HandlerFunc(func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
			// 这里假设data的前16字节是token（简化示例）
			if len(data) < 16 {
				return nil, fmt.Errorf("authentication required: missing token")
			}

			token := string(data[:16])
			actualData := data[16:]

			mu.RLock()
			valid := validTokens[token]
			mu.RUnlock()

			if !valid {
				return nil, fmt.Errorf("authentication failed: invalid token")
			}

			// 认证成功，传递实际数据给下一个处理器
			response, err := next.Handle(ctx, command, commandType, actualData)
			return response, err
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
