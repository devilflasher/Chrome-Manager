package utils

import (
	"time"
)

type PopupDirectionController struct {
	enabled bool
}

func NewPopupDirectionController() *PopupDirectionController {
	return &PopupDirectionController{
		enabled: true,
	}
}

func (pdc *PopupDirectionController) SetEnabled(enabled bool) {
	pdc.enabled = enabled
}

func (pdc *PopupDirectionController) IsEnabled() bool {
	return pdc.enabled
}

func (pdc *PopupDirectionController) ProcessChromePopups(chromeHWND uintptr) {
	// macOS stub
}

func (pdc *PopupDirectionController) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"enabled":           pdc.enabled,
		"processed_popups":  0,
		"last_process_time": time.Now(),
	}
}
