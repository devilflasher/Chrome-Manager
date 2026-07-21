package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
)

// ServiceBridge 定义 API 层需要调用的 ChromeService 方法接口
// 通过接口解耦，避免循环引用
type ServiceBridge interface {
	// 窗口管理
	ImportWindows() (interface{}, error)
	OpenWindows(numbers string) error
	ArrangeWindows(mode string) error
	CloseWindows(numbers string) error
	ZoomWindows(level int, numbers string) error
	SelectAllWindows(selected bool) error

	// 同步控制
	StartSync(masterNumber int, slaveNumbers []int) error
	StopSync() error
	GetSyncStatus() map[string]interface{}
	SetMasterWindow(windowNumber int) error

	// 标签管理
	KeepOnlyCurrentTab() error
	KeepOnlyNewTab() error
	NavigateWindowsToURL(url string, numbers string) error

	// 批量输入
	InputTextToAllWindows(lines []string, overwrite, delayed bool) error
	InputRandomNumberToAllWindows(min, max float64, isFloat bool, decimalPlaces int, overwrite, delayed bool) error

	// 系统级操控（向主控窗口发送键鼠信号，Hook 自动同步到从窗口）
	SendKeystrokeToMaster(keys string, text string) error
	SendClickToMaster(x, y int, button string) error
	FocusMasterWindow() error
}

// Server 是 OpenClaw API 的 HTTP 服务器
type Server struct {
	bridge   ServiceBridge
	token    string
	port     int
	listener net.Listener
	mu       sync.Mutex
	running  bool
}

// NewServer 创建 API 服务器实例
func NewServer(bridge ServiceBridge, port int, token string) *Server {
	if port == 0 {
		port = 18923
	}
	return &Server{
		bridge: bridge,
		token:  token,
		port:   port,
	}
}

// GenerateToken 生成随机 API Token
func GenerateToken() string {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return "chromemgr-default-token"
	}
	return hex.EncodeToString(bytes)
}

// Start 启动 API 服务器（非阻塞）
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return nil
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("API 服务器启动失败 (端口 %d): %v", s.port, err)
	}

	s.listener = listener
	s.running = true

	go func() {
		server := &http.Server{Handler: mux}
		if err := server.Serve(listener); err != nil && s.running {
			log.Printf("API 服务器异常退出: %v", err)
		}
	}()

	log.Printf("✅ OpenClaw API 服务器已启动: http://%s", addr)
	return nil
}

// Stop 停止 API 服务器
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	s.running = false
	if s.listener != nil {
		s.listener.Close()
	}
	log.Printf("🛑 OpenClaw API 服务器已停止")
}

// IsRunning 检查服务器是否在运行
func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// --- 路由注册 ---

func (s *Server) registerRoutes(mux *http.ServeMux) {
	// 窗口管理
	mux.HandleFunc("/api/v1/windows", s.auth(s.handleGetWindows))
	mux.HandleFunc("/api/v1/windows/open", s.auth(s.handleOpenWindows))
	mux.HandleFunc("/api/v1/windows/import", s.auth(s.handleImportWindows))
	mux.HandleFunc("/api/v1/windows/arrange", s.auth(s.handleArrangeWindows))
	mux.HandleFunc("/api/v1/windows/close", s.auth(s.handleCloseWindows))
	mux.HandleFunc("/api/v1/windows/zoom", s.auth(s.handleZoomWindows))
	mux.HandleFunc("/api/v1/windows/select-all", s.auth(s.handleSelectAllWindows))

	// 同步控制
	mux.HandleFunc("/api/v1/sync/status", s.auth(s.handleSyncStatus))
	mux.HandleFunc("/api/v1/sync/start", s.auth(s.handleStartSync))
	mux.HandleFunc("/api/v1/sync/stop", s.auth(s.handleStopSync))
	mux.HandleFunc("/api/v1/sync/master", s.auth(s.handleSetMaster))

	// 标签管理
	mux.HandleFunc("/api/v1/tabs/keep-current", s.auth(s.handleKeepCurrentTab))
	mux.HandleFunc("/api/v1/tabs/keep-new", s.auth(s.handleKeepNewTab))
	mux.HandleFunc("/api/v1/tabs/navigate", s.auth(s.handleNavigate))

	// 批量输入
	mux.HandleFunc("/api/v1/input/text", s.auth(s.handleInputText))
	mux.HandleFunc("/api/v1/input/random", s.auth(s.handleInputRandom))

	// 系统级操控（同步场景核心）
	mux.HandleFunc("/api/v1/input/keystroke", s.auth(s.handleKeystroke))
	mux.HandleFunc("/api/v1/input/click", s.auth(s.handleClick))
	mux.HandleFunc("/api/v1/focus/master", s.auth(s.handleFocusMaster))
	mux.HandleFunc("/api/v1/input/focus/master", s.auth(s.handleFocusMaster))
}

// --- 认证中间件 ---

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			s.jsonError(w, "未提供认证令牌", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token != s.token {
			s.jsonError(w, "认证令牌无效", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// --- 窗口管理 ---

func (s *Server) handleGetWindows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "仅支持 GET 方法", http.StatusMethodNotAllowed)
		return
	}
	windows, err := s.bridge.ImportWindows()
	if err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, windows)
}

