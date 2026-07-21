package utils

import (
	"fmt"
	"log"
	"time"
)

type InputTarget struct {
	HWND      uintptr
	DebugPort int
}

func (im *InputManager) InputRandomNumbersToTargets(targets []InputTarget, config RandomInputConfig) error {
	if len(targets) == 0 {
		return fmt.Errorf("no input targets")
	}
	if config.MaxValue <= config.MinValue {
		return fmt.Errorf("max value must be greater than min value")
	}

	randomTexts := make([]string, len(targets))
	for i := range randomTexts {
		if config.IsFloat {
			randomNumber := config.MinValue + im.rand.Float64()*(config.MaxValue-config.MinValue)
			format := fmt.Sprintf("%%.%df", config.DecimalPlaces)
			randomTexts[i] = fmt.Sprintf(format, randomNumber)
			if stringsContainsDot(randomTexts[i]) {
				randomTexts[i] = trimFloatText(randomTexts[i])
			}
		} else {
			randomNumber := int(config.MinValue) + im.rand.Intn(int(config.MaxValue-config.MinValue)+1)
			randomTexts[i] = fmt.Sprintf("%d", randomNumber)
		}
	}

	return im.InputTextFromLinesToTargets(targets, randomTexts, "sequential", config.Overwrite, config.Delayed)
}

func (im *InputManager) InputTextFromFileToTargets(targets []InputTarget, config TextInputConfig) error {
	if !im.active.CompareAndSwap(false, true) {
		return fmt.Errorf("input operation is already running")
	}
	defer im.active.Store(false)

	if len(targets) == 0 {
		return fmt.Errorf("no input targets")
	}

	var lines []string
	var err error
	if config.FilePath == "DIRECT_CONTENT" && len(config.DirectContent) > 0 {
		lines = config.DirectContent
	} else {
		lines, err = im.readTextFile(config.FilePath)
		if err != nil {
			return fmt.Errorf("read file failed: %v", err)
		}
	}
	if len(lines) == 0 {
		return fmt.Errorf("text content is empty")
	}

	return im.inputPreparedLinesToTargets(targets, lines, config.InputMethod, config.Overwrite, config.Delayed)
}

func (im *InputManager) InputTextFromLinesToTargets(targets []InputTarget, lines []string, inputMethod string, overwrite, delayed bool) error {
	if !im.active.CompareAndSwap(false, true) {
		return fmt.Errorf("input operation is already running")
	}
	defer im.active.Store(false)

	if len(targets) == 0 {
		return fmt.Errorf("no input targets")
	}
	if len(lines) == 0 {
		return fmt.Errorf("text lines are empty")
	}

	return im.inputPreparedLinesToTargets(targets, lines, inputMethod, overwrite, delayed)
}

func (im *InputManager) inputPreparedLinesToTargets(targets []InputTarget, lines []string, inputMethod string, overwrite, delayed bool) error {
	textLines := make([]string, len(targets))
	if inputMethod == "random" {
		availableLines := make([]string, len(lines))
		copy(availableLines, lines)
		im.rand.Shuffle(len(availableLines), func(i, j int) {
			availableLines[i], availableLines[j] = availableLines[j], availableLines[i]
		})
		lineIndex := 0
		for i := range textLines {
			if lineIndex >= len(availableLines) {
				im.rand.Shuffle(len(availableLines), func(i, j int) {
					availableLines[i], availableLines[j] = availableLines[j], availableLines[i]
				})
				lineIndex = 0
			}
			textLines[i] = availableLines[lineIndex]
			lineIndex++
		}
	} else {
		for i := range textLines {
			textLines[i] = lines[i%len(lines)]
		}
	}

	successCount := 0
	for i, target := range targets {
		text := textLines[i]
		if err := im.inputTextToTarget(target, text, overwrite, delayed); err != nil {
			log.Printf("input to window %d failed: %v", target.HWND, err)
		} else {
			successCount++
		}

		if i < len(targets)-1 {
			if delayed {
				time.Sleep(200 * time.Millisecond)
			} else {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}

	if successCount == 0 {
		return fmt.Errorf("all window inputs failed")
	}
	return nil
}

func (im *InputManager) inputTextToTarget(target InputTarget, text string, overwrite, delayed bool) error {
	if target.DebugPort == 0 {
		return fmt.Errorf("window %d has no debug port", target.HWND)
	}
	if err := im.inputTextToFocusedPageElement(target.DebugPort, text, overwrite, delayed); err != nil {
		return fmt.Errorf("CDP activeElement input failed: port=%d hwnd=%d: %w", target.DebugPort, target.HWND, err)
	}
	return nil
}

func stringsContainsDot(s string) bool {
	for _, r := range s {
		if r == '.' {
			return true
		}
	}
	return false
}

func trimFloatText(s string) string {
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	if s == "" {
		return "0"
	}
	return s
}
