package cserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/qorm/chead"
)

// Client TCP客户端
type Client struct {
	conn         net.Conn
	readTimeout  time.Duration
	writeTimeout time.Duration
}

// NewClient 创建新的TCP客户端
func NewClient() *Client {
	return &Client{
		readTimeout:  30 * time.Second,
		writeTimeout: 30 * time.Second,
	}
}

// SetTimeouts 设置读写超时
func (c *Client) SetTimeouts(read, write time.Duration) {
	c.readTimeout = read
	c.writeTimeout = write
}

// Connect 连接到服务器
func (c *Client) Connect(addr string) error {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", addr, err)
	}
	c.conn = conn
	return nil
}

// Close 关闭连接
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// SendRequest 发送请求
func (c *Client) SendRequest(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
	return c.sendRequestWithResponse(ctx, command, commandType, data, true)
}

// SendRequestNoResponse 发送无需响应的请求
func (c *Client) SendRequestNoResponse(ctx context.Context, command byte, commandType uint8, data []byte) error {
	_, err := c.sendRequestWithResponse(ctx, command, commandType, data, false)
	return err
}

// sendRequestWithResponse 发送请求的内部实现
func (c *Client) sendRequestWithResponse(ctx context.Context, command byte, commandType uint8, data []byte, needResponse bool) ([]byte, error) {
	if c.conn == nil {
		return nil, fmt.Errorf("not connected")
	}

	// 创建请求头部
	head := chead.NewHead()
	if err := head.SetCommand(command); err != nil {
		return nil, fmt.Errorf("invalid command: %w", err)
	}

	var response chead.Response = chead.NoResponse
	if needResponse {
		response = chead.HaveResponse
	}

	if err := head.SetConfig(chead.REQ, response, commandType); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	head.SetContentLength(uint32(len(data)))

	// 设置写超时
	if c.writeTimeout > 0 {
		deadline := time.Now().Add(c.writeTimeout)
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		c.conn.SetWriteDeadline(deadline)
	}

	// 发送头部
	if _, err := c.conn.Write(head.GetBytes()); err != nil {
		return nil, fmt.Errorf("failed to send header: %w", err)
	}

	// 发送数据
	if len(data) > 0 {
		if _, err := c.conn.Write(data); err != nil {
			return nil, fmt.Errorf("failed to send data: %w", err)
		}
	}

	// 如果不需要响应，直接返回
	if !needResponse {
		return nil, nil
	}

	// 读取响应
	return c.readResponse(ctx)
}

// readResponse 读取响应
func (c *Client) readResponse(ctx context.Context) ([]byte, error) {
	// 设置读超时
	if c.readTimeout > 0 {
		deadline := time.Now().Add(c.readTimeout)
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		c.conn.SetReadDeadline(deadline)
	}

	// 读取响应头部（6字节）
	headerBytes := make([]byte, 6)
	if _, err := io.ReadFull(c.conn, headerBytes); err != nil {
		return nil, fmt.Errorf("failed to read response header: %w", err)
	}

	// 解析头部
	head, err := chead.HeadFromBytes(headerBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid response header: %w", err)
	}

	// 检查是否为错误响应
	if head.GetCommand() == 255 && head.GetCommandType() == 31 {
		// 这是错误响应
		contentLength := head.GetContentLength()
		if contentLength > 0 {
			errorData := make([]byte, contentLength)
			if _, err := io.ReadFull(c.conn, errorData); err != nil {
				return nil, fmt.Errorf("failed to read error message: %w", err)
			}
			return nil, fmt.Errorf("server error: %s", string(errorData))
		}
		return nil, fmt.Errorf("server error: unknown error")
	}

	// 读取响应数据
	contentLength := head.GetContentLength()
	if contentLength == 0 {
		return nil, nil
	}

	responseData := make([]byte, contentLength)
	if _, err := io.ReadFull(c.conn, responseData); err != nil {
		return nil, fmt.Errorf("failed to read response data: %w", err)
	}

	return responseData, nil
}

// IsConnected 检查是否已连接
func (c *Client) IsConnected() bool {
	return c.conn != nil
}
