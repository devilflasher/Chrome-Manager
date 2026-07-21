// 窗口管理核心模块
// 负责Chrome窗口的管理、状态维护和相关操作

import { showNotification } from '../utils/notifications.js';
import { ImportWindowsWithIconSettings } from '../../../bindings/chromemanager/chromeservice.js';

const DEBUG_LOGS = window.localStorage?.getItem('chromemanagerDebug') === '1';
const debugLog = (...args) => {
    if (DEBUG_LOGS) {
        console.debug(...args);
    }
};

// 应用程序状态
const state = {
    windowConfig: {
        startX: "0",
        startY: "0",
        width: "600",
        height: "400",
        horizontalSpacing: "0",
        verticalSpacing: "0",
        windowsPerRow: "5",
    },
    windowRange: "1-10",
    masterWindowId: null, // 当前主控窗口ID（空列表时为null）
    windows: [
        // 窗口列表初始为空，用户导入窗口后才会有数据
    ]
};

/**
 * 获取应用程序状态
 * @returns {Object} 当前状态对象
 */
export function getState() {
    return state;
}

/**
 * 更新窗口配置
 * @param {Object} config 窗口配置对象
 */
export function updateWindowConfig(config) {
    state.windowConfig = { ...state.windowConfig, ...config };
    debugLog('Window config updated:', state.windowConfig);
}

/**
 * 设置窗口范围
 * @param {string} range 窗口范围字符串
 */
export function setWindowRange(range) {
    state.windowRange = range;
    debugLog('Window range updated:', range);
}

/**
 * 添加窗口到列表
 * @param {Object} window 窗口对象
 */
export function addWindow(window) {
    state.windows.push(window);
    
    // 移除自动设置主控窗口的逻辑，让用户手动选择
    debugLog('Window added:', window);
}

/**
 * 移除窗口
 * @param {number} windowId 窗口ID
 * @returns {boolean} 是否成功移除
 */
export function removeWindow(windowId) {
    const index = state.windows.findIndex(w => w.id === windowId);
    if (index !== -1) {
        state.windows.splice(index, 1);
        
        // 如果移除的是主控窗口，重新选择主控窗口
        if (state.masterWindowId === windowId) {
            state.masterWindowId = state.windows.length > 0 ? state.windows[0].id : null;
        }
        
        debugLog('Window removed:', windowId);
        return true;
    }
    return false;
}

/**
 * 更新窗口状态
 * @param {number} windowId 窗口ID
 * @param {Object} updates 要更新的属性
 * @returns {boolean} 是否成功更新
 */
export function updateWindow(windowId, updates) {
    const window = state.windows.find(w => w.id === windowId);
    if (window) {
        Object.assign(window, updates);
        debugLog('Window updated:', windowId, updates);
        return true;
    }
    return false;
}

/**
 * 切换窗口选中状态
 * @param {number} windowId 窗口ID
 * @returns {boolean} 新的选中状态
 */
export function toggleWindowSelection(windowId) {
    const window = state.windows.find(w => w.id === windowId);
    if (window) {
        window.selected = !window.selected;
        debugLog('Window selection toggled:', windowId, window.selected);
        return window.selected;
    }
    return false;
}

/**
 * 设置主控窗口
 * @param {number} windowId 窗口ID
 * @returns {boolean} 是否成功设置
 */
export function setMasterWindow(windowId) {
    const window = state.windows.find(w => w.id === windowId);
    if (window) {
        // 清除所有窗口的主控状态
        state.windows.forEach(w => {
            w.isMaster = false;
        });

        // 设置新的主控窗口
        window.isMaster = true;
        state.masterWindowId = windowId;

        return true;
    }
    return false;
}

/**
 * 获取选中的窗口列表
 * @returns {Array} 选中的窗口数组
 */
export function getSelectedWindows() {
    return state.windows.filter(w => w.selected);
}

/**
 * 获取主控窗口
 * @returns {Object|null} 主控窗口对象或null
 */
export function getMasterWindow() {
    return state.windows.find(w => w.id === state.masterWindowId) || null;
}

/**
 * 全选所有窗口
 */
export function selectAllWindows() {
    state.windows.forEach(window => {
        window.selected = true;
    });
    debugLog('All windows selected');
}

/**
 * 取消选择所有窗口
 */
export function deselectAllWindows() {
    state.windows.forEach(window => {
        window.selected = false;
    });
    debugLog('All windows deselected');
}

/**
 * 切换全选状态
 * @returns {boolean} 返回新的全选状态（true=全选，false=取消全选）
 */
export function toggleSelectAll() {
    if (state.windows.length === 0) {
        return false; // 没有窗口时忽略
    }
    
    const allSelected = state.windows.every(window => window.selected);
    
    if (allSelected) {
        // 当前全选状态，执行取消全选
        deselectAllWindows();
        return false;
    } else {
        // 非全选状态，执行全选
        selectAllWindows();
        return true;
    }
}

