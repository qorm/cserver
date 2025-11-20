package cserver

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/qorm/chead"
)

// Handler 处理器接口，用户可以实现这个接口来处理不同的命令
type Handler interface {
	Handle(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error)
}

// ConnectionAuthenticator 连接级认证器接口
type ConnectionAuthenticator interface {
	// Authenticate 对连接进行认证，返回认证信息和错误
	// authData 是客户端发送的认证数据
	// 返回的 interface{} 可以是任何认证信息（如用户ID、角色等），会存储在连接上下文中
	Authenticate(authData []byte) (interface{}, error)
}

// ConnectionAuthenticatorFunc 函数类型适配器
type ConnectionAuthenticatorFunc func(authData []byte) (interface{}, error)

func (f ConnectionAuthenticatorFunc) Authenticate(authData []byte) (interface{}, error) {
	return f(authData)
}

// contextKey 用于在context中存储认证信息的键类型
type contextKey string

const (
	// AuthInfoKey 认证信息在context中的键
	AuthInfoKey contextKey = "auth_info"
	// RemoteAddrKey 远程地址在context中的键
	RemoteAddrKey contextKey = "remote_addr"
	// AuthInfoSetterKey 认证信息设置器在context中的键
	AuthInfoSetterKey contextKey = "auth_info_setter"
)

// AuthInfoSetter 认证信息设置器，用于在认证处理器中设置认证信息
type AuthInfoSetter struct {
	authInfo interface{}
	isSet    bool
	mu       sync.Mutex
}

// Set 设置认证信息
func (s *AuthInfoSetter) Set(authInfo interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authInfo = authInfo
	s.isSet = true
}

// Get 获取认证信息
func (s *AuthInfoSetter) Get() (interface{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authInfo, s.isSet
}

// HandlerFunc 函数类型适配器，允许普通函数作为Handler
type HandlerFunc func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error)

func (f HandlerFunc) Handle(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
	return f(ctx, command, commandType, data)
}

// Middleware 中间件类型
type Middleware func(Handler) Handler

// RouteKey 路由键，用于命令和命令类型的组合
type RouteKey struct {
	Command     byte
	CommandType uint8
}

// Server TCP服务器结构
type Server struct {
	addr            string
	listener        net.Listener
	handlers        map[RouteKey]Handler // 路由键到处理器的映射
	commandHandlers map[byte]Handler     // 仅基于命令的处理器映射（向后兼容）
	middleware      []Middleware         // 中间件链
	defaultHandler  Handler              // 默认处理器
	logger          *log.Logger

	// 配置选项
	readTimeout    time.Duration
	writeTimeout   time.Duration
	maxConnections int

	// 认证相关
	requireAuth bool // 是否要求认证

	// 运行时状态
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	connCount int32
	mu        sync.RWMutex
}

// New 创建新的TCP服务器
func New(addr string) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		addr:            addr,
		handlers:        make(map[RouteKey]Handler),
		commandHandlers: make(map[byte]Handler),
		middleware:      make([]Middleware, 0),
		readTimeout:     30 * time.Second,
		writeTimeout:    30 * time.Second,
		maxConnections:  1000,
		requireAuth:     false,
		ctx:             ctx,
		cancel:          cancel,
		logger:          log.New(log.Writer(), "[CSERVER] ", log.LstdFlags),
	}
}

// RegisterHandler 注册基于命令和命令类型的处理器
func (s *Server) RegisterHandler(command byte, commandType uint8, handler Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := RouteKey{Command: command, CommandType: commandType}
	s.handlers[key] = handler
}

// RegisterHandlerFunc 注册基于命令和命令类型的处理函数
func (s *Server) RegisterHandlerFunc(command byte, commandType uint8, handler HandlerFunc) {
	s.RegisterHandler(command, commandType, handler)
}

// RegisterCommandHandler 注册仅基于命令的处理器（向后兼容）
func (s *Server) RegisterCommandHandler(command byte, handler Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commandHandlers[command] = handler
}

// RegisterCommandHandlerFunc 注册仅基于命令的处理函数（向后兼容）
func (s *Server) RegisterCommandHandlerFunc(command byte, handler HandlerFunc) {
	s.RegisterCommandHandler(command, handler)
}

// SetDefaultHandler 设置默认处理器
func (s *Server) SetDefaultHandler(handler Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultHandler = handler
}

// Use 添加中间件
func (s *Server) Use(middleware Middleware) {
	s.middleware = append(s.middleware, middleware)
}

// SetLogger 设置日志器
func (s *Server) SetLogger(logger *log.Logger) {
	s.logger = logger
}

