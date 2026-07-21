package syncmanager

import (
	"context"
	"time"

	"chromemanager/config"
)

type SyncStatus string

const (
	SyncStatusIdle    SyncStatus = "idle"
	SyncStatusRunning SyncStatus = "running"
	SyncStatusError   SyncStatus = "error"
	SyncStatusStopped SyncStatus = "stopped"
)

type EventType int

const (
	// 鼠标事件
	MouseMove EventType = iota
	MouseLeftDown
	MouseLeftUp
	MouseRightDown
	MouseRightUp
	MouseMiddleDown
	MouseMiddleUp
	MouseWheel

	// 键盘事件
	KeyDown
	KeyUp
	KeyChar

	// 特殊事件
	WindowActivate
	WindowDeactivate
	PopupShow
	PopupHide
)

type SyncEvent struct {
	Type        EventType `json:"type"`
	X           int       `json:"x"`
	Y           int       `json:"y"`
	KeyCode     uint32    `json:"keyCode"`
	KeyChar     rune      `json:"keyChar"`
	WheelDelta  int       `json:"wheelDelta"`
	Modifiers   uint32    `json:"modifiers"`
	SourceHWND  uintptr   `json:"sourceHwnd"`
	Timestamp   time.Time `json:"timestamp"`
	IsCtrlDown  bool      `json:"isCtrlDown"`
	IsShiftDown bool      `json:"isShiftDown"`
	IsAltDown   bool      `json:"isAltDown"`
}

type SyncCommandType string

const (
	// 导航命令
	CommandNavigate SyncCommandType = "navigate"

	// 点击命令
	CommandClick SyncCommandType = "click"

	// 输入文本命令
	CommandInputText SyncCommandType = "input_text"

	// 滚动命令
	CommandScroll SyncCommandType = "scroll"

	// 标签页管理命令
	CommandTabSwitch SyncCommandType = "tab_switch"
	CommandTabNew    SyncCommandType = "tab_new"
	CommandTabClose  SyncCommandType = "tab_close"

	// 窗口操作命令
	CommandWindowActivate SyncCommandType = "window_activate"
	CommandWindowSetPos   SyncCommandType = "window_set_pos"
	CommandWindowSetTitle SyncCommandType = "window_set_title"

	// 表单提交命令
	CommandFormSubmit SyncCommandType = "form_submit"

	CommandExecuteJS SyncCommandType = "execute_js"

	// 键盘按键命令
	CommandKeyPress SyncCommandType = "key_press"

	// 弹窗处理命令
	CommandHandleDialog SyncCommandType = "handle_dialog"
)

type SyncCommand struct {
	Type       SyncCommandType        `json:"type"`
	Data       map[string]interface{} `json:"data"`
	SourceHWND uintptr                `json:"sourceHwnd,omitempty"` // 来源窗口句柄，用于过滤
	TargetHWND uintptr                `json:"targetHwnd,omitempty"` // 目标窗口句柄
	Timestamp  int64                  `json:"timestamp,omitempty"`  // 命令时间戳
}

type Rect struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Right  int `json:"right"`
	Bottom int `json:"bottom"`
}

func (r Rect) Width() int {
	return r.Right - r.Left
}

func (r Rect) Height() int {
	return r.Bottom - r.Top
}

type WindowInfo struct {
	HWND        uintptr `json:"hwnd"`
	Title       string  `json:"title"`
	Number      int     `json:"number"`
	PID         int32   `json:"pid"`
	UserDataDir string  `json:"userDataDir,omitempty"`
	DebugPort   int     `json:"debugPort,omitempty"`
	CommandLine string  `json:"commandLine,omitempty"`
	IsMaster    bool    `json:"isMaster,omitempty"`
	IsRunning   bool    `json:"isRunning,omitempty"`

	// 窗口位置和尺寸信息
	ClientRect Rect `json:"clientRect"`
	WindowRect Rect `json:"windowRect"`

	// 弹出窗口列表
	PopupWindows []uintptr `json:"popupWindows"`

	// 同步状态
	LastSyncTime time.Time `json:"lastSyncTime"`
	SyncErrors   int       `json:"syncErrors"`
}

func DefaultSyncConfig() config.SyncConfig {
	return config.GetDefaultSettings().SyncConfig
}

type PerformanceMetrics struct {
	EventsProcessed int64         `json:"eventsProcessed"`
	EventsDropped   int64         `json:"eventsDropped"`
	AverageLatency  time.Duration `json:"averageLatency"`
	MaxLatency      time.Duration `json:"maxLatency"`
	MinLatency      time.Duration `json:"minLatency"`
	EventsPerSecond float64       `json:"eventsPerSecond"`
	MemoryUsage     int64         `json:"memoryUsage"`
	GoroutineCount  int           `json:"goroutineCount"`
	LastUpdateTime  time.Time     `json:"lastUpdateTime"`
}

