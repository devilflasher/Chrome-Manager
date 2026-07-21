// 负责应用程序的设置加载、保存和管理

import {
    GetSettingsForFrontend,
    UpdateSettings,
    SetCurrentGroup as SetCurrentGroupAPI,
    ResetCloseBehavior as ResetCloseBehaviorAPI,
    SetCloseBehavior as SetCloseBehaviorAPI,
    GetCloseBehavior as GetCloseBehaviorAPI,
    ClearAllDataAndRestart as ClearAllDataAndRestartAPI,
    ExportSettingsToFile as ExportSettingsToFileAPI,
    ImportSettingsFromFile as ImportSettingsFromFileAPI
} from '../../../bindings/chromemanager/chromeservice.js';
import { showNotification } from '../utils/notifications.js';

const DEBUG_LOGS = window.localStorage?.getItem('chromemanagerDebug') === '1';
const debugLog = (...args) => {
    if (DEBUG_LOGS) {
        console.debug(...args);
    }
};

// 当前设置
let currentSettings = null;

// 默认设置
const defaultSettings = {
    ShortcutPath: '',
    CacheDir: '',
    ScreenSelection: '',
    AutoModifyShortcutIcon: false,
    SyncToggleHotkey: '',
    WindowOpenSpeed: 0.2,
    LastWindowNumbers: '',
    WindowConfig: {
        // 窗口尺寸由后端配置统一管理，不在前端硬编码
        // Width和Height会从后端API动态获取
    },
    CustomArrangeParams: {
        width: 500,
        height: 400,
        startX: 0,
        startY: 0,
        horizontalSpacing: 0,
        verticalSpacing: 0,
        windowsPerRow: 5
    },
    CustomURLs: {}
};

/**
 * 加载设置
 * @returns {Promise<Object>} 设置对象
 */
export async function loadSettings() {
    try {
        currentSettings = await GetSettingsForFrontend();
        debugLog('Settings loaded:', currentSettings);

        if (!currentSettings) {
            currentSettings = { ...defaultSettings };
        }

        return currentSettings;
    } catch (error) {
        console.error('Failed to load settings:', error);
        showNotification('加载设置失败: ' + error.message, 'error');
        currentSettings = { ...defaultSettings };
        return currentSettings;
    }
}

/**
 * 保存设置
 * @param {Object} settings 要保存的设置对象
 * @returns {Promise<boolean>} 保存是否成功
 */
export async function saveSettings(settings) {
    try {
        await UpdateSettings(settings);
        currentSettings = { ...settings };
        debugLog('Settings saved successfully:', settings);
        return true;
    } catch (error) {
        console.error('Failed to save settings:', error);
        showNotification('保存设置失败: ' + error.message, 'error');
        return false;
    }
}

/**
 * 获取当前设置
 * @returns {Object} 当前设置对象
 */
export function getCurrentSettings() {
    return currentSettings || { ...defaultSettings };
}

/**
 * 更新特定设置项
 * @param {string} key 设置项的键
 * @param {*} value 设置项的值
 * @returns {Promise<boolean>} 更新是否成功
 */
export async function updateSetting(key, value) {
    const settings = getCurrentSettings();
    settings[key] = value;
    return await saveSettings(settings);
}

/**
 * 获取窗口配置
 * @returns {Object} 窗口配置对象
 */
export function getWindowConfig() {
    const settings = getCurrentSettings();
    return settings.WindowConfig || defaultSettings.WindowConfig;
}

/**
 * 保存窗口配置
 * @param {Object} windowConfig 窗口配置对象
 * @returns {Promise<boolean>} 保存是否成功
 */
export async function saveWindowConfig(windowConfig) {
    const settings = getCurrentSettings();
    settings.WindowConfig = { ...settings.WindowConfig, ...windowConfig };
    return await saveSettings(settings);
}

/**
 * 获取自定义URL配置
 * @returns {Object} 自定义URL对象
 */
export function getCustomUrls() {
    const settings = getCurrentSettings();
    return settings.CustomURLs || {};
}

