package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"chromemanager/platform/common"
	"chromemanager/utils"

	"golang.org/x/net/websocket"
)

type APIServer struct {
	srv     *http.Server
	service *ChromeService
	ctx     context.Context // wails context for emitting events
	mu      sync.Mutex
}

func NewAPIServer(service *ChromeService) *APIServer {
	return &APIServer{
		service: service,
	}
}

// Start begins the HTTP server if EnableAPI is true
func (a *APIServer) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ctx = ctx

	settings, err := a.service.loadSettings()
	if err != nil {
		return err
	}

	if !settings.EnableAPI {
		return nil
	}
	if settings.APIPort == 0 {
		settings.APIPort = 18923
	}
	if settings.APIToken == "" {
		settings.APIToken = generateAPIToken()
		if err := a.service.saveSettings(settings); err != nil {
			return err
		}
	}

	if a.srv != nil {
		return nil // Already running
	}

	mux := http.NewServeMux()

	// Auth Middleware
	authMiddleware := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token := settings.APIToken
			authHeader := r.Header.Get("Authorization")
			expected := "Bearer " + token
			if authHeader != expected || token == "" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		}
	}

	a.registerRoutes(mux, authMiddleware)

	addr := fmt.Sprintf("127.0.0.1:%d", settings.APIPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("API 服务器启动失败 (端口 %d): %v", settings.APIPort, err)
	}

	a.srv = &http.Server{
		Handler: mux,
	}

	go func() {
		fmt.Printf("API Server listening on %s\n", addr)
		if err := a.srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Printf("API Server error: %v\n", err)
		}
	}()

	return nil
}

func (a *APIServer) registerRoutes(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/v1/windows", auth(a.handleGetWindows))
	mux.HandleFunc("/api/v1/windows/import", auth(a.handleImportWindows))
	mux.HandleFunc("/api/v1/windows/open", auth(a.handleOpenWindows))
	mux.HandleFunc("/api/v1/windows/arrange", auth(a.handleArrangeWindows))
	mux.HandleFunc("/api/v1/windows/close", auth(a.handleCloseWindows))
	mux.HandleFunc("/api/v1/windows/zoom", auth(a.handleZoomWindows))
	mux.HandleFunc("/api/v1/windows/select-all", auth(a.handleSelectAll))
	mux.HandleFunc("/api/v1/windows/navigate", auth(a.handleNavigateWindows))
	mux.HandleFunc("/api/v1/sync/status", auth(a.handleSyncStatus))
	mux.HandleFunc("/api/v1/sync/start", auth(a.handleStartSync))
	mux.HandleFunc("/api/v1/sync/stop", auth(a.handleStopSync))
	mux.HandleFunc("/api/v1/sync/master", auth(a.handleSetMaster))
	mux.HandleFunc("/api/v1/tabs/keep-current", auth(a.handleKeepCurrentTab))
	mux.HandleFunc("/api/v1/tabs/keep-new", auth(a.handleKeepNewTab))
	mux.HandleFunc("/api/v1/tabs/navigate", auth(a.handleNavigateWindows))
	mux.HandleFunc("/api/v1/input/text", auth(a.handleInputText))
	mux.HandleFunc("/api/v1/input/random", auth(a.handleInputRandom))
	mux.HandleFunc("/api/v1/input/keystroke", auth(a.handleKeystroke))
	mux.HandleFunc("/api/v1/input/click", auth(a.handleClick))
	mux.HandleFunc("/api/v1/focus/master", auth(a.handleFocusMaster))
	mux.HandleFunc("/api/v1/input/focus/master", auth(a.handleFocusMaster))
}

// Stop halts the HTTP server gracefully
func (a *APIServer) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := a.srv.Shutdown(ctx)
		a.srv = nil
		return err
	}
	return nil
}

// Restart restarts the HTTP server (e.g., when settings change)
func (a *APIServer) Restart(ctx context.Context) error {
	a.Stop()
	return a.Start(ctx)
}

