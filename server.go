package cserver

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qorm/chead"
)

// HandlerFunc 处理函数类型
type HandlerFunc func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error)

// Middleware 中间件类型
type Middleware func(HandlerFunc) HandlerFunc

// contextKey 上下文键类型
type contextKey int

const (
	authInfoKey contextKey = iota
	remoteAddrKey
	authInfoSetterKey
)

// AuthInfoKey 认证信息上下文键（公开）
const AuthInfoKey = authInfoKey

// RemoteAddrKey 远程地址上下文键（公开）
const RemoteAddrKey = remoteAddrKey

// AuthInfoSetterKey 认证信息设置器上下文键（公开）
const AuthInfoSetterKey = authInfoSetterKey

// AuthInfoSetter 认证信息设置器
type AuthInfoSetter struct {
	mu       sync.RWMutex
	authInfo interface{}
	isSet    bool
}

// Set 设置认证信息
func (a *AuthInfoSetter) Set(authInfo interface{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.authInfo = authInfo
	a.isSet = true
}

// Get 获取认证信息
func (a *AuthInfoSetter) Get() (interface{}, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.authInfo, a.isSet
}

// RouteKey 路由键
type RouteKey struct {
	Command     byte
	CommandType uint8
}

// route 路由信息（预编译的处理器+中间件链）
type route struct {
	handler     HandlerFunc
	globalChain HandlerFunc // 预编译的全局中间件链
	authChain   HandlerFunc // 预编译的全局+认证中间件链
}

// Server TCP服务器
type Server struct {
	addr     string
	listener net.Listener
	routes   sync.Map // map[RouteKey]*route - 线程安全的路由表
	logger   *log.Logger

	// 中间件
	mu             sync.RWMutex
	middleware     []Middleware // 全局中间件
	authMiddleware []Middleware // 认证中间件
	defaultHandler HandlerFunc

	// 配置
	readTimeout    time.Duration
	writeTimeout   time.Duration
	maxConnections int
	requireAuth    bool

	// 运行时状态
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	connCount atomic.Int32
}

// New 创建新的TCP服务器
func New(addr string) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		addr:           addr,
		middleware:     make([]Middleware, 0),
		authMiddleware: make([]Middleware, 0),
		readTimeout:    30 * time.Second,
		writeTimeout:   30 * time.Second,
		maxConnections: 1000,
		requireAuth:    false,
		ctx:            ctx,
		cancel:         cancel,
		logger:         log.New(log.Writer(), "[CSERVER] ", log.LstdFlags),
	}
}

// Handle 注册处理器（支持可选的路由级中间件）
func (s *Server) Handle(command byte, commandType uint8, handler HandlerFunc, middlewares ...Middleware) {
	key := RouteKey{Command: command, CommandType: commandType}

	// 应用路由级中间件（从右到左）
	finalHandler := handler
	for i := len(middlewares) - 1; i >= 0; i-- {
		finalHandler = middlewares[i](finalHandler)
	}

	// 预编译中间件链
	s.mu.RLock()
	globalChain := s.buildChain(finalHandler, s.middleware)
	authChain := s.buildChain(finalHandler, append(s.middleware, s.authMiddleware...))
	s.mu.RUnlock()

	r := &route{
		handler:     finalHandler,
		globalChain: globalChain,
		authChain:   authChain,
	}

	s.routes.Store(key, r)
}

// HandleAuth 注册需要认证的处理器
func (s *Server) HandleAuth(command byte, commandType uint8, handler HandlerFunc, middlewares ...Middleware) {
	// 为需要认证的处理器自动添加认证检查中间件
	allMiddlewares := append([]Middleware{RequireAuthMiddleware()}, middlewares...)
	s.Handle(command, commandType, handler, allMiddlewares...)
}

// RegisterHandlerFunc 注册处理器（兼容旧API）
func (s *Server) RegisterHandlerFunc(command byte, commandType uint8, handler HandlerFunc) {
	s.Handle(command, commandType, handler)
}

// Use 添加全局中间件（应用到所有请求）
func (s *Server) Use(middlewares ...Middleware) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.middleware = append(s.middleware, middlewares...)

	// 重新编译所有路由的中间件链
	s.recompileRoutes()
}

// UseAuth 添加认证中间件（仅应用到已认证的请求）
func (s *Server) UseAuth(middlewares ...Middleware) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authMiddleware = append(s.authMiddleware, middlewares...)

	// 重新编译所有路由的中间件链
	s.recompileRoutes()
}

// SetDefaultHandler 设置默认处理器
func (s *Server) SetDefaultHandler(handler HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultHandler = handler
}

// buildChain 构建中间件链
func (s *Server) buildChain(handler HandlerFunc, middlewares []Middleware) HandlerFunc {
	result := handler
	for i := len(middlewares) - 1; i >= 0; i-- {
		result = middlewares[i](result)
	}
	return result
}

// recompileRoutes 重新编译所有路由的中间件链
func (s *Server) recompileRoutes() {
	s.routes.Range(func(key, value interface{}) bool {
		r := value.(*route)
		r.globalChain = s.buildChain(r.handler, s.middleware)
		r.authChain = s.buildChain(r.handler, append(s.middleware, s.authMiddleware...))
		return true
	})
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

// ChainMiddleware 链式组合多个中间件
func ChainMiddleware(middlewares ...Middleware) Middleware {
	return func(next HandlerFunc) HandlerFunc {
		// 从右到左应用中间件
		result := next
		for i := len(middlewares) - 1; i >= 0; i-- {
			result = middlewares[i](result)
		}
		return result
	}
}

// RequireAuthMiddleware 要求认证的中间件
func RequireAuthMiddleware() Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx context.Context, command byte, commandType uint8, data []byte) ([]byte, error) {
			if _, authenticated := GetAuthInfo(ctx); !authenticated {
				return nil, ErrNotAuthenticated
			}
			return next(ctx, command, commandType, data)
		}
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
	return s.connCount.Load()
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
		if s.connCount.Load() >= int32(s.maxConnections) {
			conn.Close()
			s.logger.Printf("Connection rejected: max connections reached")
			continue
		}

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
	s.connCount.Add(1)
	defer s.connCount.Add(-1)

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

	// 获取路由
	key := RouteKey{Command: command, CommandType: commandType}
	value, exists := s.routes.Load(key)

	var finalHandler HandlerFunc
	if exists {
		r := value.(*route)
		// 检查是否已认证，选择对应的中间件链
		if _, authenticated := GetAuthInfo(connCtx); authenticated {
			finalHandler = r.authChain
		} else {
			finalHandler = r.globalChain
		}
	} else {
		// 使用默认处理器
		s.mu.RLock()
		finalHandler = s.defaultHandler
		s.mu.RUnlock()
	}

	if finalHandler == nil {
		s.logger.Printf("No handler for command %d, commandType %d", command, commandType)
		s.sendErrorResponse(conn, head, fmt.Sprintf("No handler for command %d, commandType %d", command, commandType))
		return connCtx
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
	response, err := finalHandler(ctx, command, commandType, data)
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
