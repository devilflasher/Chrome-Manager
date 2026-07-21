package utils

import (
	"bufio"
	"fmt"
	"os"
)

type InputManager struct{}

func NewInputManager() *InputManager {
	return &InputManager{}
}

type RandomInputConfig struct {
	IsFloat       bool
	MinValue      float64
	MaxValue      float64
	DecimalPlaces int
	Overwrite     bool
	Delayed       bool
	InputMethod   string
}

type TextInputConfig struct {
	FilePath    string
	Enter       bool
	InputMethod string
}

func (im *InputManager) ValidateRandomConfig(config RandomInputConfig) error {
	return nil
}

func (im *InputManager) ValidateTextConfig(config TextInputConfig) error {
	return nil
}

func (im *InputManager) GetFilePreview(filePath string) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	// 读取前100行作为预览
	for i := 0; i < 100 && scanner.Scan(); i++ {
		lines = append(lines, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return lines, err
	}

	return lines, nil
}

func (im *InputManager) ForceReset() {}
func (im *InputManager) IsActive() bool { return false }

// InputRandomNumbers Mac Stub
func (im *InputManager) InputRandomNumbers(windowHWNDs []uintptr, config RandomInputConfig) error {
	return fmt.Errorf("macOS 暂不支持批量输入随机数功能 (请等待后续更新)")
}

// InputTextFromFile Mac Stub
func (im *InputManager) InputTextFromFile(windowHWNDs []uintptr, config TextInputConfig) error {
	// macOS 下不再通过 InputManager 处理，而是在 main.go 中通过 CDP 处理
	return fmt.Errorf("macOS 请直接调用 InputTextFromFile 的 CDP 实现")
}

// InputTextFromLines Mac Stub
func (im *InputManager) InputTextFromLines(windowHWNDs []uintptr, lines []string, inputMethod string, overwrite, delayed bool) error {
	return fmt.Errorf("macOS 请直接调用 InputTextFromLines 的 CDP 实现")
}