/**
 * 检查是否全部选中
 * @returns {boolean} 是否全部选中
 */
export function isAllSelected() {
    if (state.windows.length === 0) {
        return false;
    }
    return state.windows.every(window => window.selected);
}

/**
 * 清空窗口列表
 */
export function clearWindows() {
    state.windows = [];
    state.masterWindowId = null;
    debugLog('Window list cleared');
}

/**
 * 添加演示窗口（用于测试）
 * @returns {Object} 新添加的窗口对象
 */
export function addDemoWindow() {
    // 如果窗口数组为空，从1开始，否则使用最大ID+1
    const newId = state.windows.length === 0 ? 1 : Math.max(...state.windows.map(w => w.id)) + 1;
    
    const newWindow = {
        id: newId,
        selected: false,
        title: `New Tab ${newId} - Chrome`
    };
    
    addWindow(newWindow);
    return newWindow;
}

/**
 * 导入Chrome窗口
 * @param {boolean} suppressNoProcessError - 是否抑制"未找到Chrome进程"的错误提示
 * @returns {Promise<Array>} 导入的窗口列表
 */
export async function importChromeWindows(suppressNoProcessError = false) {
    try {
        // 导入窗口时总是处理图标
        const windowInfos = await ImportWindowsWithIconSettings();

        if (!windowInfos || !Array.isArray(windowInfos)) {
            showNotification('后端返回的窗口数据无效', 'warning');
            return [];
        }

        // 保存旧的主控窗口ID，以便后续检查
        const previousMasterWindowId = state.masterWindowId;
        // 清空现有窗口列表
        clearWindows();

        // 将后端返回的WindowInfo转换为前端状态格式
        const importedWindows = windowInfos.map((windowInfo, index) => {
            const window = {
                id: windowInfo.number || (index + 1),
                selected: false,
                title: windowInfo.title || `Chrome Window ${windowInfo.number || index + 1}`,
                hwnd: windowInfo.hwnd,
                pid: windowInfo.pid,
                number: windowInfo.number,
                userDataDir: windowInfo.userDataDir,
                debugPort: windowInfo.debugPort,
                isMaster: windowInfo.isMaster || false,
                isRunning: windowInfo.isRunning || true
            };

            addWindow(window);
            return window;
        });

        // 如果之前有主控窗口，检查它是否还在新的窗口列表中
        if (previousMasterWindowId) {
            const masterStillExists = importedWindows.some(w => w.id === previousMasterWindowId);
            if (masterStillExists) {
                state.masterWindowId = previousMasterWindowId;
            }
        }

        // 只有当导入了窗口时才显示成功提示
        if (importedWindows.length > 0) {
            const message = `成功导入 ${importedWindows.length} 个Chrome窗口，请手动选择主控窗口`;
            showNotification(message, 'success');
        }

        return importedWindows;
    } catch (error) {
        console.error('Failed to import Chrome windows:', error);
        
        // 根据错误类型提供更友好的错误信息
        let errorMessage = '导入Chrome窗口失败';
        let shouldClearList = false;
        
        if (error.message.includes('failed to find Chrome processes')) {
            if (suppressNoProcessError) {
                if (state.windows.length > 0) {
                    clearWindows();
                }
                return [];
            }

            errorMessage = '未找到正在运行的Chrome进程，请先启动Chrome浏览器';
            shouldClearList = true;
        } else if (isAccessibilityPermissionError(error)) {
            errorMessage = '导入窗口需要 macOS 辅助功能权限，请授权 ChromeManager.app 后重启软件再试';
            window.dispatchEvent(new CustomEvent('chromemanager:accessibility-permission-required', {
                detail: { source: 'import-windows' }
            }));
        } else if (error.message.includes('access denied')) {
            errorMessage = '权限不足，请检查 macOS 辅助功能权限';
        } else if (error.message) {
            errorMessage = `导入失败: ${error.message}`;
        }

        // 如果之前有窗口数据且需要清空列表，则清空
        if (shouldClearList && state.windows.length > 0) {
            clearWindows();
            errorMessage += '，已清空窗口列表';
        }
        
	        showNotification(errorMessage, 'error');
	        return [];
	    }
	}

function isAccessibilityPermissionError(error) {
    const message = String(error?.message || error || '');
    return message.includes('辅助功能权限被拒绝') ||
        message.includes('AXIsProcessTrusted') ||
        message.includes('Accessibility');
}

/**
 * 获取窗口数量统计
 * @returns {Object} 窗口统计信息
 */
export function getWindowStats() {
    const total = state.windows.length;
    const selected = getSelectedWindows().length;
    const hasMaster = !!getMasterWindow();
    
    return {
        total,
        selected,
        hasMaster,
        unselected: total - selected
    };
} 