/**
 * 保存自定义URL配置
 * @param {Object} customUrls 自定义URL对象
 * @returns {Promise<boolean>} 保存是否成功
 */
export async function saveCustomUrls(customUrls) {
    const settings = getCurrentSettings();
    settings.CustomURLs = customUrls;
    return await saveSettings(settings);
}

/**
 * 重置为默认设置
 * @returns {Promise<boolean>} 重置是否成功
 */
export async function resetToDefaults() {
    return await saveSettings({ ...defaultSettings });
}

// === 分组管理功能 ===

/**
 * 获取所有分组列表
 * @returns {Promise<Array>} 分组列表
 */
export async function getGroups() {
    try {
        const settings = await GetSettingsForFrontend();

        // 如果有分组数据，返回分组列表
        if (settings && settings.Groups && Array.isArray(settings.Groups)) {
            return settings.Groups;
        }

        // 如果没有分组，返回基于当前设置的默认分组
        return [{
            name: '默认分组',
            shortcutPath: settings?.ShortcutPath || '',
            cacheDir: settings?.CacheDir || ''
        }];
    } catch (error) {
        console.error('Failed to get groups:', error);
        // 如果获取失败，返回默认分组
        const settings = getCurrentSettings();
        return [{
            name: '默认分组',
            shortcutPath: settings?.ShortcutPath || '',
            cacheDir: settings?.CacheDir || ''
        }];
    }
}

/**
 * 添加新分组
 * @param {Object} groupData 分组数据 {name, shortcutPath, cacheDir}
 * @returns {Promise<boolean>} 添加是否成功
 */
export async function addGroup(groupData) {
    try {
        // 获取当前设置
        const settings = await GetSettingsForFrontend();

        // 初始化分组数组（如果不存在）
        if (!settings.Groups) {
            settings.Groups = [];
        }

        // 检查分组名是否已存在
        if (settings.Groups.some(group => group.name === groupData.name)) {
            showNotification('分组名称已存在', 'error');
            return false;
        }

        // 添加新分组
        settings.Groups.push({
            name: groupData.name,
            shortcutPath: groupData.shortcutPath || '',
            cacheDir: groupData.cacheDir || ''
        });

        // 如果这是第一个分组，启用分组模式并设置为当前分组
        if (settings.Groups.length === 1) {
            settings.EnableGroupMode = true;
            settings.CurrentGroup = groupData.name;
        }

        // 保存设置
        await UpdateSettings(settings);

        // 更新本地缓存
        currentSettings = settings;

        showNotification(`成功添加分组: ${groupData.name}`, 'success');
        return true;
    } catch (error) {
        console.error('Failed to add group:', error);
        showNotification('添加分组失败: ' + error.message, 'error');
        return false;
    }
}

/**
 * 删除分组
 * @param {string} groupName 分组名称
 * @returns {Promise<boolean>} 删除是否成功
 */
export async function removeGroup(groupName) {
    try {
        // 获取当前设置
        const settings = await GetSettingsForFrontend();

        if (!settings.Groups) {
            showNotification('没有可删除的分组', 'warning');
            return false;
        }

        // 找到要删除的分组
        const groupIndex = settings.Groups.findIndex(group => group.name === groupName);
        if (groupIndex === -1) {
            showNotification('分组不存在', 'error');
            return false;
        }

        // 删除分组
        settings.Groups.splice(groupIndex, 1);

        // 如果删除的是当前分组，切换到第一个分组或禁用分组模式
        if (settings.CurrentGroup === groupName) {
            if (settings.Groups.length > 0) {
                settings.CurrentGroup = settings.Groups[0].name;
            } else {
                settings.EnableGroupMode = false;
                settings.CurrentGroup = '';
            }
        }

        // 保存设置
        await UpdateSettings(settings);

        // 更新本地缓存
        currentSettings = settings;

        showNotification(`成功删除分组: ${groupName}`, 'success');
        return true;
    } catch (error) {
        console.error('Failed to remove group:', error);
        showNotification('删除分组失败: ' + error.message, 'error');
        return false;
    }
}