// SetTimeouts 设置读写超时
func (s *Server) SetTimeouts(read, write time.Duration) {
	s.readTimeout = read
	s.writeTimeout = write
}

// SetMaxConnections 设置最大连接数
func (s *Server) SetMaxConnections(max int) {
	s.maxConnections = max
}

// EnableAuth 启用认证要求（需要先注册 0,0 命令的认证处理器）
func (s *Server) EnableAuth() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requireAuth = true
}

// DisableAuth 禁用认证要求
func (s *Server) DisableAuth() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requireAuth = false
}

// SetAuthInfo 在上下文中设置认证信息（供认证处理器使用）
// 注意：此函数应该在认证处理器（命令 0,0）中调用
func SetAuthInfo(ctx context.Context, authInfo interface{}) {
	if setter, ok := ctx.Value(AuthInfoSetterKey).(*AuthInfoSetter); ok {
		setter.Set(authInfo)
	}
}

// Start 启动服务器
func (s *Server) Start() error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.addr, err)
	}

	s.listener = listener
	s.logger.Printf("Server listening on %s", s.addr)

	// 启动接受连接的goroutine
	s.wg.Add(1)
	go s.acceptConnections()

	return nil
}

// Stop 停止服务器
func (s *Server) Stop() error {
	s.cancel()
	if s.listener != nil {
		s.listener.Close()
	}
	s.wg.Wait()
	s.logger.Printf("Server stopped")
	return nil
}

// GetConnectionCount 获取当前连接数
func (s *Server) GetConnectionCount() int32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connCount
}

// acceptConnections 接受新连接
func (s *Server) acceptConnections() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
				s.logger.Printf("Failed to accept connection: %v", err)
				continue
			}
		}

		// 检查连接数限制
		s.mu.RLock()
		if s.connCount >= int32(s.maxConnections) {
			s.mu.RUnlock()
			conn.Close()
			s.logger.Printf("Connection rejected: max connections reached")
			continue
		}
		s.mu.RUnlock()

		// 处理连接
		s.wg.Add(1)
		go s.handleConnection(conn)
	}
}

// handleConnection 处理单个连接
func (s *Server) handleConnection(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	// 更新连接计数
	s.mu.Lock()
	s.connCount++
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.connCount--
		s.mu.Unlock()
	}()

	s.logger.Printf("New connection from %s", conn.RemoteAddr())

	// 创建独立的连接上下文，不从服务器全局上下文继承（避免认证信息泄露到其他连接）
	// 但保留服务器的取消信号监听能力
	connCtx, connCancel := context.WithCancel(context.Background())
	defer connCancel()

	// 添加远程地址信息
	ctx := context.WithValue(connCtx, RemoteAddrKey, conn.RemoteAddr().String())
	ctxPtr := &ctx // 使用指针以便认证处理器可以更新上下文

	// 开始处理请求循环
	authenticated := false

	// 在后台监听服务器关闭信号
	go func() {
		<-s.ctx.Done()
		connCancel()
	}()

	for {
		select {
		case <-connCtx.Done():
			return
		default:
		}

		// 设置读超时
		if s.readTimeout > 0 {
			conn.SetReadDeadline(time.Now().Add(s.readTimeout))
		}

		// 读取协议头部（6字节）
		headerBytes := make([]byte, 6)
		if _, err := io.ReadFull(conn, headerBytes); err != nil {
			if err != io.EOF {
				s.logger.Printf("Failed to read header from %s: %v", conn.RemoteAddr(), err)
			}
			return
		}

		// 解析头部
		head, err := chead.HeadFromBytes(headerBytes)
		if err != nil {
			s.logger.Printf("Invalid header from %s: %v", conn.RemoteAddr(), err)
			return
		}

		// 读取消息体
		contentLength := head.GetContentLength()
		var data []byte
		if contentLength > 0 {
			data = make([]byte, contentLength)
			if _, err := io.ReadFull(conn, data); err != nil {
				s.logger.Printf("Failed to read data from %s: %v", conn.RemoteAddr(), err)
				return
			}
		}

		// 处理请求
		if head.GetDirection() == chead.REQ {
			command := head.GetCommand()
			commandType := head.GetCommandType()

			// 如果是认证命令 (0,0)，处理后更新认证状态
			if command == 0 && commandType == 0 {
				newCtx := s.handleRequest(conn, *ctxPtr, head, data)
				*ctxPtr = newCtx
				if newCtx.Value(AuthInfoKey) != nil {
					authenticated = true
				}
			} else {
				// 如果需要认证但未认证，拒绝请求
				if s.requireAuth && !authenticated {
					s.logger.Printf("Unauthenticated request from %s, command: %d, commandType: %d",
						conn.RemoteAddr(), command, commandType)
					s.sendErrorResponse(conn, head, "authentication required")
					continue
				}
				_ = s.handleRequest(conn, *ctxPtr, head, data)
			}
		}
	}
}