// API Handlers
type APIWindowResponse struct {
	Number      int     `json:"number"`
	HWND        uintptr `json:"hwnd,omitempty"`
	PID         int32   `json:"pid"`
	Title       string  `json:"title"`
	UserDataDir string  `json:"userDataDir"`
	DebugPort   int     `json:"debugPort"`
	CDPUrl      string  `json:"cdpUrl"`
	IsRunning   bool    `json:"isRunning"`
}

func (a *APIServer) handleGetWindows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	windows, err := a.service.ImportWindows()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var res []APIWindowResponse
	for _, win := range windows {
		cdpUrl := ""
		if win.DebugPort > 0 {
			cdpUrl = fmt.Sprintf("http://127.0.0.1:%d", win.DebugPort)
		}
		res = append(res, APIWindowResponse{
			Number:      win.Number,
			HWND:        win.HWND,
			PID:         win.PID,
			Title:       win.Title,
			UserDataDir: win.UserDataDir,
			DebugPort:   win.DebugPort,
			CDPUrl:      cdpUrl,
			IsRunning:   win.IsRunning,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"ok":      true,
		"data":    res,
	})
}

func (a *APIServer) handleImportWindows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	windows, err := a.service.ImportWindows()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 触发前端重新执行导入和渲染
	if a.service.app != nil {
		a.service.app.Event.Emit("openclaw:import_windows")
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": "Windows imported",
		"data":    windows,
	})
}

func (a *APIServer) handleOpenWindows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	type Payload struct {
		Target  string `json:"target"`
		Numbers string `json:"numbers"`
	}
	var p Payload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "Invalid Payload", http.StatusBadRequest)
		return
	}

	target := firstNonEmpty(p.Numbers, p.Target)
	nums, err := a.parseTargetNumbers(target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Because OpenWindows accepts a string, we can just pass the payload Target,
	// BUT because our Target might be "all" or "selected", we should map it back to a string
	// of comma-separated numbers for the existing function.
	var targetStr string
	if target == "selected" || target == "all" || target == "" {
		strList := make([]string, len(nums))
		for i, n := range nums {
			strList[i] = strconv.Itoa(n)
		}
		targetStr = strings.Join(strList, ",")
	} else {
		targetStr = target
	}

	err = a.service.OpenWindows(targetStr)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": "Triggered open windows",
	})
}

func (a *APIServer) handleArrangeWindows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	type Payload struct {
		Target  string `json:"target"`
		Numbers string `json:"numbers"`
	}
	var p Payload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "Invalid Payload", http.StatusBadRequest)
		return
	}

	nums, err := a.parseTargetNumbers(firstNonEmpty(p.Numbers, p.Target))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 执行自动排列
	if err := a.service.AutoArrangeWindows(nums); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": "Auto arrange executed",
	})
}

func (a *APIServer) handleCloseWindows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	type Payload struct {
		Target  string `json:"target"`
		Numbers string `json:"numbers"`
	}
	var p Payload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "Invalid Payload", http.StatusBadRequest)
		return
	}

	nums, err := a.parseTargetNumbers(firstNonEmpty(p.Numbers, p.Target))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := a.service.CloseWindows(nums); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 触发前端更新列表
	if a.service.app != nil {
		a.service.app.Event.Emit("openclaw:import_windows")
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": fmt.Sprintf("Closing %d windows", len(nums)),
	})
}

func (a *APIServer) handleZoomWindows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	type Payload struct {
		Target  string `json:"target"`
		Numbers string `json:"numbers"`
		Level   int    `json:"level"` // -1/1 step, 0 reset, or 25/50/75/100 preset percent
	}
	var p Payload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "Invalid Payload", http.StatusBadRequest)
		return
	}

	nums, err := a.parseTargetNumbers(firstNonEmpty(p.Numbers, p.Target))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	windows := a.service.GetAPIWindows()
	portMap := make(map[int]int)
	for _, w := range windows {
		portMap[w.Number] = w.DebugPort
	}
	for _, num := range nums {
		if portMap[num] <= 0 {
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("window %d has no active debug port", num))
			return
		}
	}

	results := make(map[int]map[string]interface{})
	for _, num := range nums {
		result, err := a.cdpZoom(portMap[num], p.Level)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("window %d zoom failed: %v", num, err))
			return
		}
		results[num] = result
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": "Chrome native zoom updated for selected windows",
		"target":  nums,
		"data":    results,
	})
}

