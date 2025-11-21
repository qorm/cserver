package client

import (
	"container/list"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/qorm/chead"
)

// PooledClient 带连接池的客户端
type PooledClient struct {
	addr   string
	config *ClientConfig
	pool   *ConnPool
	closed bool
	mu     sync.RWMutex
}

// ConnPool 连接池
type ConnPool struct {
	addr          string
	config        *ClientConfig
	conns         *list.List // 空闲连接列表
	mu            sync.Mutex
	connCount     int // 当前连接总数
	waitQueue     chan struct{}
	closed        bool
	authenticator func(context.Context, *pooledConn) error // 认证函数
}

// pooledConn 池化连接
type pooledConn struct {
	conn          net.Conn
	pool          *ConnPool
	lastUsed      time.Time
	authenticated bool
	inUse         bool
}

// NewPooledClient 创建带连接池的客户端
func NewPooledClient(addr string, opts ...ClientOption) *PooledClient {
	config := DefaultClientConfig()
	for _, opt := range opts {
		opt(config)
	}

	pool := &ConnPool{
		addr:      addr,
		config:    config,
		conns:     list.New(),
		waitQueue: make(chan struct{}, config.MaxConnsPerAddr),
	}

	return &PooledClient{
		addr:   addr,
		config: config,
		pool:   pool,
	}
}

// SetAuthenticator 设置认证函数
func (c *PooledClient) SetAuthenticator(auth func(context.Context, []byte) error, authData []byte) {
	c.pool.authenticator = func(ctx context.Context, pc *pooledConn) error {
		if pc.authenticated {
			return nil
		}

		// 发送认证请求
		head := chead.NewHead()
		head.SetCommand(0)
		head.SetConfig(chead.REQ, chead.HaveResponse, 0)
		head.SetContentLength(uint32(len(authData)))

		if _, err := pc.conn.Write(head.GetBytes()); err != nil {
			return WrapError(ErrCodeConnectionClosed, "failed to send auth header", err)
		}

		if len(authData) > 0 {
			if _, err := pc.conn.Write(authData); err != nil {
				return WrapError(ErrCodeConnectionClosed, "failed to send auth data", err)
			}
		}

		// 读取响应
		headerBytes := make([]byte, 6)
		if _, err := io.ReadFull(pc.conn, headerBytes); err != nil {
			return WrapError(ErrCodeConnectionClosed, "failed to read auth response", err)
		}

		respHead, err := chead.HeadFromBytes(headerBytes)
		if err != nil {
			return WrapError(ErrCodeBadProtocol, "invalid auth response header", err)
		}

		if respHead.GetCommand() == 127 && respHead.GetCommandType() == 31 {
			return ErrAuthFailed
		}

		contentLength := respHead.GetContentLength()
		if contentLength > 0 {
			responseData := make([]byte, contentLength)
			if _, err := io.ReadFull(pc.conn, responseData); err != nil {
				return WrapError(ErrCodeConnectionClosed, "failed to read auth response data", err)
			}
		}

		pc.authenticated = true
		return nil
	}
}

// getConn 从连接池获取连接
func (p *ConnPool) getConn(ctx context.Context) (*pooledConn, error) {
	p.mu.Lock()

	// 尝试从空闲连接获取
	for p.conns.Len() > 0 {
		elem := p.conns.Front()
		pc := elem.Value.(*pooledConn)
		p.conns.Remove(elem)

		// 检查连接是否仍然有效
		if time.Since(pc.lastUsed) < p.config.KeepAlive {
			pc.inUse = true
			p.mu.Unlock()
			return pc, nil
		}

		// 连接过期，关闭
		pc.conn.Close()
		p.connCount--
	}

	// 如果没有空闲连接且未达到最大连接数，创建新连接
	if p.connCount < p.config.MaxConnsPerAddr {
		p.connCount++
		p.mu.Unlock()

		conn, err := p.dial(ctx)
		if err != nil {
			p.mu.Lock()
			p.connCount--
			p.mu.Unlock()
			return nil, err
		}

		pc := &pooledConn{
			conn:     conn,
			pool:     p,
			lastUsed: time.Now(),
			inUse:    true,
		}

		// 如果设置了认证函数，执行认证
		if p.authenticator != nil {
			if err := p.authenticator(ctx, pc); err != nil {
				pc.conn.Close()
				p.mu.Lock()
				p.connCount--
				p.mu.Unlock()
				return nil, err
			}
		}

		return pc, nil
	}

	// 达到最大连接数，等待可用连接
	p.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, ErrTimeout
	}
}