// handleRequest 处理请求并返回可能更新的上下文（用于认证）
func (s *Server) handleRequest(conn net.Conn, connCtx context.Context, head *chead.HEAD, data []byte) context.Context {
	command := head.GetCommand()
	commandType := head.GetCommandType()

	// 获取处理器，优先匹配精确路由（command + commandType）
	s.mu.RLock()
	key := RouteKey{Command: command, CommandType: commandType}
	handler, exists := s.handlers[key]
	if !exists {
		// 尝试仅基于命令的处理器
		handler, exists = s.commandHandlers[command]
		if !exists {
			handler = s.defaultHandler
		}
	}
	s.mu.RUnlock()

	if handler == nil {
		s.logger.Printf("No handler for command %d, commandType %d", command, commandType)
		s.sendErrorResponse(conn, head, fmt.Sprintf("No handler for command %d, commandType %d", command, commandType))
		return connCtx
	}

	// 应用中间件链
	finalHandler := handler
	for i := len(s.middleware) - 1; i >= 0; i-- {
		finalHandler = s.middleware[i](finalHandler)
	}

	// 创建请求上下文，继承连接上下文（包含认证信息）
	ctx, cancel := context.WithTimeout(connCtx, s.writeTimeout)
	defer cancel()

	// 如果是认证命令（0,0），添加认证信息设置器到上下文
	var authSetter *AuthInfoSetter
	if command == 0 && commandType == 0 {
		authSetter = &AuthInfoSetter{}
		ctx = context.WithValue(ctx, AuthInfoSetterKey, authSetter)
	}

	// 调用处理器
	response, err := finalHandler.Handle(ctx, command, commandType, data)
	if err != nil {
		s.logger.Printf("Handler error for command %d, commandType %d: %v", command, commandType, err)
		s.sendErrorResponse(conn, head, err.Error())
		return connCtx
	}

	// 发送响应（如果需要响应）
	if head.GetResponse() == chead.HaveResponse {
		s.sendSuccessResponse(conn, head, response)
	}

	// 如果是认证命令（0,0），从设置器中提取认证信息并更新连接上下文
	if command == 0 && commandType == 0 && authSetter != nil {
		if authInfo, isSet := authSetter.Get(); isSet {
			s.logger.Printf("Authentication info set for connection from %s", conn.RemoteAddr())
			return context.WithValue(connCtx, AuthInfoKey, authInfo)
		}
	}

	return connCtx
}

// sendSuccessResponse 发送成功响应
func (s *Server) sendSuccessResponse(conn net.Conn, requestHead *chead.HEAD, data []byte) {
	responseHead := chead.NewHead()
	responseHead.SetCommand(requestHead.GetCommand())
	responseHead.SetConfig(chead.REP, chead.NoResponse, requestHead.GetCommandType())
	responseHead.SetContentLength(uint32(len(data)))

	// 设置写超时
	if s.writeTimeout > 0 {
		conn.SetWriteDeadline(time.Now().Add(s.writeTimeout))
	}

	// 发送头部
	if _, err := conn.Write(responseHead.GetBytes()); err != nil {
		s.logger.Printf("Failed to write response header: %v", err)
		return
	}

	// 发送数据
	if len(data) > 0 {
		if _, err := conn.Write(data); err != nil {
			s.logger.Printf("Failed to write response data: %v", err)
		}
	}
}

// sendErrorResponse 发送错误响应
func (s *Server) sendErrorResponse(conn net.Conn, requestHead *chead.HEAD, errMsg string) {
	responseHead := chead.NewHead()
	responseHead.SetCommand(127) // 使用最大有效命令值表示错误
	responseHead.SetConfig(chead.REP, chead.NoResponse, 31)

	errorData := []byte(errMsg)
	responseHead.SetContentLength(uint32(len(errorData)))

	// 设置写超时
	if s.writeTimeout > 0 {
		conn.SetWriteDeadline(time.Now().Add(s.writeTimeout))
	}

	// 发送头部和数据
	if _, err := conn.Write(responseHead.GetBytes()); err != nil {
		s.logger.Printf("Failed to write error header: %v", err)
		return
	}

	if _, err := conn.Write(errorData); err != nil {
		s.logger.Printf("Failed to write error data: %v", err)
	}
}
