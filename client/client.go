package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/qorm/chead"
)

// ClientOption 客户端选项
type ClientOption func(*ClientConfig)

// ClientConfig 客户端配置
type ClientConfig struct {
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ConnectTimeout  time.Duration
	MaxRetries      int
	RetryInterval   time.Duration
	KeepAlive       time.Duration
	MaxIdleConns    int
	MaxConnsPerAddr int
}

// DefaultClientConfig 默认客户端配置
func DefaultClientConfig() *ClientConfig {
	return &ClientConfig{
		ReadTimeout:     30 * time.Second,
		WriteTimeout:    30 * time.Second,
		ConnectTimeout:  10 * time.Second,
		MaxRetries:      3,
		RetryInterval:   time.Second,
		KeepAlive:       30 * time.Second,
		MaxIdleConns:    10,
		MaxConnsPerAddr: 100,
	}
}

// WithReadTimeout 设置读超时
func WithReadTimeout(d time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.ReadTimeout = d
	}
}

// WithWriteTimeout 设置写超时
func WithWriteTimeout(d time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.WriteTimeout = d
	}
}

// WithConnectTimeout 设置连接超时
func WithConnectTimeout(d time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.ConnectTimeout = d
	}
}

// WithMaxRetries 设置最大重试次数
func WithMaxRetries(n int) ClientOption {
	return func(c *ClientConfig) {
		c.MaxRetries = n
	}
}

// WithRetryInterval 设置重试间隔
func WithRetryInterval(d time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.RetryInterval = d
	}
}

// WithMaxIdleConns 设置最大空闲连接数
func WithMaxIdleConns(n int) ClientOption {
	return func(c *ClientConfig) {
		c.MaxIdleConns = n
	}
}

// WithMaxConnsPerAddr 设置每个地址的最大连接数
func WithMaxConnsPerAddr(n int) ClientOption {
	return func(c *ClientConfig) {
		c.MaxConnsPerAddr = n
	}
}

// Client TCP客户端（简化版，单连接）
type Client struct {
	addr          string
	conn          net.Conn
	config        *ClientConfig
	mu            sync.RWMutex
	authenticated bool
	closed        bool
}

// NewClient 创建新的TCP客户端
func NewClient(addr string, opts ...ClientOption) *Client {
	config := DefaultClientConfig()
	for _, opt := range opts {
		opt(config)
	}

	return &Client{
		addr:   addr,
		config: config,
	}
}

// Connect 连接到服务器
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil // 已连接
	}

	// 创建带超时的连接
	dialer := &net.Dialer{
		Timeout:   c.config.ConnectTimeout,
		KeepAlive: c.config.KeepAlive,
	}

	conn, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return WrapError(ErrCodeConnectionClosed, "failed to connect", err)
	}

	c.conn = conn
	c.closed = false
	return nil
}

// Close 关闭连接
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		c.closed = true
		c.authenticated = false
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

// IsConnected 检查是否已连接
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn != nil && !c.closed
}

// IsAuthenticated 检查是否已认证
func (c *Client) IsAuthenticated() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.authenticated
}

// Authenticate 发送认证请求（使用命令 0,0）
func (c *Client) Authenticate(ctx context.Context, authData []byte) error {
	_, err := c.SendRequest(ctx, 0, 0, authData)
	if err != nil {
		c.mu.Lock()
		c.authenticated = false
		c.mu.Unlock()
		return WrapError(ErrCodeAuthFailed, "authentication failed", err)
	}

	c.mu.Lock()
	c.authenticated = true
	c.mu.Unlock()
	return nil
}

// SendRequest 发送请求并等待响应
func (c *Client) SendRequest(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
	return c.sendWithRetry(ctx, command, commandType, data, true)
}

// SendRequestNoResponse 发送无需响应的请求
func (c *Client) SendRequestNoResponse(ctx context.Context, command byte, commandType uint8, data []byte) error {
	_, err := c.sendWithRetry(ctx, command, commandType, data, false)
	return err
}