// putConn 将连接归还到连接池
func (p *ConnPool) putConn(pc *pooledConn, forceClose bool) {
	if forceClose {
		pc.conn.Close()
		p.mu.Lock()
		p.connCount--
		p.mu.Unlock()
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		pc.conn.Close()
		p.connCount--
		return
	}

	pc.lastUsed = time.Now()
	pc.inUse = false

	// 如果空闲连接数未达到上限，归还到池中
	if p.conns.Len() < p.config.MaxIdleConns {
		p.conns.PushBack(pc)
	} else {
		// 否则关闭连接
		pc.conn.Close()
		p.connCount--
	}
}

// dial 建立新连接
func (p *ConnPool) dial(ctx context.Context) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   p.config.ConnectTimeout,
		KeepAlive: p.config.KeepAlive,
	}

	conn, err := dialer.DialContext(ctx, "tcp", p.addr)
	if err != nil {
		return nil, WrapError(ErrCodeConnectionClosed, "failed to dial", err)
	}

	return conn, nil
}

// Close 关闭连接池
func (p *ConnPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	p.closed = true

	// 关闭所有空闲连接
	for p.conns.Len() > 0 {
		elem := p.conns.Front()
		pc := elem.Value.(*pooledConn)
		p.conns.Remove(elem)
		pc.conn.Close()
	}

	p.connCount = 0
	return nil
}

// SendRequest 发送请求并等待响应
func (c *PooledClient) SendRequest(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
	return c.sendRequest(ctx, command, commandType, data, true)
}

// SendRequestNoResponse 发送无需响应的请求
func (c *PooledClient) SendRequestNoResponse(ctx context.Context, command byte, commandType uint8, data []byte) error {
	_, err := c.sendRequest(ctx, command, commandType, data, false)
	return err
}

// sendRequest 发送请求的内部实现
func (c *PooledClient) sendRequest(ctx context.Context, command byte, commandType uint8, data []byte, needResponse bool) ([]byte, error) {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return nil, ErrConnectionClosed
	}
	c.mu.RUnlock()

	// 从连接池获取连接
	pc, err := c.pool.getConn(ctx)
	if err != nil {
		return nil, err
	}

	forceClose := false
	defer func() {
		c.pool.putConn(pc, forceClose)
	}()

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
		pc.conn.SetWriteDeadline(deadline)
	}

	// 发送头部
	if _, err := pc.conn.Write(head.GetBytes()); err != nil {
		forceClose = true
		return nil, WrapError(ErrCodeConnectionClosed, "failed to send header", err)
	}

	// 发送数据
	if len(data) > 0 {
		if _, err := pc.conn.Write(data); err != nil {
			forceClose = true
			return nil, WrapError(ErrCodeConnectionClosed, "failed to send data", err)
		}
	}

	// 如果不需要响应，直接返回
	if !needResponse {
		return nil, nil
	}

	// 读取响应
	return c.readResponse(ctx, pc, &forceClose)
}

// readResponse 读取响应
func (c *PooledClient) readResponse(ctx context.Context, pc *pooledConn, forceClose *bool) ([]byte, error) {
	// 设置读超时
	if c.config.ReadTimeout > 0 {
		deadline := time.Now().Add(c.config.ReadTimeout)
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		pc.conn.SetReadDeadline(deadline)
	}

	// 读取响应头部
	headerBytes := make([]byte, 6)
	if _, err := io.ReadFull(pc.conn, headerBytes); err != nil {
		*forceClose = true
		return nil, WrapError(ErrCodeConnectionClosed, "failed to read response header", err)
	}

	// 解析头部
	head, err := chead.HeadFromBytes(headerBytes)
	if err != nil {
		*forceClose = true
		return nil, WrapError(ErrCodeBadProtocol, "invalid response header", err)
	}

	// 检查是否为错误响应
	if head.GetCommand() == 127 && head.GetCommandType() == 31 {
		contentLength := head.GetContentLength()
		if contentLength > 0 {
			errorData := make([]byte, contentLength)
			if _, err := io.ReadFull(pc.conn, errorData); err != nil {
				*forceClose = true
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
	if _, err := io.ReadFull(pc.conn, responseData); err != nil {
		*forceClose = true
		return nil, WrapError(ErrCodeConnectionClosed, "failed to read response data", err)
	}

	return responseData, nil
}

// Close 关闭客户端
func (c *PooledClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true
	return c.pool.Close()
}

// Stats 连接池统计信息
func (c *PooledClient) Stats() PoolStats {
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()

	return PoolStats{
		IdleConns:  c.pool.conns.Len(),
		TotalConns: c.pool.connCount,
		MaxConns:   c.config.MaxConnsPerAddr,
	}
}

// PoolStats 连接池统计
type PoolStats struct {
	IdleConns  int // 空闲连接数
	TotalConns int // 总连接数
	MaxConns   int // 最大连接数
}

// Addr 获取服务器地址
func (c *PooledClient) Addr() string {
	return c.addr
}

// Config 获取客户端配置
func (c *PooledClient) Config() *ClientConfig {
	return c.config
}