func (a *APIServer) handleSelectAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	type Payload struct {
		Target   string `json:"target"`   // Usually "all"
		Select   *bool  `json:"select"`   // true to select, false to deselect
		Selected *bool  `json:"selected"` // Windows-compatible alias
	}
	var p Payload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil && err != io.EOF {
		http.Error(w, "Invalid Payload", http.StatusBadRequest)
		return
	}
	selected := true
	if p.Select != nil {
		selected = *p.Select
	}
	if p.Selected != nil {
		selected = *p.Selected
	}
	selectedNumbers := []int(nil)
	if selected {
		windows := a.service.GetAllImportedWindows()
		if len(windows) == 0 {
			if _, err := a.service.ImportWindows(); err != nil {
				writeAPIError(w, http.StatusInternalServerError, err.Error())
				return
			}
			windows = a.service.GetAllImportedWindows()
		}
		for _, window := range windows {
			selectedNumbers = append(selectedNumbers, window.Number)
		}
		sortInts(selectedNumbers)
	}
	a.service.UpdateSelectedWindows(selectedNumbers)

	if a.service.app != nil {
		a.service.app.Event.Emit("openclaw:select_all", map[string]interface{}{"select": selected})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": "Selection state updated",
		"data": map[string]interface{}{
			"selected": selected,
			"numbers":  selectedNumbers,
		},
	})
}

func (a *APIServer) handleNavigateWindows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	type Payload struct {
		Target  string `json:"target"`
		Numbers string `json:"numbers"`
		URL     string `json:"url"`
	}
	var p Payload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "Invalid Payload", http.StatusBadRequest)
		return
	}

	if p.URL == "" {
		http.Error(w, "URL is required", http.StatusBadRequest)
		return
	}

	nums, err := a.parseTargetNumbers(firstNonEmpty(p.Numbers, p.Target))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	windows := a.service.GetAPIWindows()
	portMap := make(map[int]int)
	for _, win := range windows {
		portMap[win.Number] = win.DebugPort
	}
	for _, num := range nums {
		if portMap[num] <= 0 {
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("window %d has no active debug port", num))
			return
		}
	}

	for _, num := range nums {
		if err := a.cdpNavigate(portMap[num], p.URL); err != nil {
			writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("window %d navigation failed: %v", num, err))
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": fmt.Sprintf("Navigated %d windows", len(nums)),
	})
}

func (a *APIServer) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	status, err := a.service.GetSyncStatus()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAPIJSON(w, map[string]interface{}{
		"success": true,
		"ok":      true,
		"data":    status,
	})
}

func (a *APIServer) handleStartSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		MasterNumber int    `json:"masterNumber"`
		SlaveNumbers []int  `json:"slaveNumbers"`
		Target       string `json:"target"`
		Numbers      string `json:"numbers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid Payload", http.StatusBadRequest)
		return
	}
	if (req.MasterNumber <= 0 || len(req.SlaveNumbers) == 0) && firstNonEmpty(req.Numbers, req.Target) != "" {
		nums, err := a.parseTargetNumbers(firstNonEmpty(req.Numbers, req.Target))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.MasterNumber <= 0 && len(nums) > 0 {
			req.MasterNumber = nums[0]
			if len(nums) > 1 {
				req.SlaveNumbers = nums[1:]
			}
		} else if req.MasterNumber > 0 && len(req.SlaveNumbers) == 0 {
			for _, n := range nums {
				if n != req.MasterNumber {
					req.SlaveNumbers = append(req.SlaveNumbers, n)
				}
			}
		}
	}
	if err := a.service.StartSync(req.MasterNumber, req.SlaveNumbers); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAPIJSON(w, map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": "Sync start dispatched",
	})
}

func (a *APIServer) handleStopSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.service.StopSync(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAPIJSON(w, map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": "Sync stop dispatched",
	})
}

func (a *APIServer) handleSetMaster(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Number int `json:"number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid Payload", http.StatusBadRequest)
		return
	}
	if err := a.service.SetMasterWindow(req.Number); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAPIJSON(w, map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": fmt.Sprintf("Master window set to %d", req.Number),
	})
}