type PopupMapping struct {
	MasterPopup uintptr   `json:"masterPopup"`
	SlavePopup  uintptr   `json:"slavePopup"`
	Similarity  float64   `json:"similarity"`
	LastUsed    time.Time `json:"lastUsed"`
}

type WindowMatcher struct {
	popupMappings map[uintptr][]PopupMapping
}

func NewWindowMatcher() *WindowMatcher {
	return &WindowMatcher{
		popupMappings: make(map[uintptr][]PopupMapping),
	}
}

type SyncState struct {
	Status           SyncStatus   `json:"status"`
	MasterWindowInfo *WindowInfo  `json:"masterWindow"`
	SlaveWindows     []WindowInfo `json:"slaveWindows"`
	TotalAgents      int          `json:"totalAgents"`
	ActiveAgents     int          `json:"activeAgents"`
	TotalCommands    int64        `json:"totalCommands"`
	LastError        string       `json:"lastError"`
	StartTime        time.Time    `json:"startTime"`

	// 性能统计
	EventsPerSecond float64       `json:"eventsPerSecond"`
	AverageLatency  time.Duration `json:"averageLatency"`

	// 同步配置
	Config config.SyncConfig `json:"config"`
}

type ChromeAgentConfig struct {
	HWND           uintptr                 `json:"hwnd"`
	PID            int32                   `json:"pid"`
	Number         int                     `json:"number"`
	DebugPort      int                     `json:"debugPort"`
	UserDataDir    string                  `json:"userDataDir"`
	ChromePath     string                  `json:"chromePath"`
	IsMaster       bool                    `json:"isMaster"`
	SettingsGetter func() *config.Settings `json:"-"` // 获取最新配置的回调函数
}

type ChromeAgentStatus string

const (
	AgentStatusStopped  ChromeAgentStatus = "stopped"
	AgentStatusStarting ChromeAgentStatus = "starting"
	AgentStatusRunning  ChromeAgentStatus = "running"
	AgentStatusStopping ChromeAgentStatus = "stopping"
	AgentStatusError    ChromeAgentStatus = "error"
)

type ChromeAgentState struct {
	Config       ChromeAgentConfig `json:"config"`
	Status       ChromeAgentStatus `json:"status"`
	LastError    string            `json:"lastError,omitempty"`
	LastCommand  *SyncCommand      `json:"lastCommand,omitempty"`
	CommandCount int64             `json:"commandCount"`
	StartTime    int64             `json:"startTime,omitempty"`
}

type SyncManagerStatus string

const (
	SyncManagerStatusStopped  SyncManagerStatus = "stopped"
	SyncManagerStatusStarting SyncManagerStatus = "starting"
	SyncManagerStatusRunning  SyncManagerStatus = "running"
	SyncManagerStatusStopping SyncManagerStatus = "stopping"
	SyncManagerStatusError    SyncManagerStatus = "error"
)

type SyncManagerState struct {
	Status           SyncManagerStatus         `json:"status"`
	MasterWindowInfo *WindowInfo               `json:"masterWindowInfo,omitempty"`
	AgentStates      map[int]*ChromeAgentState `json:"agentStates"`
	TotalAgents      int                       `json:"totalAgents"`
	ActiveAgents     int                       `json:"activeAgents"`
	TotalCommands    int64                     `json:"totalCommands"`
	LastError        string                    `json:"lastError,omitempty"`
	StartTime        int64                     `json:"startTime,omitempty"`
}

type Event struct {
	Type      EventType              `json:"type"`
	Data      map[string]interface{} `json:"data"`
	Timestamp int64                  `json:"timestamp"`
	Source    string                 `json:"source,omitempty"`
}

type SettingsGetter interface {
	GetSettings() *config.Settings
}

type CommandChannel chan SyncCommand

type EventChannel chan Event

type ContextWithCancel struct {
	Ctx    context.Context
	Cancel context.CancelFunc
}

type ChromeInstance struct {
	Config    ChromeAgentConfig
	Agent     *ChromeAgent
	Context   ContextWithCancel
	StartTime int64
}

type ChromeAgent struct {
}

type ScreenInfo struct {
	Index      int    `json:"index"`
	Name       string `json:"name"`
	Primary    bool   `json:"primary"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	WorkX      int    `json:"workX"`
	WorkY      int    `json:"workY"`
	WorkWidth  int    `json:"workWidth"`
	WorkHeight int    `json:"workHeight"`
}

type ArrangeConfig struct {
	StartX        int `json:"startX"`
	StartY        int `json:"startY"`
	WindowWidth   int `json:"windowWidth"`
	WindowHeight  int `json:"windowHeight"`
	HorizontalGap int `json:"horizontalGap"`
	VerticalGap   int `json:"verticalGap"`
	PerRow        int `json:"perRow"`
	ScreenIndex   int `json:"screenIndex"`
}
