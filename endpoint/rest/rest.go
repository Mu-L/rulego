/*
 * Copyright 2023 The RuleGo Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package rest provides an HTTP endpoint implementation for the RuleGo framework.
// It allows creating HTTP servers that can receive and process incoming HTTP requests,
// routing them to appropriate rule chains or components for further processing.
//
// Key components in this package include:
// - Endpoint (alias Rest): Implements the HTTP server and request handling
// - RequestMessage: Represents an incoming HTTP request
// - ResponseMessage: Represents the HTTP response to be sent back
//
// The REST endpoint supports dynamic routing configuration, allowing users to
// define routes and their corresponding rule chain or component destinations.
// It also provides flexibility in handling different HTTP methods and content types.
//
// This package integrates with the broader RuleGo ecosystem, enabling seamless
// data flow from HTTP requests to rule processing and back to HTTP responses.
package rest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/rulego/rulego/api/types"
	"github.com/rulego/rulego/api/types/endpoint"
	nodeBase "github.com/rulego/rulego/components/base"
	"github.com/rulego/rulego/endpoint/impl"
	"github.com/rulego/rulego/utils/maps"
	"github.com/rulego/rulego/utils/runtime"
	"github.com/rulego/rulego/utils/str"
)

const (
	ContentTypeKey                      = "Content-Type"
	JsonContextType                     = "application/json"
	HeaderKeyAccessControlRequestMethod = "Access-Control-Request-Method"
	HeaderKeyAccessControlAllowMethods  = "Access-Control-Allow-Methods"
	HeaderKeyAccessControlAllowHeaders  = "Access-Control-Allow-Headers"
	HeaderKeyAccessControlAllowOrigin   = "Access-Control-Allow-Origin"
	HeaderValueAll                      = "*"
)

// Type 组件类型
const Type = types.EndpointTypePrefix + "http"

// Endpoint 别名
type Endpoint = Rest

var _ endpoint.Endpoint = (*Endpoint)(nil)
var _ endpoint.HttpEndpoint = (*Endpoint)(nil)

// RequestMessage http请求消息
type RequestMessage struct {
	request  *http.Request
	response http.ResponseWriter
	body     []byte
	//路径参数
	Params   httprouter.Params
	msg      *types.RuleMsg
	err      error
	Metadata *types.Metadata
}

func (r *RequestMessage) Body() []byte {
	if r.body == nil && r.request != nil {
		defer func() {
			if r.request.Body != nil {
				_ = r.request.Body.Close()
			}
		}()
		entry, _ := io.ReadAll(r.request.Body)
		r.body = entry
	}
	return r.body
}

func (r *RequestMessage) Headers() textproto.MIMEHeader {
	if r.request == nil {
		return nil
	}
	return textproto.MIMEHeader(r.request.Header)
}

func (r RequestMessage) From() string {
	if r.request == nil {
		return ""
	}
	return r.request.URL.String()
}

func (r *RequestMessage) GetParam(key string) string {
	if r.request == nil {
		return ""
	}
	if v := r.Params.ByName(key); v == "" {
		return r.request.FormValue(key)
	} else {
		return v
	}
}

func (r *RequestMessage) SetMsg(msg *types.RuleMsg) {
	r.msg = msg
}

func (r *RequestMessage) GetMsg() *types.RuleMsg {
	if r.msg == nil {
		dataType := types.TEXT
		var data string
		if r.request != nil && r.request.Method == http.MethodGet {
			dataType = types.JSON
			data = str.ToString(r.request.URL.Query())
		} else {
			if contentType := r.Headers().Get(ContentTypeKey); strings.HasPrefix(contentType, JsonContextType) {
				dataType = types.JSON
			}
			data = string(r.Body())
		}
		if r.Metadata == nil {
			r.Metadata = types.NewMetadata()
		}
		ruleMsg := types.NewMsg(0, r.From(), dataType, r.Metadata, data)
		r.msg = &ruleMsg
	}
	return r.msg
}

func (r *RequestMessage) SetStatusCode(statusCode int) {
}

func (r *RequestMessage) SetBody(body []byte) {
	r.body = body
}

func (r *RequestMessage) SetError(err error) {
	r.err = err
}

func (r *RequestMessage) GetError() error {
	return r.err
}

func (r *RequestMessage) Request() *http.Request {
	return r.request
}

func (r *RequestMessage) Response() http.ResponseWriter {
	return r.response
}

// ResponseMessage http响应消息
type ResponseMessage struct {
	request  *http.Request
	response http.ResponseWriter
	body     []byte
	to       string
	msg      *types.RuleMsg
	err      error
	mu       sync.RWMutex
}

func (r *ResponseMessage) Body() []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.body
}

func (r *ResponseMessage) Headers() textproto.MIMEHeader {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.response == nil {
		return nil
	}
	return textproto.MIMEHeader(r.response.Header())
}

func (r *ResponseMessage) From() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.request == nil {
		return ""
	}
	return r.request.URL.String()
}

func (r *ResponseMessage) GetParam(key string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.request == nil {
		return ""
	}
	return r.request.FormValue(key)
}

func (r *ResponseMessage) SetMsg(msg *types.RuleMsg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msg = msg
}
func (r *ResponseMessage) GetMsg() *types.RuleMsg {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.msg
}

func (r *ResponseMessage) SetStatusCode(statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.response != nil {
		r.response.WriteHeader(statusCode)
	}
}

func (r *ResponseMessage) SetBody(body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.body = body
	if r.response != nil {
		_, _ = r.response.Write(body)
	}
}

func (r *ResponseMessage) SetError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func (r *ResponseMessage) GetError() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.err
}

func (r *ResponseMessage) Request() *http.Request {
	return r.request
}
func (r *ResponseMessage) Response() http.ResponseWriter {
	return r.response
}

// Config Rest 服务配置
type Config struct {
	Server      string `json:"server"`      //服务器地址
	CertFile    string `json:"certFile"`    //证书文件
	CertKeyFile string `json:"certKeyFile"` //证书私钥文件
	//是否允许跨域
	AllowCors        bool
	ReadTimeout      int  `json:"readTimeout"`      // 读取超时时间（秒），0使用默认值10秒
	WriteTimeout     int  `json:"writeTimeout"`     // 写入超时时间（秒），0使用默认值10秒
	IdleTimeout      int  `json:"idleTimeout"`      // 空闲超时时间（秒），0使用默认值60秒
	DisableKeepalive bool `json:"disableKeepalive"` //  禁用keepalive
}

// Rest 接收端端点
type Rest struct {
	impl.BaseEndpoint
	nodeBase.SharedNode[*Rest]
	//配置
	Config     Config
	RuleConfig types.Config
	Server     *http.Server
	//http路由器
	router  *httprouter.Router
	started bool
}

// Type 组件类型
func (rest *Rest) Type() string {
	return Type
}

func (rest *Rest) New() types.Node {
	return &Rest{
		Config: Config{
			Server:       ":6333",
			ReadTimeout:  10,
			WriteTimeout: 10,
			IdleTimeout:  60,
		},
	}
}

// Init 初始化
func (rest *Rest) Init(ruleConfig types.Config, configuration types.Configuration) error {
	err := maps.Map2Struct(configuration, &rest.Config)
	if err != nil {
		return err
	}
	rest.RuleConfig = ruleConfig
	return rest.SharedNode.Init(rest.RuleConfig, rest.Type(), rest.Config.Server, false, func() (*Rest, error) {
		return rest.initServer()
	})
}

// Destroy 销毁
func (rest *Rest) Destroy() {
	_ = rest.Close()
}

func (rest *Rest) Restart() error {
	if rest.Server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rest.Server.Shutdown(ctx)
	}

	if rest.SharedNode.InstanceId != "" {
		if shared, err := rest.SharedNode.Get(); err == nil {
			return shared.Restart()
		} else {
			return err
		}
	}
	if rest.router != nil {
		rest.newRouter()
	}
	var oldRouter = make(map[string]endpoint.Router)

	rest.Lock()
	for id, router := range rest.RouterStorage {
		if !router.IsDisable() {
			oldRouter[id] = router
		}
	}
	rest.Unlock()

	rest.RouterStorage = make(map[string]endpoint.Router)
	rest.started = false

	if err := rest.Start(); err != nil {
		return err
	}
	for _, router := range oldRouter {
		if len(router.GetParams()) == 0 {
			router.SetParams("GET")
		}
		if !rest.HasRouter(router.GetId()) {
			if _, err := rest.AddRouter(router, router.GetParams()...); err != nil {
				rest.Printf("rest add router path:=%s error:%v", router.FromToString(), err)
				continue
			}
		}

	}
	return nil
}

func (rest *Rest) Close() error {
	if rest.Server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := rest.Server.Shutdown(ctx); err != nil {
			return err
		}
	}
	if rest.router != nil {
		rest.newRouter()
	}
	if rest.SharedNode.InstanceId != "" {
		if shared, err := rest.SharedNode.Get(); err == nil {
			rest.RLock()
			defer rest.RUnlock()
			for key := range rest.RouterStorage {
				shared.deleteRouter(key)
			}
			//重启共享服务
			return shared.Restart()
		}
	}

	rest.started = false
	rest.BaseEndpoint.Destroy()
	return nil
}

func (rest *Rest) Id() string {
	return rest.Config.Server
}

func (rest *Rest) AddRouter(router endpoint.Router, params ...interface{}) (id string, err error) {
	if len(params) <= 0 {
		return "", errors.New("need to specify HTTP method")
	} else if router == nil {
		return "", errors.New("router can not nil")
	} else {
		defer func() {
			if e := recover(); e != nil {
				err = fmt.Errorf("addRouter err :%v", e)
			}
		}()
		err2 := rest.addRouter(strings.ToUpper(str.ToString(params[0])), router)
		return router.GetId(), err2
	}
}

func (rest *Rest) RemoveRouter(routerId string, params ...interface{}) error {
	routerId = strings.TrimSpace(routerId)
	rest.Lock()
	defer rest.Unlock()
	if rest.RouterStorage != nil {
		if router, ok := rest.RouterStorage[routerId]; ok && !router.IsDisable() {
			router.Disable(true)
			return nil
		} else {
			return fmt.Errorf("router: %s not found", routerId)
		}
	}
	return nil
}

func (rest *Rest) deleteRouter(routerId string) {
	routerId = strings.TrimSpace(routerId)
	rest.Lock()
	defer rest.Unlock()
	if rest.RouterStorage != nil {
		delete(rest.RouterStorage, routerId)
	}
}

func (rest *Rest) Start() error {
	if err := rest.checkIsInitSharedNode(); err != nil {
		return err
	}
	if netResource, err := rest.SharedNode.Get(); err == nil {
		return netResource.startServer()
	} else {
		return err
	}
}

func (rest *Rest) Listen() (net.Listener, error) {
	addr := rest.Server.Addr
	if addr == "" {
		if rest.Config.CertKeyFile != "" && rest.Config.CertFile != "" {
			addr = ":https"
		} else {
			addr = ":http"
		}
	}
	return net.Listen("tcp", addr)
}

// addRouter 注册1个或者多个路由
//
// For GET, POST, PUT, PATCH and DELETE requests the respective shortcut
// functions can be used.
func (rest *Rest) addRouter(method string, routers ...endpoint.Router) error {
	method = strings.ToUpper(method)

	rest.Lock()
	defer rest.Unlock()

	if rest.RouterStorage == nil {
		rest.RouterStorage = make(map[string]endpoint.Router)
	}
	for _, item := range routers {
		path := strings.TrimSpace(item.FromToString())
		if id := item.GetId(); id == "" {
			item.SetId(rest.RouterKey(method, path))
		}
		//存储路由
		item.SetParams(method)
		rest.RouterStorage[item.GetId()] = item
		if rest.SharedNode.InstanceId != "" {
			if shared, err := rest.SharedNode.Get(); err == nil {
				return shared.addRouter(method, item)
			} else {
				return err
			}
		} else {
			if rest.router == nil {
				rest.newRouter()
			}
			isWait := false
			if from := item.GetFrom(); from != nil {
				if to := from.GetTo(); to != nil {
					isWait = to.IsWait()
				}
			}
			// 转换路径参数格式：将 {id} 格式转换为 :id 格式
			path = rest.convertPathParams(path)
			rest.router.Handle(method, path, rest.handler(item, isWait))
		}

	}
	return nil
}

func (rest *Rest) GET(routers ...endpoint.Router) endpoint.HttpEndpoint {
	rest.addRouter(http.MethodGet, routers...)
	return rest
}

func (rest *Rest) HEAD(routers ...endpoint.Router) endpoint.HttpEndpoint {
	rest.addRouter(http.MethodHead, routers...)
	return rest
}

func (rest *Rest) OPTIONS(routers ...endpoint.Router) endpoint.HttpEndpoint {
	rest.addRouter(http.MethodOptions, routers...)
	return rest
}

func (rest *Rest) POST(routers ...endpoint.Router) endpoint.HttpEndpoint {
	rest.addRouter(http.MethodPost, routers...)
	return rest
}

func (rest *Rest) PUT(routers ...endpoint.Router) endpoint.HttpEndpoint {
	rest.addRouter(http.MethodPut, routers...)
	return rest
}

func (rest *Rest) PATCH(routers ...endpoint.Router) endpoint.HttpEndpoint {
	rest.addRouter(http.MethodPatch, routers...)
	return rest
}

func (rest *Rest) DELETE(routers ...endpoint.Router) endpoint.HttpEndpoint {
	rest.addRouter(http.MethodDelete, routers...)
	return rest
}

func (rest *Rest) GlobalOPTIONS(handler http.Handler) endpoint.HttpEndpoint {
	rest.Router().GlobalOPTIONS = handler
	return rest
}

func (rest *Rest) RegisterStaticFiles(resourceMapping string) endpoint.HttpEndpoint {
	if resourceMapping != "" {
		mapping := strings.Split(resourceMapping, ",")
		for _, item := range mapping {
			files := strings.Split(item, "=")
			if len(files) == 2 {
				urlPath := strings.TrimSpace(files[0])
				localDir := strings.TrimSpace(files[1])

				// 移除 /*filepath 后缀以获取基础路径
				basePath := urlPath
				if strings.HasSuffix(urlPath, "/*filepath") {
					basePath = urlPath[:len(urlPath)-10]
				}

				// 确保路径以 /{filepath:*} 结尾，这是 fasthttp router 的要求
				if !strings.HasSuffix(urlPath, "/*filepath") {
					if strings.HasSuffix(basePath, "/") {
						urlPath = basePath + "*filepath"
					} else {
						urlPath = basePath + "/*filepath"
					}
				}
				rest.Router().ServeFiles(strings.TrimSpace(urlPath), http.Dir(strings.TrimSpace(localDir)))
			}
		}
	}
	return rest
}

func (rest *Rest) checkIsInitSharedNode() error {
	if !rest.SharedNode.IsInit() {
		err := rest.SharedNode.Init(rest.RuleConfig, rest.Type(), rest.Config.Server, false, func() (*Rest, error) {
			return rest.initServer()
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (rest *Rest) Router() *httprouter.Router {
	rest.checkIsInitSharedNode()

	if fromPool, err := rest.SharedNode.Get(); err != nil {
		rest.Printf("get router err :%v", err)
		return rest.newRouter()
	} else {
		return fromPool.router
	}
}

func (rest *Rest) RouterKey(method string, from string) string {
	return method + ":" + from
}

func (rest *Rest) handler(router endpoint.Router, isWait bool) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
		defer func() {
			//捕捉异常
			if e := recover(); e != nil {
				rest.Printf("http endpoint handler err :\n%v", runtime.Stack())
			}
		}()
		if router.IsDisable() {
			http.NotFound(w, r)
			return
		}
		metadata := types.NewMetadata()
		exchange := &endpoint.Exchange{
			In: &RequestMessage{
				request:  r,
				response: w,
				Params:   params,
				Metadata: metadata,
			},
			Out: &ResponseMessage{
				request:  r,
				response: w,
			},
		}

		//把路径参数放到msg元数据中
		for _, param := range params {
			metadata.PutValue(param.Key, param.Value)
		}

		//把url?参数放到msg元数据中
		for key, value := range r.URL.Query() {
			if len(value) > 1 {
				metadata.PutValue(key, str.ToString(value))
			} else {
				metadata.PutValue(key, value[0])
			}

		}
		var ctx = r.Context()
		if !isWait {
			//异步不能使用request context，否则后续执行会取消
			ctx = context.Background()
		}
		rest.DoProcess(ctx, router, exchange)
	}
}

func (rest *Rest) Printf(format string, v ...interface{}) {
	if rest.RuleConfig.Logger != nil {
		rest.RuleConfig.Logger.Printf(format, v...)
	}
}

// Started 返回服务是否已经启动
func (rest *Rest) Started() bool {
	return rest.started
}

// GetServer 获取HTTP服务
func (rest *Rest) GetServer() *http.Server {
	if rest.Server != nil {
		return rest.Server
	} else if rest.SharedNode.InstanceId != "" {
		if shared, err := rest.SharedNode.Get(); err == nil {
			return shared.Server
		}
	}
	return nil
}
func (rest *Rest) newRouter() *httprouter.Router {
	rest.router = httprouter.New()
	//设置跨域
	if rest.Config.AllowCors {
		rest.GlobalOPTIONS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(HeaderKeyAccessControlRequestMethod) != "" {
				// 设置 CORS 相关的响应头
				header := w.Header()
				header.Set(HeaderKeyAccessControlAllowMethods, HeaderValueAll)
				header.Set(HeaderKeyAccessControlAllowHeaders, HeaderValueAll)
				header.Set(HeaderKeyAccessControlAllowOrigin, HeaderValueAll)
			}
			// 返回 204 状态码
			w.WriteHeader(http.StatusNoContent)
		}))
		rest.AddInterceptors(func(router endpoint.Router, exchange *endpoint.Exchange) bool {
			exchange.Out.Headers().Set(HeaderKeyAccessControlAllowOrigin, HeaderValueAll)
			return true
		})
	}
	return rest.router
}
func (rest *Rest) initServer() (*Rest, error) {
	if rest.router == nil {
		rest.newRouter()
	}
	return rest, nil
}

func (rest *Rest) startServer() error {
	if rest.started {
		return nil
	}
	var err error

	// 创建HTTP服务器并应用超时配置
	rest.Server = &http.Server{
		Addr:    rest.Config.Server,
		Handler: rest.router,
	}

	// 应用读取超时配置
	if rest.Config.ReadTimeout > 0 {
		rest.Server.ReadTimeout = time.Duration(rest.Config.ReadTimeout) * time.Second
	} else {
		rest.Server.ReadTimeout = 10 * time.Second // 默认10秒
	}

	// 应用写入超时配置
	if rest.Config.WriteTimeout > 0 {
		rest.Server.WriteTimeout = time.Duration(rest.Config.WriteTimeout) * time.Second
	} else {
		rest.Server.WriteTimeout = 10 * time.Second // 默认10秒
	}

	// 应用空闲超时配置
	if rest.Config.IdleTimeout > 0 {
		rest.Server.IdleTimeout = time.Duration(rest.Config.IdleTimeout) * time.Second
	} else {
		rest.Server.IdleTimeout = 60 * time.Second // 默认60秒
	}

	// 应用禁用keepalive配置
	if rest.Config.DisableKeepalive {
		rest.Server.SetKeepAlivesEnabled(false)
	}
	ln, err := rest.Listen()
	if err != nil {
		return err
	}
	//标记已经启动
	rest.started = true

	isTls := rest.Config.CertKeyFile != "" && rest.Config.CertFile != ""
	if rest.OnEvent != nil {
		rest.OnEvent(endpoint.EventInitServer, rest)
	}
	if isTls {
		rest.Printf("started rest server with TLS on %s", rest.Config.Server)
		go func() {
			defer ln.Close()
			err = rest.Server.ServeTLS(ln, rest.Config.CertFile, rest.Config.CertKeyFile)
			if rest.OnEvent != nil {
				rest.OnEvent(endpoint.EventCompletedServer, err)
			}
		}()
	} else {
		rest.Printf("started rest server on %s", rest.Config.Server)
		go func() {
			defer ln.Close()
			err = rest.Server.Serve(ln)
			if rest.OnEvent != nil {
				rest.OnEvent(endpoint.EventCompletedServer, err)
			}
		}()
	}
	return err
}

// convertPathParams 转换路径参数格式：将 {id}格式转换为 :id  格式
func (rest *Rest) convertPathParams(path string) string {
	// 使用正则表达式匹配 :参数名 格式并转换为 {参数名} 格式
	re := regexp.MustCompile(`{([a-zA-Z_][a-zA-Z0-9_]*)}`)
	return re.ReplaceAllString(path, ":$1")
}