func (a *APIServer) handleKeepCurrentTab(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	nums, err := a.parseOptionalNumbers(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.service.KeepOnlyCurrentTab(nums); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAPIJSON(w, map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": "Kept current tab",
	})
}

func (a *APIServer) handleKeepNewTab(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	nums, err := a.parseOptionalNumbers(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.service.KeepOnlyNewTab(nums); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAPIJSON(w, map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": "Kept new tab",
	})
}

func (a *APIServer) handleInputText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Target      string   `json:"target"`
		Numbers     string   `json:"numbers"`
		Lines       []string `json:"lines"`
		Text        string   `json:"text"`
		InputMethod string   `json:"inputMethod"`
		Overwrite   bool     `json:"overwrite"`
		Delayed     bool     `json:"delayed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid Payload", http.StatusBadRequest)
		return
	}
	if len(req.Lines) == 0 && req.Text != "" {
		req.Lines = []string{req.Text}
	}
	if req.InputMethod == "" {
		req.InputMethod = "sequence"
	}
	nums, err := a.parseTargetNumbers(firstNonEmpty(req.Numbers, req.Target))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.service.InputTextFromLines(nums, req.Lines, req.InputMethod, req.Overwrite, req.Delayed); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAPIJSON(w, map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": fmt.Sprintf("Input %d text lines", len(req.Lines)),
	})
}

func (a *APIServer) handleInputRandom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Target        string  `json:"target"`
		Numbers       string  `json:"numbers"`
		Min           float64 `json:"min"`
		Max           float64 `json:"max"`
		IsFloat       bool    `json:"isFloat"`
		DecimalPlaces int     `json:"decimalPlaces"`
		Overwrite     bool    `json:"overwrite"`
		Delayed       bool    `json:"delayed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid Payload", http.StatusBadRequest)
		return
	}
	nums, err := a.parseTargetNumbers(firstNonEmpty(req.Numbers, req.Target))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg := utils.RandomInputConfig{
		MinValue:      req.Min,
		MaxValue:      req.Max,
		IsFloat:       req.IsFloat,
		DecimalPlaces: req.DecimalPlaces,
		Overwrite:     req.Overwrite,
		Delayed:       req.Delayed,
	}
	if err := a.service.InputRandomNumbers(nums, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAPIJSON(w, map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": "Random input dispatched",
	})
}

func (a *APIServer) handleKeystroke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Keys string `json:"keys"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid Payload", http.StatusBadRequest)
		return
	}
	pid, _, err := a.masterPIDAndRect()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.sendCapturedKeystroke(pid, req.Keys, req.Text); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAPIJSON(w, map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": "Keystroke dispatched to master",
	})
}

func (a *APIServer) handleClick(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		X      int    `json:"x"`
		Y      int    `json:"y"`
		Button string `json:"button"`
		Screen bool   `json:"screen"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid Payload", http.StatusBadRequest)
		return
	}
	pid, rect, err := a.masterPIDAndRect()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	screenX, screenY := req.X, req.Y
	if !req.Screen {
		screenX = rect.Left + req.X
		screenY = rect.Top + req.Y
	}
	if err := a.sendCapturedClick(pid, screenX, screenY, req.Button); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAPIJSON(w, map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": "Click dispatched to master",
	})
}