func (s *Server) handleOpenWindows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Numbers string `json:"numbers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := s.bridge.OpenWindows(req.Numbers); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, "窗口已打开")
}

func (s *Server) handleImportWindows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	windows, err := s.bridge.ImportWindows()
	if err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, windows)
}

func (s *Server) handleArrangeWindows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := s.bridge.ArrangeWindows(req.Mode); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, "窗口已排列")
}

func (s *Server) handleCloseWindows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 DELETE 或 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Numbers string `json:"numbers"` // 可选，不传则关闭所有已导入窗口
	}
	// 尝试解析 body，如果解析失败（如 body 为空）则忽略，直接关闭所有
	json.NewDecoder(r.Body).Decode(&req)

	if err := s.bridge.CloseWindows(req.Numbers); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, "窗口已关闭")
}

func (s *Server) handleZoomWindows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Level   int    `json:"level"`
		Numbers string `json:"numbers"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "无效的请求负载", http.StatusBadRequest)
		return
	}

	// 如果没有传 Level，可以给个智能默认值或者直接报错
	if req.Level == 0 {
		req.Level = 100 // 默认为恢复 100%
	}

	if err := s.bridge.ZoomWindows(req.Level, req.Numbers); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, fmt.Sprintf("窗口缩放已调整至 %d%%", req.Level))
}

func (s *Server) handleSelectAllWindows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Selected bool `json:"selected"`
	}
	// 默认全选
	req.Selected = true
	json.NewDecoder(r.Body).Decode(&req)

	if err := s.bridge.SelectAllWindows(req.Selected); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, "状态已更新")
}

// --- 同步控制 ---

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "仅支持 GET 方法", http.StatusMethodNotAllowed)
		return
	}
	status := s.bridge.GetSyncStatus()
	s.jsonOK(w, status)
}

func (s *Server) handleStartSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		MasterNumber int   `json:"masterNumber"`
		SlaveNumbers []int `json:"slaveNumbers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := s.bridge.StartSync(req.MasterNumber, req.SlaveNumbers); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, "同步已启动（插件弹窗将自动同步）")
}

func (s *Server) handleStopSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	if err := s.bridge.StopSync(); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, "同步已停止")
}

func (s *Server) handleSetMaster(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Number int `json:"number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := s.bridge.SetMasterWindow(req.Number); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, fmt.Sprintf("主控窗口已设置为 %d", req.Number))
}

// --- 标签管理 ---

func (s *Server) handleKeepCurrentTab(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	if err := s.bridge.KeepOnlyCurrentTab(); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, "已仅保留当前标签页")
}

func (s *Server) handleKeepNewTab(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	if err := s.bridge.KeepOnlyNewTab(); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, "已仅保留新标签页")
}

func (s *Server) handleNavigate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		URL     string `json:"url"`
		Numbers string `json:"numbers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := s.bridge.NavigateWindowsToURL(req.URL, req.Numbers); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, "所有窗口已导航至指定 URL")
}

// --- 批量输入 ---

func (s *Server) handleInputText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Lines     []string `json:"lines"`
		Overwrite bool     `json:"overwrite"`
		Delayed   bool     `json:"delayed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := s.bridge.InputTextToAllWindows(req.Lines, req.Overwrite, req.Delayed); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, fmt.Sprintf("已向所有窗口输入 %d 行文本", len(req.Lines)))
}

func (s *Server) handleInputRandom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Min           float64 `json:"min"`
		Max           float64 `json:"max"`
		IsFloat       bool    `json:"isFloat"`
		DecimalPlaces int     `json:"decimalPlaces"`
		Overwrite     bool    `json:"overwrite"`
		Delayed       bool    `json:"delayed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := s.bridge.InputRandomNumberToAllWindows(req.Min, req.Max, req.IsFloat, req.DecimalPlaces, req.Overwrite, req.Delayed); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, "随机数已输入至所有窗口")
}

// --- 系统级操控（同步场景核心）---
//
// 这些端点向主控窗口发送系统级的键盘/鼠标信号。
// 当同步开启时，ChromeManager 的键鼠 Hook 会自动将这些操作广播到所有从窗口，
// 包括浏览器插件弹出的弹窗窗口（由 popupCache 机制自动处理）。

func (s *Server) handleKeystroke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Keys string `json:"keys"` // 组合键，如 "ctrl+a"
		Text string `json:"text"` // 文本输入
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := s.bridge.SendKeystrokeToMaster(req.Keys, req.Text); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, "键盘信号已发送至主控窗口")
}

func (s *Server) handleClick(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		X      int    `json:"x"`
		Y      int    `json:"y"`
		Button string `json:"button"` // "left", "right", "middle"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if req.Button == "" {
		req.Button = "left"
	}
	if err := s.bridge.SendClickToMaster(req.X, req.Y, req.Button); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, "鼠标信号已发送至主控窗口")
}

func (s *Server) handleFocusMaster(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	if err := s.bridge.FocusMasterWindow(); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonOK(w, "主控窗口已聚焦")
}

// --- JSON 响应工具 ---

func (s *Server) jsonOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":   true,
		"data": data,
	})
}

func (s *Server) jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":    false,
		"error": message,
	})
}