// sendWithRetry 发送请求（支持重试）
func (c *Client) sendWithRetry(ctx context.Context, command byte, commandType uint8, data []byte, needResponse bool) ([]byte, error) {
	var lastErr error

	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			// 等待后重试
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.config.RetryInterval):
			}
		}

		response, err := c.sendRequest(ctx, command, commandType, data, needResponse)
		if err == nil {
			return response, nil
		}

		lastErr = err

		// 检查是否应该重试
		if !c.shouldRetry(err) {
			break
		}

		// 如果连接断开，尝试重新连接
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			c.Close()
			if connErr := c.Connect(ctx); connErr != nil {
				lastErr = connErr
				break
			}
		}
	}

	return nil, lastErr
}

// shouldRetry 判断是否应该重试
func (c *Client) shouldRetry(err error) bool {
	// 超时、连接错误等可重试
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	return false
}

// sendRequest 发送请求的内部实现
func (c *Client) sendRequest(ctx context.Context, command byte, commandType uint8, data []byte, needResponse bool) ([]byte, error) {
	c.mu.RLock()
	if c.conn == nil || c.closed {
		c.mu.RUnlock()
		return nil, ErrConnectionClosed
	}
	conn := c.conn
	c.mu.RUnlock()

	// 创建请求头部
	head := chead.NewHead()
	if err := head.SetCommand(command); err != nil {
		return nil, WrapError(ErrCodeInvalidRequest, "invalid command", err)
	}

	var response chead.Response = chead.NoResponse
	if needResponse {
		response = chead.HaveResponse
	}
	if err := head.SetConfig(chead.REQ, response, commandType); err != nil {
		return nil, WrapError(ErrCodeInvalidRequest, "invalid config", err)
	}

	head.SetContentLength(uint32(len(data)))

	// 设置写超时
	if c.config.WriteTimeout > 0 {
		deadline := time.Now().Add(c.config.WriteTimeout)
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		conn.SetWriteDeadline(deadline)
	}

	// 发送头部
	if _, err := conn.Write(head.GetBytes()); err != nil {
		return nil, WrapError(ErrCodeConnectionClosed, "failed to send header", err)
	}

	// 发送数据
	if len(data) > 0 {
		if _, err := conn.Write(data); err != nil {
			return nil, WrapError(ErrCodeConnectionClosed, "failed to send data", err)
		}
	}

	// 如果不需要响应，直接返回
	if !needResponse {
		return nil, nil
	}

	// 读取响应
	return c.readResponse(ctx, conn)
}

// readResponse 读取响应
func (c *Client) readResponse(ctx context.Context, conn net.Conn) ([]byte, error) {
	// 设置读超时
	if c.config.ReadTimeout > 0 {
		deadline := time.Now().Add(c.config.ReadTimeout)
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		conn.SetReadDeadline(deadline)
	}

	// 读取响应头部（6字节）
	headerBytes := make([]byte, 6)
	if _, err := io.ReadFull(conn, headerBytes); err != nil {
		return nil, WrapError(ErrCodeConnectionClosed, "failed to read response header", err)
	}

	// 解析头部
	head, err := chead.HeadFromBytes(headerBytes)
	if err != nil {
		return nil, WrapError(ErrCodeBadProtocol, "invalid response header", err)
	}

	// 检查是否为错误响应
	if head.GetCommand() == 127 && head.GetCommandType() == 31 {
		contentLength := head.GetContentLength()
		if contentLength > 0 {
			errorData := make([]byte, contentLength)
			if _, err := io.ReadFull(conn, errorData); err != nil {
				return nil, WrapError(ErrCodeConnectionClosed, "failed to read error message", err)
			}
			return nil, NewError(ErrCodeInternalError, "server error", fmt.Errorf("%s", string(errorData)))
		}
		return nil, NewError(ErrCodeInternalError, "unknown server error", nil)
	}

	// 读取响应数据
	contentLength := head.GetContentLength()
	if contentLength == 0 {
		return nil, nil
	}

	responseData := make([]byte, contentLength)
	if _, err := io.ReadFull(conn, responseData); err != nil {
		return nil, WrapError(ErrCodeConnectionClosed, "failed to read response data", err)
	}

	return responseData, nil
}

// Addr 获取服务器地址
func (c *Client) Addr() string {
	return c.addr
}

// Config 获取客户端配置
func (c *Client) Config() *ClientConfig {
	return c.config
}