/**
 * 更新分组
 * @param {string} oldName 原分组名称
 * @param {Object} groupData 新分组数据
 * @returns {Promise<boolean>} 更新是否成功
 */
export async function updateGroup(oldName, groupData) {
    try {
        // 获取当前设置
        const settings = await GetSettingsForFrontend();

        if (!settings.Groups) {
            showNotification('没有可更新的分组', 'warning');
            return false;
        }

        // 找到要更新的分组
        const groupIndex = settings.Groups.findIndex(group => group.name === oldName);
        if (groupIndex === -1) {
            showNotification('分组不存在', 'error');
            return false;
        }

        // 如果更改了名称，检查新名称是否已存在
        if (groupData.name !== oldName && settings.Groups.some(group => group.name === groupData.name)) {
            showNotification('分组名称已存在', 'error');
            return false;
        }

        // 更新分组
        settings.Groups[groupIndex] = {
            name: groupData.name,
            shortcutPath: groupData.shortcutPath || '',
            cacheDir: groupData.cacheDir || ''
        };

        // 如果更改了当前分组的名称，更新CurrentGroup
        if (settings.CurrentGroup === oldName) {
            settings.CurrentGroup = groupData.name;
        }

        // 保存设置
        await UpdateSettings(settings);

        // 更新本地缓存
        currentSettings = settings;

        showNotification(`成功更新分组: ${groupData.name}`, 'success');
        return true;
    } catch (error) {
        console.error('Failed to update group:', error);
        showNotification('更新分组失败: ' + error.message, 'error');
        return false;
    }
}

/**
 * 设置当前分组
 * @param {string} groupName 分组名称
 * @returns {Promise<boolean>} 设置是否成功
 */
export async function setCurrentGroup(groupName) {
    try {
        // 直接调用专用API，不需要读取并回写整个设置对象
        // 避免 GetSettingsForFrontend 返回当前分组路径覆盖全局路径的问题
        await SetCurrentGroupAPI(groupName);

        // 更新本地缓存
        if (currentSettings) {
            currentSettings.CurrentGroup = groupName;
        }

        showNotification(`已切换到分组: ${groupName}`, 'success');
        return true;
    } catch (error) {
        console.error('Failed to set current group:', error);
        showNotification('切换分组失败: ' + error.message, 'error');
        return false;
    }
}

/**
 * 设置分组模式开关
 * @param {boolean} enabled 是否启用分组模式
 * @returns {Promise<boolean>} 设置是否成功
 */
export async function setGroupMode(enabled) {
    try {
        // 获取当前设置
        const settings = await GetSettingsForFrontend();

        // 设置分组模式
        settings.EnableGroupMode = enabled;

        // 如果启用分组模式但没有分组，创建默认分组
        if (enabled && (!settings.Groups || settings.Groups.length === 0)) {
            settings.Groups = [{
                name: '默认分组',
                shortcutPath: settings.ShortcutPath || '',
                cacheDir: settings.CacheDir || ''
            }];
            settings.CurrentGroup = '默认分组';
        }

        // 保存设置
        await UpdateSettings(settings);

        // 更新本地缓存
        currentSettings = settings;

        showNotification(`分组模式已${enabled ? '启用' : '禁用'}`, 'success');
        return true;
    } catch (error) {
        console.error('Failed to set group mode:', error);
        showNotification('设置分组模式失败: ' + error.message, 'error');
        return false;
    }
}

// 拖拽功能已移除 - 用户体验优化

// === 行为设置功能 ===

/**
 * 重置关闭行为设置
 * @returns {Promise<boolean>} 重置是否成功
 */
export async function resetCloseBehavior() {
    try {
        await ResetCloseBehaviorAPI();
        showNotification('关闭行为设置已重置', 'success');
        return true;
    } catch (error) {
        console.error('Failed to reset close behavior:', error);
        showNotification('重置关闭行为设置失败: ' + error.message, 'error');
        return false;
    }
}

/**
 * 设置关闭行为
 * @param {string} behavior 关闭行为 ("close" 或 "minimize")
 * @returns {Promise<boolean>} 设置是否成功
 */