func (a *APIServer) handleFocusMaster(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	pid, _, err := a.masterPIDAndRect()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.service.provider.SetForegroundWindow(common.WindowHandle(pid)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAPIJSON(w, map[string]interface{}{
		"success": true,
		"ok":      true,
		"message": "Master focused",
	})
}

// parseTargetNumbers parses robust targets: "", "all", "selected", "1,3,5"
func (a *APIServer) parseTargetNumbers(target string) ([]int, error) {
	target = strings.TrimSpace(target)
	if target == "" || target == "all" {
		// Return all managed windows.
		windows := a.service.GetAllImportedWindows()
		if len(windows) == 0 {
			if imported, err := a.service.ImportWindows(); err == nil {
				for _, w := range imported {
					windows = append(windows, utils.WindowInfo{
						Number:    w.Number,
						Title:     w.Title,
						HWND:      w.HWND,
						PID:       w.PID,
						DebugPort: w.DebugPort,
						IsRunning: w.IsRunning,
						IsMaster:  w.IsMaster,
					})
				}
			}
		}
		var results []int
		for _, w := range windows {
			results = append(results, w.Number)
		}
		if len(results) == 0 {
			return nil, fmt.Errorf("没有可用窗口，请先打开并导入窗口")
		}
		sortInts(results)
		return results, nil
	} else if target == "selected" {
		// Return internally maintained selected IDs
		a.service.selectedWindowsMux.RLock()
		defer a.service.selectedWindowsMux.RUnlock()
		var results []int
		results = append(results, a.service.selectedWindows...)
		if len(results) == 0 {
			return nil, fmt.Errorf("没有选中的窗口")
		}
		sortInts(results)
		return results, nil
	}

	var results []int
	parts := strings.Split(target, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(p, "-") {
			rangeParts := strings.SplitN(p, "-", 2)
			start, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid window number: %s", rangeParts[0])
			}
			end, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid window number: %s", rangeParts[1])
			}
			if end < start {
				return nil, fmt.Errorf("invalid window range: %s", p)
			}
			for i := start; i <= end; i++ {
				results = append(results, i)
			}
		} else {
			num, err := strconv.Atoi(p)
			if err != nil {
				return nil, fmt.Errorf("invalid window number: %s", p)
			}
			results = append(results, num)
		}
	}
	sortInts(results)
	return results, nil
}

