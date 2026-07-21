// Placeholder to trigger next search step - realized I need to find the rendering code first.
// 负责应用程序的设置加载、保存和管理

import {
    GetSettingsForFrontend,
    UpdateSettings,
    ResetCloseBehavior,
    SetCloseBehavior,
    GetCloseBehavior,
    ClearAllData,
    ExportSettings,
    ImportSettings,
    RestartApplication,
    AddGroup as BackendAddGroup,
    RemoveGroup as BackendRemoveGroup,
    UpdateGroup as BackendUpdateGroup,
    SetCurrentGroup as BackendSetCurrentGroup,
    SetGroupMode as BackendSetGroupMode
} from '../../../bindings/chromemanager/chromeservice.js';
import { showNotification } from '../utils/notifications.js';

// 当前设置
let currentSettings = null;

// 默认设置
const defaultSettings = {
    ShortcutPath: '',
    CacheDir: '',
    ScreenSelection: '',
    AutoModifyShortcutIcon: true,
    SyncToggleHotkey: '',
    WindowOpenSpeed: 0.2,
    WindowCloseInterval: 0.1,
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
        await BackendAddGroup({
            name: groupData.name,
            shortcutPath: groupData.shortcutPath || '',
            cacheDir: groupData.cacheDir || ''
        });
        currentSettings = await GetSettingsForFrontend();
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
        await BackendRemoveGroup(groupName);
        currentSettings = await GetSettingsForFrontend();
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
        await BackendUpdateGroup(oldName, {
            name: groupData.name,
            shortcutPath: groupData.shortcutPath || '',
            cacheDir: groupData.cacheDir || ''
        });
        currentSettings = await GetSettingsForFrontend();
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
export async function setCurrentGroup(groupName, options = {}) {
    try {
        await BackendSetCurrentGroup(groupName);
        currentSettings = await GetSettingsForFrontend();
        if (!options.silent) {
            showNotification(`已切换到分组: ${groupName}`, 'success');
        }
        return true;
    } catch (error) {
        console.error('Failed to set current group:', error);
        if (!options.silent) {
            showNotification('切换分组失败: ' + error.message, 'error');
        }
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
        await BackendSetGroupMode(enabled);
        currentSettings = await GetSettingsForFrontend();
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
        await ResetCloseBehavior();
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
        await SetCloseBehavior(behavior);
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
        return await GetCloseBehavior();
    } catch (error) {
        console.error('Failed to get close behavior:', error);
        return "";
    }
}

// === 数据及配置设置功能 ===

/**
 * 重置配置文件
 * @returns {Promise<boolean>} 重置是否成功
 */
export async function clearAllData() {
    try {
        await ClearAllData();
        showNotification('配置已重置为默认状态', 'success');
        return true;
    } catch (error) {
        console.error('Failed to clear all data:', error);
        showNotification('重置配置失败: ' + error.message, 'error');
        return false;
    }
}

/**
 * 导出设置配置
 * @returns {Promise<Object|null>} 导出的设置对象，失败时返回null
 */
export async function exportSettings() {
    try {
        const settings = await ExportSettings();
        return settings;
    } catch (error) {
        console.error('Failed to export settings:', error);
        showNotification('导出设置失败: ' + error.message, 'error');
        return null;
    }
}

/**
 * 导入设置配置
 * @param {Object} settingsData 要导入的设置数据
 * @returns {Promise<boolean>} 导入是否成功
 */
export async function importSettings(settingsData) {
    try {
        await ImportSettings(settingsData);

        showNotification('设置导入成功，应用将自动重启...', 'success');

        // 延迟 500ms 让通知显示出来
        await new Promise(resolve => setTimeout(resolve, 500));

        await RestartApplication();

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