export async function setCloseBehavior(behavior) {
    try {
        await SetCloseBehaviorAPI(behavior);
        showNotification(`关闭行为已设置为: ${behavior === 'close' ? '直接关闭' : '最小化到托盘'}`, 'success');
        return true;
    } catch (error) {
        console.error('Failed to set close behavior:', error);
        showNotification('设置关闭行为失败: ' + error.message, 'error');
        return false;
    }
}

/**
 * 获取关闭行为设置
 * @returns {Promise<string>} 当前的关闭行为设置
 */
export async function getCloseBehavior() {
    try {
        return await GetCloseBehaviorAPI();
    } catch (error) {
        console.error('Failed to get close behavior:', error);
        return "";
    }
}

// === 数据及配置设置功能 ===

/**
 * 清理所有应用数据并重启
 * @returns {Promise<boolean>} 是否成功
 */
export async function clearAllData() {
    try {
        await ClearAllDataAndRestartAPI();
        return true;
    } catch (error) {
        console.error('Failed to clear all data:', error);
        throw error;
    }
}

/**
 * 导出设置配置
 * @returns {Promise<boolean>} 导出是否成功
 */
export async function exportSettings() {
    try {
        await ExportSettingsToFileAPI();
        return true;
    } catch (error) {
        console.error('Failed to export settings:', error);
        showNotification('导出设置失败: ' + error.message, 'error');
        return false;
    }
}

/**
 * 导入设置配置
 * @returns {Promise<boolean>} 导入是否成功
 */
export async function importSettings() {
    try {
        await ImportSettingsFromFileAPI();
        return true;
    } catch (error) {
        console.error('Failed to import settings:', error);
        showNotification('导入设置失败: ' + error.message, 'error');
        return false;
    }
}

/**
 * 显示关闭行为选择对话框
 * @returns {Promise<string|null>} 用户选择的行为，取消时返回null
 */
export async function showCloseBehaviorDialog() {
    return new Promise((resolve) => {
        // 创建对话框背景
        const overlay = document.createElement('div');
        overlay.style.cssText = `
            position: fixed;
            top: 0;
            left: 0;
            width: 100%;
            height: 100%;
            background: rgba(0, 0, 0, 0.5);
            z-index: 10001;
            display: flex;
            justify-content: center;
            align-items: center;
        `;

        // 创建对话框
        const dialog = document.createElement('div');
        dialog.style.cssText = `
            background: white;
            border-radius: 8px;
            padding: 24px;
            max-width: 400px;
            width: 90%;
            box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
        `;

        dialog.innerHTML = `
            <h3 style="margin: 0 0 16px 0; color: #333;">选择窗口关闭行为</h3>
            <p style="margin: 0 0 24px 0; color: #666; line-height: 1.5;">
                当您关闭应用程序窗口时，您希望如何处理？
            </p>
            <div style="display: flex; gap: 12px; justify-content: flex-end;">
                <button id="closeBehaviorMinimize" style="
                    padding: 8px 16px;
                    border: 1px solid #ddd;
                    border-radius: 4px;
                    background: #f5f5f5;
                    cursor: pointer;
                    font-size: 14px;
                ">最小化到托盘</button>
                <button id="closeBehaviorClose" style="
                    padding: 8px 16px;
                    border: 1px solid #007bff;
                    border-radius: 4px;
                    background: #007bff;
                    color: white;
                    cursor: pointer;
                    font-size: 14px;
                ">直接关闭</button>
            </div>
        `;

        overlay.appendChild(dialog);
        document.body.appendChild(overlay);

        // 添加事件监听
        dialog.querySelector('#closeBehaviorClose').addEventListener('click', () => {
            document.body.removeChild(overlay);
            resolve('close');
        });

        dialog.querySelector('#closeBehaviorMinimize').addEventListener('click', () => {
            document.body.removeChild(overlay);
            resolve('minimize');
        });

        // 点击背景关闭（取消）
        overlay.addEventListener('click', (e) => {
            if (e.target === overlay) {
                document.body.removeChild(overlay);
                resolve(null);
            }
        });
    });
} 
