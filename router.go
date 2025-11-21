package cserver

// Route 路由构建器 - 针对特定 commandType 的路由配置
type Route struct {
	server      *Server
	commandType uint8
	middlewares []Middleware
}

// NewRoute 创建路由构建器（指定 commandType）
func (s *Server) NewRoute(commandType uint8) *Route {
	return &Route{
		server:      s,
		commandType: commandType,
		middlewares: make([]Middleware, 0),
	}
}

// Use 添加中间件到路由
func (r *Route) Use(middlewares ...Middleware) *Route {
	r.middlewares = append(r.middlewares, middlewares...)
	return r
}

// Handle 注册处理器（指定 command）
func (r *Route) Handle(command byte, handler HandlerFunc) *Route {
	r.server.Handle(command, r.commandType, handler, r.middlewares...)
	return r
}

// HandleAuth 注册需要认证的处理器（指定 command）
func (r *Route) HandleAuth(command byte, handler HandlerFunc) *Route {
	r.server.HandleAuth(command, r.commandType, handler, r.middlewares...)
	return r
}

// // RouteGroup 路由组
// type RouteGroup struct {
// 	server      *Server
// 	middlewares []Middleware
// }

// // NewGroup 创建路由组
// func (s *Server) NewGroup() *RouteGroup {
// 	return &RouteGroup{
// 		server:      s,
// 		middlewares: make([]Middleware, 0),
// 	}
// }

// // Use 添加中间件到路由组
// func (g *RouteGroup) Use(middlewares ...Middleware) *RouteGroup {
// 	g.middlewares = append(g.middlewares, middlewares...)
// 	return g
// }

// // Route 创建带组中间件的路由
// func (g *RouteGroup) Route(commandType uint8) *Route {
// 	return &Route{
// 		server:      g.server,
// 		commandType: commandType,
// 		middlewares: append([]Middleware{}, g.middlewares...),
// 	}
// }

// // Handle 快捷方法：直接注册处理器
// func (g *RouteGroup) Handle(command byte, commandType uint8, handler HandlerFunc) {
// 	g.server.Handle(command, commandType, handler, g.middlewares...)
// }

// // HandleAuth 快捷方法：直接注册需要认证的处理器
// func (g *RouteGroup) HandleAuth(command byte, commandType uint8, handler HandlerFunc) {
// 	g.server.HandleAuth(command, commandType, handler, g.middlewares...)
// }
