//go:build windows

package windows

import (
	"chromemanager/platform/common"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/go-toast/toast"
)

// getAppID 获取应用ID，使用exe的完整路径确保唯一性
func getAppID() string {
	exePath, err := os.Executable()
	if err != nil {
		return "ChromeManager.DesktopApp"
	}
	// 使用exe的绝对路径作为AppID，这样不需要注册
	return exePath
}

// getNotificationIcon 获取通知图标路径
// 优先使用 icons/appicon.png，如果不存在则使用exe
func getNotificationIcon() string {
	exePath, err := os.Executable()
	if err != nil {
		return ""
	}
	exeDir := filepath.Dir(exePath)

	// 优先使用提取的 appicon.png
	appIconPath := filepath.Join(exeDir, "icons", "appicon.png")
	if _, err := os.Stat(appIconPath); err == nil {
		absPath, _ := filepath.Abs(appIconPath)
		return absPath
	}

	// 如果 appicon.png 不存在，使用 exe 文件
	absExePath, _ := filepath.Abs(exePath)
	return absExePath
}

// ShowNotification 显示系统通知
// 从 utils.NotificationManager 迁移完成 ✅
func (p *Provider) ShowNotification(options common.NotificationOptions) error {
	// 获取通知图标路径
	iconPath := getNotificationIcon()

	// 获取exe路径用于ActivationArguments
	exePath, _ := os.Executable()

	// 构建 toast 通知
	notification := toast.Notification{
		AppID:   getAppID(), // 使用exe路径作为AppID
		Title:   options.Title,
		Message: options.Message,
		Icon:    iconPath, // 使用提取的appicon.png或exe
	}

	// 如果用户提供了自定义图标路径，优先使用它
	if options.IconPath != "" {
		notification.Icon = options.IconPath
	}

	// 设置音频
	if options.Silent {
		notification.Audio = toast.Silent
	} else {
		notification.Audio = toast.Default
	}

	// 设置持续时间
	if options.Duration > 0 {
		// toast 库只支持 Short 和 Long
		// 短通知：5秒，长通知：25秒
		// 我们简单地根据时长选择
		if options.Duration.Seconds() <= 10 {
			notification.Duration = toast.Short
		} else {
			notification.Duration = toast.Long
		}
	} else {
		notification.Duration = toast.Short
	}

	// 添加激活器路径（可选，帮助Windows识别应用）
	notification.ActivationArguments = exePath

	// 推送通知
	err := notification.Push()
	if err != nil {
		log.Printf("发送通知失败: %v", err)
		return fmt.Errorf("failed to push notification: %w", err)
	}

	return nil
}

// CloseNotification 关闭通知
// Windows Toast 通知不支持程序化关闭，这是一个空实现
func (p *Provider) CloseNotification(notificationID string) error {
	// Windows Toast Notification API 不支持程序化关闭通知
	// 用户需要手动关闭或等待自动消失
	log.Printf("⚠️ Windows Toast 通知不支持程序化关闭: %s", notificationID)
	return nil
}

// IsSupported 检查当前平台是否支持通知
// Windows 10+ 支持 Toast 通知
func (p *Provider) IsSupported() bool {
	// Windows 10 及以上版本都支持 Toast 通知
	// 这里简化处理，假设都支持
	return true
}