func (a *APIServer) parseOptionalNumbers(r *http.Request) ([]int, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(body)) == "" {
		return a.parseTargetNumbers("all")
	}
	var req struct {
		Target  string `json:"target"`
		Numbers string `json:"numbers"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	return a.parseTargetNumbers(firstNonEmpty(req.Numbers, req.Target))
}

func (a *APIServer) masterPIDAndRect() (int, common.Rect, error) {
	if a.service.syncManager == nil {
		return 0, common.Rect{}, fmt.Errorf("sync manager not initialized")
	}
	master := a.service.syncManager.GetMasterWindow()
	if master == nil {
		return 0, common.Rect{}, fmt.Errorf("未设置主控窗口")
	}
	pid := int(master.PID)
	if pid <= 0 {
		pid = int(master.HWND)
	}
	if pid <= 0 {
		return 0, common.Rect{}, fmt.Errorf("主控窗口 PID 无效")
	}
	rect, err := a.service.provider.GetWindowRect(common.WindowHandle(pid))
	if err != nil {
		return pid, common.Rect{}, err
	}
	return pid, *rect, nil
}

func (a *APIServer) sendCapturedKeystroke(pid int, keys string, text string) error {
	type capturedKeyboard interface {
		SendCapturedKeystrokeToPID(pid int, keys string) error
		SendCapturedTextToPID(pid int, text string) error
	}
	if sender, ok := a.service.provider.(capturedKeyboard); ok {
		if strings.TrimSpace(keys) != "" {
			if err := sender.SendCapturedKeystrokeToPID(pid, keys); err != nil {
				return err
			}
			time.Sleep(50 * time.Millisecond)
		}
		if text != "" {
			return sender.SendCapturedTextToPID(pid, text)
		}
		return nil
	}

	if err := a.service.provider.SetForegroundWindow(common.WindowHandle(pid)); err != nil {
		return err
	}
	if strings.TrimSpace(keys) != "" {
		return fmt.Errorf("captured keystroke is not supported by this provider")
	}
	if text != "" {
		return a.service.provider.SendTextInput(text)
	}
	return nil
}

func (a *APIServer) sendCapturedClick(pid int, x int, y int, button string) error {
	type capturedMouse interface {
		SendCapturedMouseClickToPID(pid int, x int, y int, button string) error
	}
	if sender, ok := a.service.provider.(capturedMouse); ok {
		return sender.SendCapturedMouseClickToPID(pid, x, y, button)
	}

	if err := a.service.provider.SetForegroundWindow(common.WindowHandle(pid)); err != nil {
		return err
	}
	mouseButton := common.MouseLeft
	if strings.EqualFold(button, "right") {
		mouseButton = common.MouseRight
	}
	return a.service.provider.SendMouseClick(x, y, mouseButton)
}

func generateAPIToken() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("chromemanager-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func writeAPIJSON(w http.ResponseWriter, payload map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": false,
		"ok":      false,
		"message": message,
	})
}

func sortInts(values []int) {
	for i := 0; i < len(values)-1; i++ {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}

func (a *APIServer) cdpZoom(port int, level int) (map[string]interface{}, error) {
	type nativeZoomProvider interface {
		SetChromeNativeZoomOnPort(port int, level int) (map[string]interface{}, error)
	}
	if provider, ok := a.service.provider.(nativeZoomProvider); ok {
		return provider.SetChromeNativeZoomOnPort(port, level)
	}
	return nil, fmt.Errorf("native zoom is not supported by this provider")
}

type cdpTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	WebSocketDebuggerUrl string `json:"webSocketDebuggerUrl"`
	URL                  string `json:"url"`
}

func (a *APIServer) cdpNavigate(port int, targetURL string) error {
	// If sync is running, reuse existing CDP connections to avoid conflicts
	if a.service.syncManager != nil && a.service.syncManager.IsRunning() {
		// Use JS-based navigation through existing session
		script := fmt.Sprintf("window.location.href = %q;", targetURL)
		return a.service.syncManager.EvaluateJSOnPort(port, script)
	}

	// Fallback: sync is not running, safe to create a temporary direct connection
	return a.cdpNavigateDirect(port, targetURL)
}

// cdpNavigateDirect creates a temporary page-level WebSocket connection for navigation.
// Only safe to use when sync is NOT running.
func (a *APIServer) cdpNavigateDirect(port int, targetURL string) error {
	listURL := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(listURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var targets []cdpTarget
	if err := json.Unmarshal(body, &targets); err != nil {
		return err
	}

	var pageTargets []cdpTarget
	for _, t := range targets {
		if t.Type == "page" && t.WebSocketDebuggerUrl != "" && !strings.HasPrefix(t.URL, "devtools://") {
			pageTargets = append(pageTargets, t)
		}
	}
	if len(pageTargets) == 0 {
		return fmt.Errorf("no valid page target found for port %d", port)
	}

	selected := pageTargets[0]
	for _, target := range pageTargets {
		visible, err := cdpPageTargetVisible(target.WebSocketDebuggerUrl)
		if err == nil && visible {
			selected = target
			break
		}
	}

	result, err := sendDirectCDPCommand(selected.WebSocketDebuggerUrl, "Page.navigate", map[string]interface{}{
		"url": targetURL,
	})
	if err != nil {
		return err
	}

	var navigationResult struct {
		ErrorText string `json:"errorText"`
	}
	if err := json.Unmarshal(result, &navigationResult); err != nil {
		return fmt.Errorf("invalid Page.navigate response: %w", err)
	}
	if navigationResult.ErrorText != "" {
		return fmt.Errorf("Page.navigate failed: %s", navigationResult.ErrorText)
	}
	return nil
}

func cdpPageTargetVisible(wsURL string) (bool, error) {
	result, err := sendDirectCDPCommand(wsURL, "Runtime.evaluate", map[string]interface{}{
		"expression":    "document.visibilityState === 'visible'",
		"returnByValue": true,
	})
	if err != nil {
		return false, err
	}

	var evaluated struct {
		Result struct {
			Value bool `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &evaluated); err != nil {
		return false, err
	}
	return evaluated.Result.Value, nil
}

func sendDirectCDPCommand(wsURL, method string, params map[string]interface{}) (json.RawMessage, error) {
	ws, err := websocket.Dial(wsURL, "", "http://localhost/")
	if err != nil {
		return nil, err
	}
	defer ws.Close()
	if err := ws.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return nil, err
	}

	request := map[string]interface{}{
		"id":     1,
		"method": method,
		"params": params,
	}
	if err := websocket.JSON.Send(ws, request); err != nil {
		return nil, err
	}

	for {
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := websocket.JSON.Receive(ws, &response); err != nil {
			return nil, err
		}
		if response.ID != 1 {
			continue
		}
		if response.Error != nil {
			return nil, fmt.Errorf("%s failed (%d): %s", method, response.Error.Code, response.Error.Message)
		}
		return response.Result, nil
	}
}
