import './style.css';
import './app.css';
import { Events } from '@wailsio/runtime';

// 导入必要的模块
import {
    getState,
    setWindowRange,
    updateWindowConfig,
    toggleWindowSelection,
    setMasterWindow,
    getMasterWindow,
    toggleSelectAll,
    isAllSelected,
    clearWindows,
    getSelectedWindows,
    importChromeWindows
} from './modules/core/window-manager.js';

// 导入后端API
import {
    GetSettingsForFrontend,
    UpdateSettings,
    OpenWindows,
    ImportWindowsWithIconSettings,
    SelectFolder,
    AutoArrangeWindows,
    CustomArrangeWindows,
    GetScreensInfo,
    ArrangeWindowsOnScreen,
    CloseApplication,
    CloseWindows,
    MaximizeMainWindow,
    MinimizeMainWindow,
    BatchOpenURL,
    BatchOpenURLToSelectedWindows,
    GetPresetURLs,
    SaveCustomURL,
    DeleteCustomURL,
    GetRandomInputConfig,
    SaveRandomInputConfig,
    CreateEnvironments,
    GetEnvironmentCreationDebugInfo,
    KeepOnlyCurrentTab,
    KeepOnlyNewTab,
    StartSync,
    StopSync,
    UpdateSelectedWindows,
    SetMasterWindow,
    RestoreDefaultIcons,
    SelectFile,
    GetFilePreview,
    InputRandomNumbers,
    InputTextFromLines,
    InputTextFromFile,
    OpenAccessibilitySettings,
    RevealChromeManagerAppInFinder,
    RevealTerminalAppInFinder,
    ResetCloseBehavior,
    ScanBrowserCaches,
    CleanBrowserCaches,
    RepairExtensionsFromDonor,
    OpenExternalURL
} from '../bindings/chromemanager/chromeservice.js';

// 导入模块化组件
import {
    loadSettings,
    getCurrentSettings,
    getGroups,
    setCurrentGroup,
    removeGroup,
    clearAllData,
    exportSettings,
    importSettings,
    updateGroup,
    addGroup
} from './modules/config/settings.js';
import { showNotification, showSuccess, showError, showWarning, showInfo } from './modules/utils/notifications.js';
import { getDOMElements, validateElements } from './modules/ui/dom-elements.js';
import { initTabs, switchToTab } from './modules/ui/tabs.js';

// DOM元素引用
let elements = {};
const AUTOMATION_SKILL_REPO_URL = 'https://github.com/devilflasher/chromemanager-skill';
const ACCESSIBILITY_ASSIST_SKIP_KEY = 'chromemanager_accessibility_assist_skip_forever';
const DEBUG_LOGS = window.localStorage?.getItem('chromemanagerDebug') === '1';
const debugLog = (...args) => {
    if (DEBUG_LOGS) {
        console.debug(...args);
    }
};

/**
 * 应用程序初始化
 */
async function init() {
    debugLog('Initializing Chrome Manager...');

    // 获取DOM元素引用
    elements = getDOMElements();

    // 验证关键元素
    const missingElements = validateElements(elements);
    if (missingElements.length > 0) {
        showError(`关键DOM元素缺失: ${missingElements.join(', ')}`);
        return;
    }

    debugLog('DOM elements loaded:', Object.keys(elements));

    // 初始化标签页
    initTabs();

    // 绑定事件
    bindEvents();

    Events.On('sync.toggle.hotkey', () => {
        if (elements?.syncToggleBtn && !elements.syncToggleBtn.disabled) {
            elements.syncToggleBtn.click();
        }
    });

    // 监听同步自动停止事件（当主控窗口被关闭时）
    Events.On('sync.auto.stopped', () => {
        if (window.resetSyncButtonState) {
            window.resetSyncButtonState();
        }
    });

    // 监听同步状态事件
    Events.On('sync.started', (data) => {
        // Wails 3 事件系统：data.data 是一个数组，需要取第一个元素
        let eventData = data?.data;
        if (Array.isArray(eventData) && eventData.length > 0) {
            eventData = eventData[0];
        }

        if (window.onSyncStarted) {
            window.onSyncStarted(eventData);
        }
    });

    Events.On('sync.start.failed', (data) => {
        // Wails 3 事件系统：提取数组中的第一个元素
        let eventData = data?.data;
        if (Array.isArray(eventData) && eventData.length > 0) {
            eventData = eventData[0];
        }
        if (window.onSyncStartFailed) {
            window.onSyncStartFailed(eventData);
        }
    });

    Events.On('sync.stopped', () => {
        if (window.onSyncStopped) {
            window.onSyncStopped();
        }
    });

    Events.On('sync.stop.failed', (data) => {
        // Wails 3 事件系统：提取数组中的第一个元素
        let eventData = data?.data;
        if (Array.isArray(eventData) && eventData.length > 0) {
            eventData = eventData[0];
        }
        if (window.onSyncStopFailed) {
            window.onSyncStopFailed(eventData);
        }
    });

    // 加载设置
    try {
        await loadSettings();
        debugLog('Settings loaded successfully');

        // 更新表单值（确保在设置加载完成后执行）
        updateFormValues();
    } catch (error) {
        console.error('Failed to load settings:', error);
        showError('加载设置失败: ' + error.message);

        // 即使加载失败也要更新表单值（使用默认值）
        updateFormValues();
    }

    // 初始化自定义URL选择框
    initCustomUrlSelect();

    // 初始化分组选择器
    initializeGroupSelects();

    // 启动时提醒用户同时授权 ChromeManager.app 与终端（支持今日不再提醒）
    if (shouldShowAccessibilityPermissionAssist()) {
        setTimeout(() => {
            showAccessibilityPermissionAssistModal();
        }, 300);
    }

    debugLog('Chrome Manager initialized successfully');

    // 在这里定义updateSelectAllButtonText函数，确保能访问elements
    window.updateSelectAllButtonText = function () {
        debugLog('=== updateSelectAllButtonText 被调用 ===');
        const button = elements.configureAllBtn;
        debugLog('全选按钮元素:', button);

        if (!button) {
            debugLog('全选按钮不存在');
            return;
        }

        const state = getState();
        debugLog('当前窗口状态:', state);
        debugLog('窗口数量:', state.windows.length);

        if (state.windows.length === 0) {
            button.textContent = '全部选择';
            debugLog('设置按钮文字为: 全部选择 (无窗口)');
            return;
        }

        const allSelected = isAllSelected();
        debugLog('是否全部选中:', allSelected);

        const newText = allSelected ? '取消全选' : '全部选择';
        button.textContent = newText;
        debugLog('设置按钮文字为:', newText);
    };
}

/**
 * 绑定事件监听器
 */
function bindEvents() {
    debugLog('Binding events...');

    // 窗口控制按钮
    bindWindowControls();

    // 主要功能按钮
    bindMainFunctionButtons();

    // 工具栏按钮
    bindToolbarButtons();

    // 表单事件
    bindFormEvents();

    debugLog('Events bound successfully');

    // OpenClaw API 相关事件
    Events.On('openclaw:import_windows', async () => {
        try {
            await importChromeWindows();
            renderWindowTable();
            setupTableEventListeners();
            if (window.updateSelectAllButtonText) {
                window.updateSelectAllButtonText();
            }
            syncSelectedToBackend();
        } catch (error) {
            console.error('API 导入窗口事件执行失败:', error);
        }
    });

    window.addEventListener('chromemanager:accessibility-permission-required', () => {
        showAccessibilityPermissionAssistModal({
            reason: '导入窗口需要读取 Chrome 窗口列表和窗口位置，因此必须先给 ChromeManager.app 辅助功能权限。如果同步键鼠仍无效，请同时在“输入监控”中授权 ChromeManager.app。如果您之前添加过权限但重新打包了程序，请在权限界面点击“-”号按钮删除之前的程序，重新添加新打包的程序。',
            showSkipForever: false
        });
    });

    Events.On('openclaw:select_all', (e) => {
        let isSelect = true;
        if (e && e.data && e.data.length > 0 && typeof e.data[0].select !== 'undefined') {
            isSelect = e.data[0].select;
        }
        
        const allSelected = isAllSelected();
        if ((isSelect && !allSelected) || (!isSelect && allSelected)) {
             toggleSelectAll();
             if (window.updateSelectAllButtonText) {
                 window.updateSelectAllButtonText();
             }
             renderWindowTable();
             setupTableEventListeners();
             syncSelectedToBackend();
        }
    });
}

/**
 * 绑定窗口控制按钮事件
 */
function bindWindowControls() {
    // 最小化按钮
    elements.minimizeBtn?.addEventListener('click', async () => {
        try {
            await MinimizeMainWindow();
            debugLog('Window minimized successfully');
        } catch (err) {
            console.error('Failed to minimize window:', err);
            showError('最小化窗口失败');
        }
    });

    // 最大化按钮
    elements.maximizeBtn?.addEventListener('click', async () => {
        try {
            await MaximizeMainWindow();
            debugLog('Window maximize toggled successfully');
        } catch (err) {
            console.error('Failed to maximize window:', err);
            showError('最大化窗口失败');
        }
    });

    // 关闭按钮
    elements.closeBtn?.addEventListener('click', async () => {
        try {
            // 直接关闭应用程序
            debugLog('直接关闭应用程序');

            await CloseApplication();
        } catch (err) {
            console.error('Failed to close application:', err);
            showError('关闭应用程序失败: ' + err.message);
        }
    });
}

/**
 * 绑定主要功能按钮事件
 */
function bindMainFunctionButtons() {
    // 设置按钮
    elements.settingsBtn?.addEventListener('click', async () => {
        try {
            showSettingsModal();
        } catch (error) {
            console.error('Failed to open settings modal:', error);
            showError('打开设置失败: ' + error.message);
        }
    });

    // 打开窗口按钮
    elements.openWindowBtn?.addEventListener('click', async () => {
        const numbers = elements.windowRange?.value?.trim();
        if (!numbers) {
            showError('请输入窗口编号');
            return;
        }

        try {
            await OpenWindows(numbers);
            showSuccess('窗口打开成功');
        } catch (error) {
            console.error('Failed to open windows:', error);

            // 对特定错误提供更友好的提示
            if (error.message && error.message.includes('shortcut path is not configured')) {
                showError('请先在设置中配置快捷方式路径，然后再打开窗口');
                // 自动打开设置对话框
                setTimeout(() => {
                    try {
                        showSettingsModal();
                    } catch (settingError) {
                        console.error('Failed to open settings modal:', settingError);
                    }
                }, 1500);
            } else if (error.message && error.message.includes('成功打开')) {
                // 部分成功的情况，使用黄色警告提示
                showWarning(error.message);
            } else {
                showError('打开窗口失败: ' + error.message);
            }
        }
    });

    // 窗口编号输入框回车键
    elements.windowRange?.addEventListener('keypress', (e) => {
        if (e.key === 'Enter') {
            elements.openWindowBtn?.click();
        }
    });

    // 批量打开网页按钮
    elements.batchOpenBtn?.addEventListener('click', async () => {
        const url = elements.urlInput?.value?.trim();
        if (!url) {
            showError('请输入网址');
            return;
        }

        try {
            // 移除重复提示，只保留完成后的成功提示
            await BatchOpenURLToSelectedWindows(url);
            showSuccess('网页批量打开成功');
        } catch (error) {
            console.error('批量打开网页失败:', error);
            if (error.message?.includes('已从列表移除')) {
                await importChromeWindows(true);
                renderWindowTable();
                setupTableEventListeners();
                window.updateSelectAllButtonText?.();
            }
            showError('批量打开网页失败: ' + error.message);
        }
    });

    // 网址输入框回车键
    elements.urlInput?.addEventListener('keypress', (e) => {
        if (e.key === 'Enter') {
            elements.batchOpenBtn?.click();
        }
    });

    // 预设网址选择变更
    elements.customUrlSelect?.addEventListener('change', (e) => {
        if (e.target.value) {
            elements.urlInput.value = e.target.value;
        }
    });

    // 自定义网址管理按钮
    elements.manageUrlsBtn?.addEventListener('click', async () => {
        try {
            await showCustomURLsModal();
        } catch (error) {
            console.error('打开自定义网址管理失败:', error);
            showError('打开自定义网址管理失败: ' + error.message);
        }
    });

    // 仅保留当前标签页按钮
    elements.keepCurrentTabBtn?.addEventListener('click', async () => {
        try {
            const selectedWindows = getSelectedWindows();
            if (selectedWindows.length === 0) {
                showError('请先选择要操作的窗口');
                return;
            }

            // 提取窗口编号
            const windowNumbers = selectedWindows.map(w => w.id);
            debugLog('对窗口执行"仅保留当前标签页"操作:', windowNumbers);

            // 动态导入API并调用

            await KeepOnlyCurrentTab(windowNumbers);

            showSuccess(`成功为 ${selectedWindows.length} 个窗口保留当前标签页`);
        } catch (error) {
            console.error('仅保留当前标签页失败:', error);
            showError('操作失败: ' + error.message);
        }
    });

    // 仅保留新标签页按钮
    elements.keepNewTabBtn?.addEventListener('click', async () => {
        try {
            const selectedWindows = getSelectedWindows();
            if (selectedWindows.length === 0) {
                showError('请先选择要操作的窗口');
                return;
            }

            // 提取窗口编号
            const windowNumbers = selectedWindows.map(w => w.id);
            debugLog('对窗口执行"仅保留新标签页"操作:', windowNumbers);

            // 动态导入API并调用

            await KeepOnlyNewTab(windowNumbers);

            showSuccess(`成功为 ${selectedWindows.length} 个窗口创建新标签页`);
        } catch (error) {
            console.error('仅保留新标签页失败:', error);
            showError('操作失败: ' + error.message);
        }
    });

    // 随机数字输入按钮
    elements.randomNumberBtn?.addEventListener('click', async () => {
        try {
            const selectedWindows = getSelectedWindows();
            if (selectedWindows.length === 0) {
                showError('请先选择要操作的窗口');
                return;
            }

            // 显示随机数字输入配置对话框
            showRandomNumberDialog(selectedWindows);
        } catch (error) {
            console.error('随机数字输入失败:', error);
            showError('操作失败: ' + error.message);
        }
    });

    // 指定文本输入按钮
    elements.textInputBtn?.addEventListener('click', async () => {
        try {
            const selectedWindows = getSelectedWindows();
            if (selectedWindows.length === 0) {
                showError('请先选择要操作的窗口');
                return;
            }

            // 显示文本输入配置对话框
            showTextInputDialog(selectedWindows);
        } catch (error) {
            console.error('指定文本输入失败:', error);
            showError('操作失败: ' + error.message);
        }
    });

    // 批量创建环境按钮
    elements.createEnvBtn?.addEventListener('click', async () => {
        debugLog('🏭 创建环境按钮被点击');

        // 保存原始按钮文本，确保在finally块中可以访问
        const originalText = elements.createEnvBtn.textContent;

        try {
            // 首先获取调试信息
            debugLog('🔍 获取环境创建调试信息...');
            const debugInfo = await GetEnvironmentCreationDebugInfo();
            debugLog('📋 调试信息:', debugInfo);

            // 检查设置是否配置
            if (!debugInfo.cacheDir || !debugInfo.shortcutPath) {
                showError(`请先在设置中配置目录：
                缓存目录: ${debugInfo.cacheDir || '未配置'}
                快捷方式目录: ${debugInfo.shortcutPath || '未配置'}
                
                请点击设置按钮进行配置。`);
                return;
            }

            // 检查Chrome路径
            if (debugInfo.chromePathError) {
                showError('Chrome浏览器路径检测失败: ' + debugInfo.chromePathError);
                return;
            }

            // 获取输入的环境编号
            const envNumbers = elements.envNumbers?.value?.trim();
            debugLog('输入的环境编号:', envNumbers);

            if (!envNumbers) {
                showError('请输入要创建的环境编号，例如: 1-5,7,9');
                return;
            }

            // 保存当前参数到配置文件
            try {

                await UpdateSettings({ LastEnvCreationNumbers: envNumbers });
                debugLog('✅ 已保存环境创建参数到配置文件:', envNumbers);
            } catch (error) {
                console.warn('⚠️ 保存环境创建参数失败:', error);
                // 不阻止创建流程，只是记录警告
            }

            // 解析环境编号数量用于显示进度
            const estimatedCount = envNumbers.split(',').reduce((total, part) => {
                if (part.includes('-')) {
                    const [start, end] = part.split('-').map(n => parseInt(n.trim()));
                    return total + (end - start + 1);
                } else {
                    return total + 1;
                }
            }, 0);

            // 显示加载状态
            elements.createEnvBtn.textContent = `并发创建中...`;
            elements.createEnvBtn.disabled = true;

            debugLog('🚀 开始并发创建环境...');
            debugLog('当前配置:');
            debugLog('- 缓存目录:', debugInfo.cacheDir);
            debugLog('- 快捷方式目录:', debugInfo.shortcutPath);
            debugLog('- Chrome路径:', debugInfo.chromePath);
            debugLog('- 预计创建数量:', estimatedCount);

            const startTime = Date.now();

            // 调用后端方法创建环境
            await CreateEnvironments(envNumbers);

            const endTime = Date.now();
            const duration = ((endTime - startTime) / 1000).toFixed(1);

            debugLog('✅ 环境创建成功');
            showSuccess('✅ 环境创建成功！');

            // 不清空输入框，保留用户刚才使用的参数
            debugLog('✅ 保留用户刚才使用的创建参数:', envNumbers);

        } catch (error) {
            console.error('❌ 创建环境失败:', error);

            // 获取详细错误信息
            let errorMessage = '创建环境失败: ' + error.message;

            // 如果是目录配置问题，提供更详细的提示
            if (error.message.includes('配置缓存目录和快捷方式目录')) {
                errorMessage += '\n\n💡 解决方法：\n1. 点击"设置"按钮\n2. 配置"缓存目录"和"快捷方式目录"\n3. 点击"保存设置"\n4. 重新尝试创建环境';
            }

            showError(errorMessage);
        } finally {
            // 恢复按钮状态
            if (elements.createEnvBtn) {
                elements.createEnvBtn.textContent = originalText || '开始创建';
                elements.createEnvBtn.disabled = false;
            }
        }
    });

    elements.browserCacheCleanupBtn?.addEventListener('click', () => {
        try {
            const groupName = elements.browserCacheGroupSelect?.value?.trim() || '默认分组';
            const windowNumbers = elements.browserCacheWindowRange?.value?.trim();

            if (!windowNumbers) {
                showError('请输入要扫描的窗口编号');
                return;
            }

            showBrowserCacheCleanupModal(groupName, windowNumbers);
        } catch (error) {
            console.error('Failed to open browser cache cleanup modal:', error);
            showError('打开浏览器缓存清理失败: ' + error.message);
        }
    });

    elements.automationOpenclawBtn?.addEventListener('click', () => {
        try {
            showAutomationOpenClawModal();
        } catch (error) {
            console.error('Failed to open automation integration modal:', error);
            showError('打开 OpenClaw 接入说明失败: ' + error.message);
        }
    });

    elements.automationExamplesBtn?.addEventListener('click', () => {
        try {
            showAutomationExamplesModal();
        } catch (error) {
            console.error('Failed to open automation examples modal:', error);
            showError('打开示例指令失败: ' + error.message);
        }
    });

    // 绑定底部链接点击事件 - 使用系统浏览器打开
    document.querySelectorAll('.footer-link').forEach(link => {
        link.addEventListener('click', async (e) => {
            e.preventDefault();
            const url = link.getAttribute('href');
            if (url && url !== 'javascript:void(0)') {
                try {
                    debugLog('Opening external URL:', url);
                    await OpenExternalURL(url);
                } catch (err) {
                    console.error('Failed to open external URL:', err);
                    // 降级方案：如果是普通浏览器环境则直接打开
                    window.open(url, '_blank');
                }
            }
        });
    });

}

/**
 * 绑定工具栏按钮事件
 */
function bindToolbarButtons() {
    // 导入窗口按钮
    elements.importWindowsBtn?.addEventListener('click', async () => {
        debugLog('=== 导入窗口按钮被点击 ===');
        debugLog('按钮元素:', elements.importWindowsBtn);
        debugLog('点击事件开始执行...');

        try {
            await importChromeWindows();
            debugLog('importChromeWindows 执行完成');
            renderWindowTable();
            debugLog('renderWindowTable 执行完成');
            setupTableEventListeners();
            debugLog('setupTableEventListeners 执行完成');
            // 重置全选按钮状态
            if (window.updateSelectAllButtonText) {
                window.updateSelectAllButtonText();
            }
        } catch (error) {
            console.error('导入窗口按钮事件执行失败:', error);
        }
    });

    debugLog('导入窗口按钮事件已绑定:', !!elements.importWindowsBtn);

    // 调试：检查所有按钮元素是否存在
    debugLog('按钮元素检查:');
    debugLog('- configureAllBtn:', !!elements.configureAllBtn, elements.configureAllBtn);
    debugLog('- autoArrangeBtn:', !!elements.autoArrangeBtn, elements.autoArrangeBtn);
    debugLog('- customArrangeBtn:', !!elements.customArrangeBtn, elements.customArrangeBtn);

    // 全选按钮
    elements.configureAllBtn?.addEventListener('click', () => {
        debugLog('=== 全部选择按钮被点击 ===');

        const newState = toggleSelectAll();
        debugLog('全选状态切换为:', newState);

        // 更新按钮文字
        if (window.updateSelectAllButtonText) {
            window.updateSelectAllButtonText();
        }

        // 重新渲染窗口表格以更新复选框状态
        renderWindowTable();
        setupTableEventListeners();
        syncSelectedToBackend();
    });

    debugLog('全选按钮事件已绑定:', !!elements.configureAllBtn);

    // 自动排列按钮
    elements.autoArrangeBtn?.addEventListener('click', async () => {
        debugLog('=== 自动排列按钮被点击 ===');

        const selectedWindows = getSelectedWindows();
        if (selectedWindows.length === 0) {
            showError('请先选择要排列的窗口');
            return;
        }

        try {
            const windowIds = selectedWindows.map(w => w.id);
            debugLog('选中的窗口ID:', windowIds);

            // 获取当前选择的屏幕（从设置中获取）
            const currentSettings = await GetSettingsForFrontend();
            const selectedScreen = currentSettings?.ScreenSelection;

            if (selectedScreen) {
                // 调用在指定屏幕上排列的API（使用自动计算的参数）

                const screens = await GetScreensInfo();
                const screenIndex = screens.findIndex(s => s.name === selectedScreen);

                if (screenIndex >= 0) {
                    debugLog(`在屏幕 ${screenIndex} (${selectedScreen}) 上自动排列窗口`);

                    // 根据屏幕分辨率和窗口数量动态计算布局
                    const screen = screens[screenIndex];
                    const windowCount = selectedWindows.length;

                    // 计算最佳的列数（接近正方形布局）
                    const cols = Math.ceil(Math.sqrt(windowCount));
                    const rows = Math.ceil(windowCount / cols);

                    // 使用屏幕工作区计算窗口大小
                    const screenWidth = screen.workWidth;
                    const screenHeight = screen.workHeight;
                    const windowWidth = Math.floor(screenWidth / cols);
                    const windowHeight = Math.floor(screenHeight / rows);

                    debugLog(`屏幕分辨率: ${screenWidth}x${screenHeight}, 布局: ${rows}行x${cols}列, 窗口大小: ${windowWidth}x${windowHeight}`);

                    // 传递相对坐标（0, 0），让后端根据屏幕索引来添加偏移
                    const autoParams = {
                        startX: 0,
                        startY: 0,
                        width: windowWidth,
                        height: windowHeight,
                        horizontalSpacing: 0,
                        verticalSpacing: 0,
                        windowsPerRow: cols
                    };

                    await ArrangeWindowsOnScreen(windowIds, screenIndex, autoParams);
                    showSuccess(`成功在 ${selectedScreen} 上自动排列 ${selectedWindows.length} 个窗口 (${rows}行x${cols}列)`);
                } else {
                    // 找不到指定屏幕，使用默认排列
                    debugLog('找不到指定屏幕，使用默认自动排列');
                    await AutoArrangeWindows(windowIds);
                    showSuccess(`成功自动排列 ${selectedWindows.length} 个窗口`);
                }
            } else {
                // 没有指定屏幕，使用默认排列
                await AutoArrangeWindows(windowIds);
                showSuccess(`成功自动排列 ${selectedWindows.length} 个窗口`);
            }
        } catch (error) {
            console.error('自动排列失败:', error);
            showError('自动排列失败: ' + error.message);
        }
    });

    debugLog('自动排列按钮事件已绑定:', !!elements.autoArrangeBtn);

    // 自定义排列按钮
    elements.customArrangeBtn?.addEventListener('click', async () => {
        debugLog('=== 自定义排列按钮被点击 ===');

        const selectedWindows = getSelectedWindows();
        if (selectedWindows.length === 0) {
            showError('请先选择要排列的窗口');
            return;
        }

        try {
            const windowIds = selectedWindows.map(w => w.id);
            debugLog('选中的窗口ID:', windowIds);

            // 获取自定义排列参数
            const params = {
                startX: parseInt(elements.startX?.value || '0'),
                startY: parseInt(elements.startY?.value || '0'),
                width: parseInt(elements.width?.value || '600'),
                height: parseInt(elements.height?.value || '400'),
                horizontalSpacing: parseInt(elements.horizontalSpacing?.value || '0'),
                verticalSpacing: parseInt(elements.verticalSpacing?.value || '0'),
                windowsPerRow: parseInt(elements.windowsPerRow?.value || '5')
            };

            debugLog('自定义排列参数:', params);

            // 获取当前选择的屏幕（从设置中获取）
            const currentSettings = await GetSettingsForFrontend();
            const selectedScreen = currentSettings?.ScreenSelection;

            if (selectedScreen) {
                // 获取屏幕信息并调整参数

                const screens = await GetScreensInfo();
                const screen = screens.find(s => s.name === selectedScreen);

                if (screen) {
                    debugLog(`在屏幕 '${selectedScreen}' 上自定义排列窗口`);
                    // 将屏幕偏移量加到参数中
                    const adjustedParams = {
                        ...params,
                        startX: params.startX + screen.workLeft,
                        startY: params.startY + screen.workTop
                    };
                    await CustomArrangeWindows(windowIds, adjustedParams);
                    showSuccess(`成功在 ${selectedScreen} 上自定义排列 ${selectedWindows.length} 个窗口`);
                } else {
                    // 找不到指定屏幕，使用默认排列
                    debugLog('找不到指定屏幕，使用默认排列');
                    await CustomArrangeWindows(windowIds, params);
                    showSuccess(`成功自定义排列 ${selectedWindows.length} 个窗口`);
                }
            } else {
                // 没有指定屏幕，使用默认排列
                await CustomArrangeWindows(windowIds, params);
                showSuccess(`成功自定义排列 ${selectedWindows.length} 个窗口`);
            }
        } catch (error) {
            console.error('自定义排列失败:', error);
            showError('自定义排列失败: ' + error.message);
        }
    });

    debugLog('自定义排列按钮事件已绑定:', !!elements.customArrangeBtn);

    // 关闭窗口按钮
    elements.closeWindowsBtn?.addEventListener('click', async () => {
        debugLog('=== 关闭窗口按钮被点击 ===');

        const selectedWindows = getSelectedWindows();
        if (selectedWindows.length === 0) {
            showError('请先选择要关闭的窗口');
            return;
        }

        // 确认对话框
        const confirmed = await showConfirm(
            '确认关闭窗口',
            `确定要关闭选中的 ${selectedWindows.length} 个窗口吗？`
        );

        if (!confirmed) {
            return;
        }

        try {
            const windowIds = selectedWindows.map(w => w.id);
            debugLog('准备关闭的窗口ID:', windowIds);

            // 调用后端API关闭窗口（移除重复提示，只在完成后统一提示）

            await CloseWindows(windowIds);

            // 关闭完成后，立即刷新窗口列表
            try {
                // 在刷新前保存主控窗口ID（重要！因为importChromeWindows会清除它）
                const previousMasterWindowId = getState().masterWindowId;

                // 立即刷新窗口列表
                const importedWindows = await importChromeWindows(true);

                // 强制重新渲染界面
                renderWindowTable();
                setupTableEventListeners();

                // 如果全选按钮存在，更新其状态
                if (window.updateSelectAllButtonText) {
                    window.updateSelectAllButtonText();
                }

                // 检查主控窗口是否还存在，如果不存在则重置按钮（双保险）
                debugLog('🔍 检查主控窗口状态...');
                debugLog('当前同步状态 isSyncing:', isSyncing);
                debugLog('刷新前的主控窗口ID:', previousMasterWindowId);
                debugLog('刷新后的窗口列表:', importedWindows?.map(w => w.id));

                const currentMasterWindowId = getState().masterWindowId;
                debugLog('刷新后的主控窗口ID:', currentMasterWindowId);

                // 如果正在同步但刷新后没有主控窗口了，重置按钮
                if (isSyncing && !currentMasterWindowId) {
                    debugLog('⚠️ 同步运行中但无主控窗口，重置按钮状态...');
                    setTimeout(() => {
                        if (window.resetSyncButtonState) {
                            debugLog('✅ 执行按钮状态重置（无主控窗口）');
                            window.resetSyncButtonState();
                        }
                    }, 100);
                }
                // 如果之前有主控窗口，检查它是否还存在
                else if (previousMasterWindowId) {
                    const masterStillExists = importedWindows && importedWindows.some(w => w.id === previousMasterWindowId);
                    debugLog('主控窗口是否还存在:', masterStillExists);

                    if (!masterStillExists) {
                        debugLog('⚠️ 主控窗口已被关闭，准备重置按钮状态...');
                        setTimeout(() => {
                            if (window.resetSyncButtonState) {
                                debugLog('✅ 执行按钮状态重置（主控窗口被关闭）');
                                window.resetSyncButtonState();
                            }
                        }, 100);
                    } else {
                        debugLog('✓ 主控窗口仍然存在，保持同步状态');
                    }
                }
                // 如果没有主控窗口且窗口列表为空
                else if (!importedWindows || importedWindows.length === 0) {
                    debugLog('🔍 无主控窗口且窗口列表为空，重置按钮');
                    setTimeout(() => {
                        if (window.resetSyncButtonState) {
                            window.resetSyncButtonState();
                        }
                    }, 100);
                } else {
                    debugLog('ℹ️ 无主控窗口，但有窗口存在，且未在同步中，不重置按钮');
                }

                if (importedWindows && importedWindows.length > 0) {
                    showInfo(`窗口已关闭，列表已更新，当前有 ${importedWindows.length} 个活跃窗口`);
                } else {
                    showInfo('窗口已关闭，当前没有活跃窗口');
                }
            } catch (error) {
                console.error('刷新窗口列表时出错:', error);
                // 不显示错误提示，只在控制台记录，因为窗口关闭本身是成功的
                console.warn('窗口关闭成功，但列表刷新时遇到问题，这通常是正常的');

                // 如果刷新失败，至少显示一个成功的提示
                showInfo('✅ 窗口已关闭');
            }

        } catch (error) {
            console.error('关闭窗口失败:', error);
            showError('关闭窗口失败: ' + error.message);
        }
    });

    // 同步切换按钮
    let isSyncing = false;

    // 重置同步按钮状态的函数
    function resetSyncButtonState() {
        if (elements.syncToggleBtn) {
            isSyncing = false;
            elements.syncToggleBtn.innerHTML = '<span>开始同步</span>';
            elements.syncToggleBtn.classList.remove('secondary');
            elements.syncToggleBtn.style.backgroundColor = '';
            elements.syncToggleBtn.style.color = '';
        }
    }

    // 将重置函数暴露到window对象，以便其他地方调用
    window.resetSyncButtonState = resetSyncButtonState;

    // 事件处理函数
    window.onSyncStarted = (data) => {
        isSyncing = true;
        elements.syncToggleBtn.disabled = false;
        elements.syncToggleBtn.innerHTML = '<span>停止同步</span>';
        elements.syncToggleBtn.classList.add('secondary');
        elements.syncToggleBtn.style.backgroundColor = '#ff4444';
        elements.syncToggleBtn.style.color = 'white';

        // 更新主控窗口状态
        if (data && data.masterWindowId) {
            setMasterWindow(data.masterWindowId);
            renderWindowTable();
            setupTableEventListeners();
        }
    };

    window.onSyncStartFailed = (data) => {
        isSyncing = false;
        elements.syncToggleBtn.disabled = false;
        elements.syncToggleBtn.innerHTML = '<span>开始同步</span>';
        elements.syncToggleBtn.classList.remove('secondary');
        elements.syncToggleBtn.style.backgroundColor = '';
        elements.syncToggleBtn.style.color = '';
        const errorMessage = data?.error || '未知错误';
        showError(`同步启动失败: ${errorMessage}`);
        if (isAccessibilityPermissionErrorMessage(errorMessage)) {
            showAccessibilityPermissionAssistModal({
                reason: '同步功能需要监听鼠标和键盘事件，因此必须先给 ChromeManager.app 辅助功能权限，并建议同时给“输入监控”权限。如果您之前添加过权限但重新打包了程序，请在权限界面点击“-”号按钮删除之前的程序，重新添加新打包的程序。',
                showSkipForever: false
            });
        }
    };

    window.onSyncStopped = () => {
        isSyncing = false;
        elements.syncToggleBtn.disabled = false;
        elements.syncToggleBtn.innerHTML = '<span>开始同步</span>';
        elements.syncToggleBtn.classList.remove('secondary');
        elements.syncToggleBtn.style.backgroundColor = '';
        elements.syncToggleBtn.style.color = '';
    };

    window.onSyncStopFailed = (data) => {
        showError(`同步停止失败: ${data?.error || '未知错误'}`);
        // 保持按钮状态不变，因为同步可能仍在运行
    };

    // 同步切换按钮 - 乐观UI更新
    elements.syncToggleBtn?.addEventListener('click', async () => {
        try {
            if (!isSyncing) {
                // 开始同步
                const state = getState();

                if (state.windows.length === 0) {
                    showNotification('请先导入Chrome窗口', 'error');
                    return;
                }

                const selectedWindows = state.windows.filter(w => w.selected);
                if (selectedWindows.length === 0) {
                    showNotification('请至少选择一个窗口', 'error');
                    return;
                }

                let masterWindowId = state.masterWindowId || 0;
                let slaveWindowNumbers = [];

                if (masterWindowId > 0) {
                    const slaveWindows = selectedWindows.filter(w => w.id !== masterWindowId);
                    slaveWindowNumbers = slaveWindows.map(w => w.id);
                } else {
                    slaveWindowNumbers = selectedWindows.map(w => w.id);
                }

                // 乐观UI更新 - 立即更新按钮状态
                isSyncing = true;
                elements.syncToggleBtn.disabled = true;
                elements.syncToggleBtn.innerHTML = '<span>正在启动</span>';

                // 调用后端API（不等待执行完成）

                StartSync(masterWindowId, slaveWindowNumbers).catch(err => {
                    // API调用失败（网络错误等）
                    isSyncing = false;
                    elements.syncToggleBtn.disabled = false;
                    elements.syncToggleBtn.innerHTML = '<span>开始同步</span>';
                    showError(`调用失败: ${err.message}`);
                });

            } else {
                // 停止同步 - 乐观UI更新
                isSyncing = false;
                elements.syncToggleBtn.disabled = true;
                elements.syncToggleBtn.innerHTML = '<span>正在停止</span>';

                // 调用后端API（不等待执行完成）

                StopSync().catch(err => {
                    // API调用失败
                    isSyncing = true;
                    elements.syncToggleBtn.disabled = false;
                    elements.syncToggleBtn.innerHTML = '<span>停止同步</span>';
                    showError(`调用失败: ${err.message}`);
                });
            }

        } catch (error) {
            console.error('同步操作失败:', error);
            showNotification(`同步操作失败: ${error.message}`, 'error');
        }
    });
}

/**
 * 绑定表单事件
 */
function bindFormEvents() {
    // 窗口配置字段
    const configFields = ['startX', 'startY', 'width', 'height', 'horizontalSpacing', 'verticalSpacing', 'windowsPerRow'];

    configFields.forEach(field => {
        const element = elements[field];
        if (element) {
            element.addEventListener('change', (e) => {
                updateWindowConfig({ [field]: e.target.value });
            });
        }
    });

    // 窗口范围输入
    elements.windowRange?.addEventListener('change', (e) => {
        setWindowRange(e.target.value);
    });
}

/**
 * 渲染窗口表格
 */
function renderWindowTable() {
    debugLog('=== renderWindowTable 被调用 ===');
    const content = elements.windowTableContent;
    if (!content) {
        debugLog('windowTableContent 元素不存在');
        return;
    }

    const state = getState();
    debugLog('当前状态:', state);
    debugLog('窗口列表:', state.windows);
    debugLog('窗口数量:', state.windows.length);

    content.innerHTML = '';

    // 如果没有窗口数据，保持空白
    if (state.windows.length === 0) {
        debugLog('窗口列表为空，不渲染任何内容');
        return;
    }

    debugLog('开始渲染窗口列表...');
    state.windows.forEach((window, index) => {
        debugLog(`渲染窗口 ${index + 1}:`, window);
        const row = document.createElement('div');
        row.className = 'window-row';

        // 简化显示，只保留基本信息
        const windowNumber = window.number || window.id;

        row.innerHTML = `
            <div class="col-select">
                <input type="checkbox" ${window.selected ? 'checked' : ''} data-window-id="${window.id}">
            </div>
            <div class="col-separator">|</div>
            <div class="col-number">
                <div class="window-number">${windowNumber}</div>
            </div>
            <div class="col-separator">|</div>
            <div class="col-title">
                <div class="window-title">${window.title}</div>
            </div>
            <div class="col-separator">|</div>
            <div class="col-master">
                <input type="radio" name="master-window" value="${window.id}"
                       ${state.masterWindowId === window.id ? 'checked' : ''}
                       class="master-radio" data-window-id="${window.id}">
            </div>
        `;
        content.appendChild(row);
    });
}

// 同步选中状态到后端供 OpenClaw API 使用
async function syncSelectedToBackend() {
    try {

        const state = getState();
        const selectedIds = state.windows.filter(w => w.selected).map(w => w.id);
        await UpdateSelectedWindows(selectedIds);
    } catch (e) {
        console.error('API 同步选中状态失败:', e);
    }
}

/**
 * 设置表格事件监听器
 */
function setupTableEventListeners() {
    // 选择框事件
    const checkboxes = document.querySelectorAll('.table-content input[type="checkbox"]');
    checkboxes.forEach(checkbox => {
        checkbox.addEventListener('change', (e) => {
            const windowId = parseInt(e.target.dataset.windowId);
            toggleWindowSelection(windowId);
            renderWindowTable();
            setupTableEventListeners();
            syncSelectedToBackend();
        });
    });

    // 主控窗口选择事件
    const radioButtons = document.querySelectorAll('.table-content input[type="radio"].master-radio');
    radioButtons.forEach(radio => {
        radio.addEventListener('change', async (e) => {
            if (e.target.checked) {
                const windowId = parseInt(e.target.dataset.windowId);
                debugLog(`用户选择窗口 ${windowId} 为主控窗口`);

                // 获取当前的主控窗口ID
                const currentMasterWindow = getMasterWindow();
                const currentMasterWindowId = currentMasterWindow ? currentMasterWindow.id : null;

                debugLog(`当前主控窗口: ${currentMasterWindowId}, 新主控窗口: ${windowId}`);

                try {
                    // 调用后端API设置主控窗口样式

                    await SetMasterWindow(windowId);
                    debugLog(`成功设置窗口 ${windowId} 为主控窗口`);

                    // 更新前端状态
                    const setResult = setMasterWindow(windowId);
                    debugLog(`setMasterWindow(${windowId}) 返回:`, setResult);

                    // 验证状态是否正确设置
                    const state = getState();
                    debugLog('设置主控窗口后的状态:', {
                        masterWindowId: state.masterWindowId,
                        masterWindow: getMasterWindow()
                    });

                    // 重置同步按钮状态（因为后端可能已经停止了同步）
                    if (currentMasterWindowId && currentMasterWindowId !== windowId) {
                        debugLog('检测到主控窗口切换，重置同步按钮状态');
                        resetSyncButtonState();
                    }

                    renderWindowTable();
                    setupTableEventListeners();

                    showNotification(`窗口 ${windowId} 已设为主控窗口`, 'success');
                } catch (error) {
                    console.error(`设置主控窗口失败:`, error);
                    showNotification(`设置主控窗口失败: ${error.message}`, 'error');

                    // 恢复单选框状态
                    e.target.checked = false;
                    renderWindowTable();
                    setupTableEventListeners();
                }
            }
        });
    });
}

/**
 * 初始化自定义URL选择器
 */
async function initCustomUrlSelect() {
    const urlSelect = elements.customUrlSelect;
    if (!urlSelect) return;

    try {
        // 从后端获取预设网址
        const presetUrls = await GetPresetURLs();

        urlSelect.innerHTML = '<option value="">选择预设网址</option>';

        // 添加预设网址选项
        for (const [name, url] of Object.entries(presetUrls)) {
            const option = document.createElement('option');
            option.value = url;
            option.textContent = name;
            urlSelect.appendChild(option);
        }

        debugLog('预设网址加载完成，共', Object.keys(presetUrls).length, '个');
    } catch (error) {
        console.error('加载预设网址失败:', error);

        // 失败时使用默认网址
        const defaultUrls = [
            { value: 'https://www.google.com', text: 'Google' },
            { value: 'https://www.baidu.com', text: '百度' },
            { value: 'https://www.github.com', text: 'GitHub' },
            { value: 'https://www.youtube.com', text: 'YouTube' },
            { value: 'https://www.bilibili.com', text: 'Bilibili' },
        ];

        urlSelect.innerHTML = '<option value="">选择预设网址</option>';
        defaultUrls.forEach(url => {
            const option = document.createElement('option');
            option.value = url.value;
            option.textContent = url.text;
            urlSelect.appendChild(option);
        });
    }
}

/**
 * 初始化分组选择器
 */
async function refreshBrowserCacheGroupSelect() {
    const select = document.getElementById('browser-cache-group-select');
    if (!select) {
        return;
    }

    try {
        const groups = await getGroups();
        const settings = getCurrentSettings();

        select.innerHTML = '';

        const defaultOption = document.createElement('option');
        defaultOption.value = '默认分组';
        defaultOption.textContent = '默认分组';
        select.appendChild(defaultOption);

        if (groups && groups.length > 0) {
            groups.forEach(group => {
                if (group.name !== '默认分组' && group.name !== '') {
                    const option = document.createElement('option');
                    option.value = group.name;
                    option.textContent = group.name;
                    select.appendChild(option);
                }
            });
        }

        setGroupSelectValue(select, getPreferredGroupSelectValue(settings));
    } catch (error) {
        console.error('Failed to initialize browser cache group select:', error);
        select.innerHTML = '<option value="默认分组">默认分组</option>';
    }
}

function getPreferredGroupSelectValue(settings) {
    const groupName = settings?.CurrentGroup?.trim();
    if (!groupName) {
        return '默认分组';
    }
    return groupName;
}

function setGroupSelectValue(select, groupName) {
    if (!select) {
        return;
    }

    const normalizedGroupName = groupName && groupName.trim() ? groupName.trim() : '默认分组';
    const hasOption = Array.from(select.options).some(option => option.value === normalizedGroupName);
    if (hasOption) {
        select.value = normalizedGroupName;
        return;
    }

    if (Array.from(select.options).some(option => option.value === '默认分组')) {
        select.value = '默认分组';
    }
}

function syncGroupSelects(groupName) {
    const normalizedGroupName = groupName && groupName.trim() ? groupName.trim() : '默认分组';
    const selectIds = [
        'open-window-group-select',
        'batch-env-group-select',
        'browser-cache-group-select',
    ];

    selectIds.forEach(id => {
        const select = document.getElementById(id);
        setGroupSelectValue(select, normalizedGroupName);
    });
}

async function initializeGroupSelects() {
    try {
        const groups = await getGroups();
        const settings = getCurrentSettings();

        // 初始化"打开窗口"页面的分组选择器
        const openWindowGroupSelect = document.getElementById('open-window-group-select');
        if (openWindowGroupSelect) {
            openWindowGroupSelect.innerHTML = '';

            // 首先添加默认分组选项
            const defaultOption = document.createElement('option');
            defaultOption.value = '默认分组';
            defaultOption.textContent = '默认分组';
            openWindowGroupSelect.appendChild(defaultOption);

            // 然后添加自定义分组（过滤掉名为"默认分组"的项，避免重复）
            if (groups && groups.length > 0) {
                groups.forEach(group => {
                    if (group.name !== '默认分组' && group.name !== '') {
                        const option = document.createElement('option');
                        option.value = group.name;
                        option.textContent = group.name;
                        openWindowGroupSelect.appendChild(option);
                    }
                });
            }

            // 设置当前选中的分组
            setGroupSelectValue(openWindowGroupSelect, getPreferredGroupSelectValue(settings));

            // 添加分组变更事件监听器 (确保只添加一次)
            if (!openWindowGroupSelect.dataset.listenerAdded) {
                openWindowGroupSelect.addEventListener('change', async (e) => {
                    const selectedGroup = e.target.value;
                    if (selectedGroup) {
                        try {
                            await setCurrentGroup(selectedGroup);
                            syncGroupSelects(selectedGroup);
                            debugLog('打开窗口页面切换到分组:', selectedGroup);
                        } catch (error) {
                            console.error('切换分组失败:', error);
                        }
                    }
                });
                openWindowGroupSelect.dataset.listenerAdded = 'true';
            }
        }

        // 初始化"批量创建环境"页面的分组选择器
        const batchEnvGroupSelect = document.getElementById('batch-env-group-select');
        if (batchEnvGroupSelect) {
            batchEnvGroupSelect.innerHTML = '';

            // 首先添加默认分组选项
            const defaultOption = document.createElement('option');
            defaultOption.value = '默认分组';
            defaultOption.textContent = '默认分组';
            batchEnvGroupSelect.appendChild(defaultOption);

            // 然后添加自定义分组（过滤掉名为"默认分组"的项，避免重复）
            if (groups && groups.length > 0) {
                groups.forEach(group => {
                    if (group.name !== '默认分组' && group.name !== '') {
                        const option = document.createElement('option');
                        option.value = group.name;
                        option.textContent = group.name;
                        batchEnvGroupSelect.appendChild(option);
                    }
                });
            }

            // 设置当前选中的分组
            setGroupSelectValue(batchEnvGroupSelect, getPreferredGroupSelectValue(settings));

            // 添加分组变更事件监听器 (确保只添加一次)
            if (!batchEnvGroupSelect.dataset.listenerAdded) {
                batchEnvGroupSelect.addEventListener('change', async (e) => {
                    const selectedGroup = e.target.value;
                    if (selectedGroup) {
                        try {
                            await setCurrentGroup(selectedGroup);
                            syncGroupSelects(selectedGroup);
                            debugLog('批量创建环境页面切换到分组:', selectedGroup);
                        } catch (error) {
                            console.error('切换分组失败:', error);
                        }
                    }
                });
                batchEnvGroupSelect.dataset.listenerAdded = 'true';
            }
        }

        await refreshBrowserCacheGroupSelect();
        debugLog('分组选择器初始化完成，共', groups ? groups.length : 0, '个分组');
    } catch (error) {
        console.error('初始化分组选择器失败:', error);

        // 失败时设置默认选项
        const defaultOption = '<option value="默认分组">默认分组</option>';

        const openWindowGroupSelect = document.getElementById('open-window-group-select');
        if (openWindowGroupSelect) {
            openWindowGroupSelect.innerHTML = defaultOption;
        }

        const batchEnvGroupSelect = document.getElementById('batch-env-group-select');
        if (batchEnvGroupSelect) {
            batchEnvGroupSelect.innerHTML = defaultOption;
        }

        const browserCacheGroupSelect = document.getElementById('browser-cache-group-select');
        if (browserCacheGroupSelect) {
            browserCacheGroupSelect.innerHTML = defaultOption;
        }
    }
}

/**
 * 更新表单值
 */
function updateFormValues() {
    const state = getState();
    const settings = getCurrentSettings();

    // 更新窗口配置表单（使用保存的自定义排列参数）
    const customParams = settings?.CustomArrangeParams; // 后端返回的字段名是首字母大写
    if (customParams) {
        // 如果有保存的自定义排列参数，使用它们
        if (elements.startX) elements.startX.value = customParams.startX || 0;
        if (elements.startY) elements.startY.value = customParams.startY || 0;
        if (elements.width) elements.width.value = customParams.width || 500; // 默认500
        if (elements.height) elements.height.value = customParams.height || 400;
        if (elements.horizontalSpacing) elements.horizontalSpacing.value = customParams.horizontalSpacing || 0;
        if (elements.verticalSpacing) elements.verticalSpacing.value = customParams.verticalSpacing || 0;
        if (elements.windowsPerRow) elements.windowsPerRow.value = customParams.windowsPerRow || 5;
        debugLog('已加载保存的自定义排列参数:', customParams);
    } else {
        // 如果没有保存的参数，使用默认值
        if (elements.startX) elements.startX.value = state.windowConfig.startX;
        if (elements.startY) elements.startY.value = state.windowConfig.startY;
        if (elements.width) elements.width.value = 500; // 默认宽度改为500
        if (elements.height) elements.height.value = state.windowConfig.height;
        if (elements.horizontalSpacing) elements.horizontalSpacing.value = state.windowConfig.horizontalSpacing;
        if (elements.verticalSpacing) elements.verticalSpacing.value = state.windowConfig.verticalSpacing;
        if (elements.windowsPerRow) elements.windowsPerRow.value = state.windowConfig.windowsPerRow;
        debugLog('使用默认自定义排列参数');
    }

    // 更新窗口范围（使用最后使用的编号）
    if (elements.windowRange) {
        // 使用LastWindowNumbers字段显示用户上次输入的参数
        const lastWindowNumbers = settings?.LastWindowNumbers || '';
        elements.windowRange.value = lastWindowNumbers; // 如果为空则显示空字符串
        debugLog('最后使用的窗口编号:', lastWindowNumbers);
    }

    // 更新环境创建参数（使用最后使用的编号）
    if (elements.envNumbers) {
        // 使用LastEnvCreationNumbers字段显示用户上次输入的参数
        const lastEnvCreationNumbers = settings?.LastEnvCreationNumbers || '';
        elements.envNumbers.value = lastEnvCreationNumbers; // 如果为空则显示空字符串
        debugLog('🔍 环境创建参数调试信息:');
        debugLog('  - settings对象:', settings);
        debugLog('  - LastEnvCreationNumbers字段:', settings?.LastEnvCreationNumbers);
        debugLog('  - 最终使用的值:', lastEnvCreationNumbers);
        debugLog('  - envNumbers元素:', elements.envNumbers);
        debugLog('  - 设置后的值:', elements.envNumbers.value);
    }
}

/**
 * 显示设置模态对话框
 */
async function showSettingsModal() {
    // 创建模态框HTML
    // 创建模态框HTML -- Refactored for Grid Layout
    const modalHTML = `
        <div class="settings-modal-overlay" id="settingsModalOverlay">
            <div class="settings-modal">
                <!-- 设置窗口标题栏 -->
                <div class="settings-titlebar">
                    <div class="titlebar-content">
                        <h1 class="settings-title">ChromeManager 4.0 - 设置</h1>
                        <button class="modal-close-btn" id="modalCloseBtn">×</button>
                    </div>
                </div>
                
                <!-- 设置内容 -->
                <div class="settings-content">
                    <!-- 默认路径设置 -->
                    <div class="settings-section">
                        <h2 class="section-title">核心路径</h2>
                        


                        <div class="setting-row">
                            <label class="setting-label">快捷方式路径</label>
                            <div class="setting-control">
                                <input type="text" id="modalShortcutPath" class="setting-input" placeholder="Chrome快捷方式存放目录" autocomplete="off">
                                <button class="browse-btn" id="modalBrowseShortcut">浏览...</button>
                            </div>
                        </div>
                        
                        <div class="setting-row">
                            <label class="setting-label">缓存目录</label>
                            <div class="setting-control">
                                <input type="text" id="modalCacheDir" class="setting-input" placeholder="应用数据缓存目录" autocomplete="off">
                                <button class="browse-btn" id="modalBrowseCache">浏览...</button>
                            </div>
                        </div>

                        <div class="setting-row">
                            <label class="setting-label">浏览器路径</label>
                            <div class="setting-control">
                                <input type="text" id="modalChromePath" class="setting-input" placeholder="自动检测 (可选)" autocomplete="off">
                                <button class="browse-btn" id="modalBrowseChrome">浏览...</button>
                            </div>
                        </div>
                    </div>

                    <!-- 分组管理 (Moved Up) -->
                    <div class="settings-section">
                        <h2 class="section-title">分组管理</h2>
                        <div class="setting-row">
                            <label class="setting-label">管理操作</label>
                            <div class="setting-control button-row-left" style="gap: 8px;">
                                <button class="setting-btn primary" id="modalAddGroup">新建分组</button>
                                <select id="modalEditGroupSelect" class="setting-select" style="width: 150px;">
                                    <option value="">选择分组...</option>
                                </select>
                                <button class="setting-btn secondary" id="modalEditGroup" disabled>编辑</button>
                                <button class="setting-btn secondary" id="modalDeleteGroup" disabled>删除</button>
                            </div>
                        </div>
                    </div>

                    <!-- 屏幕与窗口 -->
                    <div class="settings-section">
                        <h2 class="section-title">屏幕与窗口</h2>
                        <div class="setting-row">
                            <label class="setting-label">目标屏幕</label>
                            <div class="setting-control">
                                <select id="modalScreenSelection" class="setting-select">
                                    <option value="">加载中...</option>
                                </select>
                                <button class="browse-btn" id="modalRefreshScreens">刷新数据</button>
                            </div>
                        </div>

                        <div class="setting-row">
                            <label class="setting-label">窗口打开间隔</label>
                            <div class="setting-control">
                                <input type="range" id="modalWindowSpeed" min="0.1" max="3.0" step="0.1" value="0.6" class="setting-slider">
                                <span id="modalSpeedValue" class="speed-value" style="min-width: 60px; text-align: right;">0.6秒</span>
                            </div>
                        </div>
                    </div>

                    <!-- 行为习惯 -->
                    <div class="settings-section">
                        <h2 class="section-title">行为习惯</h2>
                        <div class="setting-row">
                            <label class="setting-label">同步快捷键</label>
                            <div class="setting-control hotkey-control">
                                <input type="text" id="modalSyncHotkey" class="setting-input" placeholder="点击设置组合键..." autocomplete="off" readonly>
                                <button class="setting-btn secondary" id="modalClearHotkey" style="margin-left: 8px;">清除</button>
                            </div>
                        </div>
                    </div>

                    <!-- 图标管理 -->
                    <div class="settings-section">
                        <h2 class="section-title">图标管理</h2>
                        <div class="setting-row">
                            <label class="setting-label">自动修饰</label>
                            <div class="setting-control" style="justify-content: flex-start;">
                                <label class="checkbox-label">
                                    <input type="checkbox" id="modalAutoModifyIcon">
                                    <span>创建窗口时自动修改窗口图标（带窗口序号）</span>
                                </label>
                            </div>
                        </div>
                        <div class="setting-row">
                             <label class="setting-label">维护操作</label>
                             <div class="setting-control button-row-left">
                                 <button class="setting-btn secondary" id="modalRestoreIcons">还原默认图标</button>
                             </div>
                        </div>
                    </div>

                    <!-- OpenClaw API 集成 -->
                    <div class="settings-section">
                        <h2 class="section-title">OpenClaw API 集成</h2>
                        <div class="setting-row">
                            <label class="setting-label">API 状态</label>
                            <div class="setting-control" style="justify-content: flex-start;">
                                <label class="checkbox-label">
                                    <input type="checkbox" id="modalEnableAPI">
                                    <span class="checkbox-text">启用后台通信 API</span>
                                </label>
                            </div>
                        </div>
                        <div class="setting-row">
                            <label class="setting-label">通信端口</label>
                            <div class="setting-control">
                                <input type="number" class="setting-input" id="modalAPIPort" min="1024" max="65535" placeholder="18923" style="width: 120px;">
                            </div>
                        </div>
                        <div class="setting-row">
                            <label class="setting-label">API Token</label>
                            <div class="setting-control hotkey-control" style="flex: 1; justify-content: flex-start; gap: 8px;">
                                <input type="text" class="setting-input" id="modalAPIToken" placeholder="点击右侧生成按钮" style="width: 280px;" readonly>
                                <button class="setting-btn secondary" id="modalGenerateToken" style="padding: 0 12px; height: 32px; flex-shrink: 0; min-width: max-content; margin-top: 0; white-space: nowrap;">生成新Token</button>
                            </div>
                        </div>
                    </div>

                    <div class="settings-section">
                        <h2 class="section-title">扩展修复</h2>
                        <div style="font-size: 12px; color: #999; line-height: 1.6; margin: -2px 0 12px 0;">
                            主要针对浏览器插件损坏，但不方便使用修复功能重置插件的情况，用此方法可保留插件数据。
                        </div>
                        <div class="setting-row">
                            <label class="setting-label">受损窗口编号</label>
                            <div class="setting-control">
                                <input type="text" id="modalRepairTargetNumbers" class="setting-input" style="max-width: 160px;" placeholder="例如 7,9 或 7-10" autocomplete="off">
                            </div>
                        </div>
                        <div class="setting-row">
                            <label class="setting-label">克隆环境编号</label>
                            <div class="setting-control">
                                <input type="number" id="modalRepairDonorNumber" class="setting-input" style="max-width: 160px;" placeholder="例如 2" autocomplete="off">
                                <button class="setting-btn secondary" id="modalRepairExtensions">修复</button>
                            </div>
                        </div>
                        <div style="font-size: 12px; color: #999; line-height: 1.6; margin-top: 10px;">
                            <span style="display: block;">
                                在当前分组内复制克隆环境的扩展安装文件到受损环境。修复完成后，请前往 Chrome 应用商店重新安装官方插件即可恢复显示，原有插件数据会尽量保留。
                            </span>
                        </div>
                    </div>

                    <div class="settings-section">
                        <h2 class="section-title">辅助功能权限</h2>
                        <div style="font-size: 12px; color: #999; line-height: 1.6; margin: -2px 0 12px 0;">
                            导入窗口和窗口控制需要在“辅助功能”中允许 <code>ChromeManager.app</code>；使用键盘同步时，还需要在“输入监控”中允许 <code>ChromeManager.app</code>。
                        </div>
                        <div class="setting-row">
                            <label class="setting-label">权限入口</label>
                            <div class="setting-control button-row-left" style="gap: 8px;">
                                <button class="setting-btn secondary permission-action-btn" id="modalOpenAccessibilitySettings">打开辅助功能设置</button>
                                <button class="setting-btn secondary permission-action-btn" id="modalOpenInputMonitoringSettings">打开键盘输入设置</button>
                                <button class="setting-btn secondary permission-action-btn" id="modalRevealChromeManagerApp">定位 ChromeManager.app</button>
                                <button class="setting-btn secondary permission-action-btn" id="modalRevealTerminalApp">定位 终端.app</button>
                            </div>
                        </div>
                    </div>

                    <!-- 数据维护 -->
                    <div class="settings-section" style="border-bottom: none;">
                        <h2 class="section-title">数据维护</h2>
                         <div class="setting-row">
                             <label class="setting-label">配置操作</label>
                             <div class="setting-control button-row-left">
                                <button class="setting-btn secondary" id="modalExportSettings">导出配置</button>
                                <button class="setting-btn secondary" id="modalImportSettings">导入配置</button>
                                <button class="setting-btn gray" id="modalClearAllData" style="margin-left: auto;">重置所有数据</button>
                            </div>
                        </div>
                    </div>

                    <!-- 底部操作栏 -->
                    <div class="settings-actions">
                        <button class="setting-btn gray" id="modalCancelSettings">取消</button>
                        <button class="setting-btn primary" id="modalSaveSettings" style="min-width: 100px;">保存修改</button>
                    </div>
                </div>
            </div>
        </div>
    `;

    // 添加模态框到页面
    document.body.insertAdjacentHTML('beforeend', modalHTML);

    // 获取模态框元素
    const overlay = document.getElementById('settingsModalOverlay');
    const modalCloseBtn = document.getElementById('modalCloseBtn');
    const cancelBtn = document.getElementById('modalCancelSettings');
    const saveBtn = document.getElementById('modalSaveSettings');
    const speedSlider = document.getElementById('modalWindowSpeed');
    const speedValue = document.getElementById('modalSpeedValue');
    const syncHotkeyInput = document.getElementById('modalSyncHotkey');
    const clearHotkeyBtn = document.getElementById('modalClearHotkey');

    const normalizeHotkeyKey = (event) => {
        const key = event.key;
        if (!key) return '';
        const keyMap = {
            ' ': 'Space',
            'Spacebar': 'Space',
            'Space': 'Space',
            'ArrowUp': 'Up',
            'ArrowDown': 'Down',
            'ArrowLeft': 'Left',
            'ArrowRight': 'Right',
            'Escape': 'Esc',
            'Esc': 'Esc',
            'Enter': 'Enter',
            'Return': 'Enter',
            'Tab': 'Tab',
            'Backspace': 'Backspace',
            'Delete': 'Delete'
        };
        if (keyMap[key]) return keyMap[key];
        const code = event.code || '';
        if (keyMap[code]) return keyMap[code];
        if (/^F\d{1,2}$/i.test(key)) return key.toUpperCase();
        if (/^Digit\d$/.test(code)) return code.replace('Digit', '');
        if (/^Key[A-Z]$/.test(code)) return code.replace('Key', '');
        const upper = key.toUpperCase();
        if (upper === 'META') {
            return /Mac|iPod|iPhone|iPad/.test(navigator.platform) ? 'Cmd' : 'Win';
        }
        if (upper === 'CONTROL') return 'Ctrl';
        if (upper === 'ALT') return 'Alt';
        if (upper === 'SHIFT') return 'Shift';
        if (key.length === 1) return upper;
        return key;
    };

    const buildHotkeyString = (event) => {
        const modifiers = [];
        if (event.ctrlKey || event.key === 'Control') modifiers.push('Ctrl');
        if (event.altKey || event.key === 'Alt') modifiers.push('Alt');
        if (event.shiftKey || event.key === 'Shift') modifiers.push('Shift');
        
        const isMac = /Mac|iPod|iPhone|iPad/.test(navigator.platform);
        const metaStr = isMac ? 'Cmd' : 'Win';
        if (event.metaKey || event.key === 'Meta') modifiers.push(metaStr);
        
        const mainKey = normalizeHotkeyKey(event);
        
        // Prevent doubling up if the main key is literally the modifier key
        if (!mainKey || ['Ctrl', 'Alt', 'Shift', 'Win', 'Cmd'].includes(mainKey)) {
            return modifiers.length ? modifiers.join('+') : '';
        }
        modifiers.push(mainKey);
        return modifiers.join('+');
    };

    let recordingHotkey = false;

    if (syncHotkeyInput) {
        syncHotkeyInput.addEventListener('focus', () => {
            recordingHotkey = true;
        });

        syncHotkeyInput.addEventListener('blur', () => {
            recordingHotkey = false;
        });

        syncHotkeyInput.addEventListener('keydown', (event) => {
            event.preventDefault();
            if (!recordingHotkey) {
                recordingHotkey = true;
            }

            if (event.key === 'Escape') {
                syncHotkeyInput.blur();
                return;
            }

            if (event.key === 'Backspace' || event.key === 'Delete') {
                syncHotkeyInput.value = '';
                syncHotkeyInput.dataset.hotkey = '';
                return;
            }

            const combo = buildHotkeyString(event);
            if (combo) {
                const parts = combo.split('+');
                const lastPart = parts[parts.length - 1];
                if (!['Ctrl', 'Alt', 'Shift', 'Win', 'Cmd'].includes(lastPart)) {
                    syncHotkeyInput.value = combo;
                    syncHotkeyInput.dataset.hotkey = combo;
                }
            }
        });
    }

    if (clearHotkeyBtn && syncHotkeyInput) {
        clearHotkeyBtn.addEventListener('click', () => {
            syncHotkeyInput.value = '';
            syncHotkeyInput.dataset.hotkey = '';
            syncHotkeyInput.blur();
        });
    }


    // 等待DOM完全渲染后加载设置 - 使用多重延迟确保成功
    const loadSettingsWithRetry = async (retryCount = 0) => {
        try {
            debugLog(`Attempt ${retryCount + 1}: Loading settings into modal...`);

            const settings = await GetSettingsForFrontend();

            debugLog('Backend settings data:', settings);

            const shortcutInput = document.getElementById('modalShortcutPath');
            const cacheInput = document.getElementById('modalCacheDir');
            const chromePathInput = document.getElementById('modalChromePath');
            const screenSelect = document.getElementById('modalScreenSelection');
            const speedSlider = document.getElementById('modalWindowSpeed');
            const speedValue = document.getElementById('modalSpeedValue');
            const closeBehaviorStatus = document.getElementById('modalCloseBehaviorStatus');
            const syncHotkeyInput = document.getElementById('modalSyncHotkey');
            const clearHotkeyBtn = document.getElementById('modalClearHotkey');

            debugLog('DOM elements check:', {
                shortcutInput: !!shortcutInput,
                cacheInput: !!cacheInput,
                screenSelect: !!screenSelect,
                modalExists: !!document.getElementById('settingsModalOverlay')
            });

            if ((!shortcutInput || !cacheInput) && retryCount < 3) {
                debugLog(`Elements not ready, retrying in ${500 * (retryCount + 1)}ms...`);
                setTimeout(() => loadSettingsWithRetry(retryCount + 1), 500 * (retryCount + 1));
                return;
            }

            if (settings && shortcutInput && cacheInput) {
                // 设置实际值
                if (settings.ShortcutPath) {
                    shortcutInput.value = settings.ShortcutPath;
                    shortcutInput.placeholder = `当前: ${settings.ShortcutPath}`;
                    debugLog('Set shortcut path:', settings.ShortcutPath);
                }

                if (settings.CacheDir) {
                    cacheInput.value = settings.CacheDir;
                    cacheInput.placeholder = `当前: ${settings.CacheDir}`;
                    debugLog('Set cache dir:', settings.CacheDir);
                }

                if (settings.ChromePath && chromePathInput) {
                    chromePathInput.value = settings.ChromePath;
                    chromePathInput.placeholder = `当前: ${settings.ChromePath}`;
                }



                if (syncHotkeyInput) {
                    let hotkeyValue = (settings.SyncToggleHotkey || '').trim();
                    if (/Mac|iPod|iPhone|iPad/.test(navigator.platform)) {
                        hotkeyValue = hotkeyValue.replace(/Win/g, 'Cmd');
                    }
                    syncHotkeyInput.value = hotkeyValue;
                    syncHotkeyInput.dataset.hotkey = hotkeyValue;
                }

                const iconCheckbox = document.getElementById('modalAutoModifyIcon');
                if (iconCheckbox) {
                    iconCheckbox.checked = settings.AutoModifyShortcutIcon !== false;
                }

                const apiCheckbox = document.getElementById('modalEnableAPI');
                if (apiCheckbox) {
                    apiCheckbox.checked = settings.EnableAPI === true;
                }
                const apiPortInput = document.getElementById('modalAPIPort');
                if (apiPortInput) {
                    apiPortInput.value = settings.APIPort || 18923;
                }
                const apiTokenInput = document.getElementById('modalAPIToken');
                if (apiTokenInput) {
                    apiTokenInput.value = settings.APIToken || '';
                }
                const genTokenBtn = document.getElementById('modalGenerateToken');
                if (genTokenBtn && apiTokenInput) {
                    genTokenBtn.onclick = () => {
                        apiTokenInput.value = crypto.randomUUID();
                    };
                }
                const repairExtensionsBtn = document.getElementById('modalRepairExtensions');
                const repairTargetNumbersInput = document.getElementById('modalRepairTargetNumbers');
                const repairDonorNumberInput = document.getElementById('modalRepairDonorNumber');
                if (repairExtensionsBtn && repairTargetNumbersInput && repairDonorNumberInput) {
                    repairExtensionsBtn.addEventListener('click', async () => {
                        const targetNumbers = repairTargetNumbersInput.value.trim();
                        const donorNumber = parseInt(repairDonorNumberInput.value, 10);

                        if (!targetNumbers) {
                            showError('请输入受损窗口编号');
                            return;
                        }

                        if (!Number.isInteger(donorNumber) || donorNumber <= 0) {
                            showError('请输入有效的克隆环境编号');
                            return;
                        }

                        const originalText = repairExtensionsBtn.textContent;
                        repairExtensionsBtn.disabled = true;
                        repairExtensionsBtn.textContent = '修复中...';

                        try {
                            const repairResult = await RepairExtensionsFromDonor(targetNumbers, donorNumber);
                            const repairedTargets = repairResult?.repairedTargets || 0;
                            const skippedRunning = repairResult?.skippedRunning || 0;
                            const missingTargets = repairResult?.missingTargets || 0;
                            const failedTargets = repairResult?.failedTargets || 0;

                            if (repairedTargets > 0) {
                                showSuccess(`已修复 ${repairedTargets} 个环境的扩展安装文件`);
                                const extensionText = (repairResult.extensionIds || []).join('、') || '扩展';
                                const extraSummary = (skippedRunning > 0 || missingTargets > 0 || failedTargets > 0)
                                    ? `\n\n附加结果：运行中跳过 ${skippedRunning} 个，目录缺失 ${missingTargets} 个，失败 ${failedTargets} 个。`
                                    : '';

                                await showNoticeDialog(
                                    '扩展修复完成',
                                    `当前分组：${repairResult.groupName || '默认分组'}\n` +
                                    `克隆环境：${repairResult.donorWindowNumber}\n` +
                                    `已修复环境数：${repairedTargets}\n` +
                                    `扩展 ID：${extensionText}\n\n` +
                                    '请前往官方扩展商店重新安装官方插件，即可恢复扩展显示并尽量保留原有数据。' +
                                    extraSummary
                                );
                                return;
                            }

                            showWarning(`本次没有修复任何环境。运行中跳过 ${skippedRunning} 个，目录缺失 ${missingTargets} 个，失败 ${failedTargets} 个。`);
                        } catch (error) {
                            console.error('Repair extensions failed:', error);
                            showError('修复扩展失败: ' + error.message);
                        } finally {
                            repairExtensionsBtn.disabled = false;
                            repairExtensionsBtn.textContent = originalText || '修复';
                        }
                    });
                }

                // 加载屏幕信息
                await loadScreensIntoSelect(screenSelect, settings.ScreenSelection);

                if (speedSlider) {
                    speedSlider.value = settings.WindowOpenSpeed || 0.6;
                }
                if (speedValue) {
                    speedValue.textContent = `${settings.WindowOpenSpeed || 0.6}秒/个`;
                }

                // 初始化分组管理按钮事件
                const addGroupBtn = document.getElementById('modalAddGroup');
                const editGroupSelect = document.getElementById('modalEditGroupSelect');
                const editGroupBtn = document.getElementById('modalEditGroup');
                const deleteGroupBtn = document.getElementById('modalDeleteGroup');

                const loadGroupsIntoSelect = async () => {
                    if (!editGroupSelect) return;
                    try {

                        const groups = await getGroups();
                        editGroupSelect.innerHTML = '<option value="">选择分组...</option>';
                        groups.forEach(g => {
                            if (g.name === '') return;
                            const opt = document.createElement('option');
                            opt.value = g.name;
                            opt.textContent = g.name;
                            editGroupSelect.appendChild(opt);
                        });
                        if (editGroupBtn) editGroupBtn.disabled = true;
                        if (deleteGroupBtn) deleteGroupBtn.disabled = true;
                    } catch(e) { console.error('Load groups error', e); }
                };

                if (addGroupBtn) {
                    addGroupBtn.addEventListener('click', async () => {
                        await showGroupEditDialog();
                        await loadGroupsIntoSelect();
                        if (typeof initializeGroupSelects === 'function') await initializeGroupSelects();
                    });
                }

                if (editGroupSelect) {
                    await loadGroupsIntoSelect();
                    editGroupSelect.addEventListener('change', (e) => {
                        const selected = e.target.value;
                        const isDefault = (selected === '默认分组');
                        if (editGroupBtn) editGroupBtn.disabled = !selected;
                        if (deleteGroupBtn) deleteGroupBtn.disabled = !selected || isDefault;
                    });
                }

                if (editGroupBtn) {
                    editGroupBtn.addEventListener('click', async () => {
                        const selected = editGroupSelect.value;
                        if (!selected) return;

                        const groups = await getGroups();
                        const groupToEdit = groups.find(g => g.name === selected);
                        if (groupToEdit) {
                            await showGroupEditDialog(groupToEdit);
                            await loadGroupsIntoSelect();
                            if (typeof initializeGroupSelects === 'function') await initializeGroupSelects();
                        }
                    });
                }

                if (deleteGroupBtn) {
                    deleteGroupBtn.addEventListener('click', async () => {
                        const selected = editGroupSelect.value;
                        if (!selected || selected === '默认分组') return;
                        
                        const confirmed = await showConfirm(
                            '确认删除分组',
                            `确定要删除分组 "${selected}" 吗？此操作不可恢复。`
                        );
                        if (!confirmed) return;

                        try {

                            const success = await removeGroup(selected);
                            if (success) {
                                await loadGroupsIntoSelect();
                                if (typeof initializeGroupSelects === 'function') await initializeGroupSelects();
                            }
                        } catch(e) { console.error(e); }
                    });
                }

                // === 辅助功能权限入口 ===
                const openAccessibilitySettingsBtn = document.getElementById('modalOpenAccessibilitySettings');
                if (openAccessibilitySettingsBtn) {
                    openAccessibilitySettingsBtn.addEventListener('click', async () => {
                        try {
                            await OpenAccessibilitySettings();
                            showInfo('已打开“系统设置 > 隐私与安全性 > 辅助功能”');
                        } catch (error) {
                            console.error('Failed to open accessibility settings:', error);
                            showError('打开辅助功能设置失败: ' + error.message);
                        }
                    });
                }

                const openInputMonitoringSettingsBtn = document.getElementById('modalOpenInputMonitoringSettings');
                if (openInputMonitoringSettingsBtn) {
                    openInputMonitoringSettingsBtn.addEventListener('click', async () => {
                        try {
                            await OpenExternalURL('x-apple.systempreferences:com.apple.preference.security?Privacy_ListenEvent');
                            showInfo('已打开“系统设置 > 隐私与安全性 > 输入监控”');
                        } catch (error) {
                            console.error('Failed to open input monitoring settings:', error);
                            showError('打开键盘输入设置失败: ' + error.message);
                        }
                    });
                }

                const revealChromeManagerAppBtn = document.getElementById('modalRevealChromeManagerApp');
                if (revealChromeManagerAppBtn) {
                    revealChromeManagerAppBtn.addEventListener('click', async () => {
                        try {
                            await RevealChromeManagerAppInFinder();
                            showInfo('已在 Finder 中定位 ChromeManager.app');
                        } catch (error) {
                            console.error('Failed to reveal ChromeManager app:', error);
                            showError('定位 ChromeManager.app 失败: ' + error.message);
                        }
                    });
                }

                const revealTerminalAppBtn = document.getElementById('modalRevealTerminalApp');
                if (revealTerminalAppBtn) {
                    revealTerminalAppBtn.addEventListener('click', async () => {
                        try {
                            await RevealTerminalAppInFinder();
                            showInfo('已在 Finder 中定位 终端.app');
                        } catch (error) {
                            console.error('Failed to reveal terminal app:', error);
                            showError('定位 终端.app 失败: ' + error.message);
                        }
                    });
                }



                // === 路径浏览和屏幕刷新按钮 ===

                // 绑定浏览快捷方式目录按钮
                const browseShortcutBtn = document.getElementById('modalBrowseShortcut');
                if (browseShortcutBtn) {
                    browseShortcutBtn.addEventListener('click', async () => {
                        try {

                            const selectedPath = await SelectFolder();
                            if (selectedPath) {
                                document.getElementById('modalShortcutPath').value = selectedPath;
                                showSuccess('快捷方式目录选择成功');
                            }
                        } catch (error) {
                            console.error('Failed to select shortcut folder:', error);
                            if (!error.message.includes('cancelled')) {
                                showError('选择快捷方式目录失败: ' + error.message);
                            }
                        }
                    });
                }

                // 绑定浏览缓存目录按钮
                const browseCacheBtn = document.getElementById('modalBrowseCache');
                if (browseCacheBtn) {
                    browseCacheBtn.addEventListener('click', async () => {
                        try {

                            const selectedPath = await SelectFolder();
                            if (selectedPath) {
                                document.getElementById('modalCacheDir').value = selectedPath;
                                showSuccess('缓存目录选择成功');
                            }
                        } catch (error) {
                            console.error('Failed to select cache folder:', error);
                            if (!error.message.includes('cancelled')) {
                                showError('选择缓存目录失败: ' + error.message);
                            }
                        }
                    });
                }

                // 绑定浏览Chrome路径按钮
                const browseChromeBtn = document.getElementById('modalBrowseChrome');
                if (browseChromeBtn) {
                    browseChromeBtn.addEventListener('click', async () => {
                        try {

                            const selectedPath = await SelectFile("选择Chrome可执行文件", "Executable Files|*.exe");
                            if (selectedPath) {
                                document.getElementById('modalChromePath').value = selectedPath;
                                showSuccess('Chrome路径选择成功');
                            }
                        } catch (error) {
                            console.error('Failed to select chrome path:', error);
                            if (!error.message.includes('cancelled')) {
                                showError('选择Chrome路径失败: ' + error.message);
                            }
                        }
                    });
                }

                // 刷新屏幕列表按钮
                const refreshScreensBtn = document.getElementById('modalRefreshScreens');
                if (refreshScreensBtn) {
                    refreshScreensBtn.addEventListener('click', async () => {
                        try {
                            debugLog('正在刷新屏幕列表...');
                            showInfo('正在刷新屏幕列表...');

                            const screenSelect = document.getElementById('modalScreenSelection');
                            await loadScreensIntoSelect(screenSelect);

                            showSuccess('屏幕列表已刷新');
                        } catch (error) {
                            console.error('刷新屏幕列表失败:', error);
                            showError('刷新屏幕列表失败: ' + error.message);
                        }
                    });
                }

                // === 图标管理按钮 ===

                // 一键还原快捷方式图标
                const restoreIconsBtn = document.getElementById('modalRestoreIcons');
                if (restoreIconsBtn) {
                    restoreIconsBtn.addEventListener('click', async () => {
                        const confirmed = await showConfirm(
                            '确认还原快捷方式图标',
                            '此操作将把所有快捷方式的图标还原为Chrome默认图标，确定要继续吗？'
                        );

                        if (!confirmed) return;

                        try {
                            debugLog('正在还原快捷方式图标...');
                            showInfo('正在还原快捷方式图标，请稍候...');

                            await RestoreDefaultIcons();

                            showSuccess('快捷方式图标已成功还原为Chrome默认图标');
                            debugLog('快捷方式图标还原完成');
                        } catch (error) {
                            console.error('还原快捷方式图标失败:', error);
                            showError('还原快捷方式图标失败: ' + error.message);
                        }
                    });
                }

                // === 数据及配置设置按钮 ===

                // 重置为默认按钮
                const clearAllDataBtn = document.getElementById('modalClearAllData');
                debugLog('Clear All Data Button:', clearAllDataBtn);

                if (clearAllDataBtn) {
                    debugLog('Attaching event listener to Clear All Data button');
                    clearAllDataBtn.addEventListener('click', async () => {
                        debugLog('Clear All Data button clicked!');
                        const confirmed = await showConfirm(
                            '确认重置为默认配置',
                            '此操作将删除配置文件并重启应用，所有设置将恢复为默认值，此操作不可恢复，确定要继续吗？'
                        );

                        if (!confirmed) return;

                        try {

                            await clearAllData();

                            // 显示消息，应用将自动重启
                            showSuccess('配置文件已删除，应用将自动重启...');
                        } catch (error) {
                            console.error('重置配置失败:', error);
                            showError('重置配置失败: ' + error.message);
                        }
                    });
                } else {
                    console.error('Clear All Data button not found!');
                }

                // 导出设置按钮
                const exportSettingsBtn = document.getElementById('modalExportSettings');
                debugLog('Export Settings Button:', exportSettingsBtn);

                if (exportSettingsBtn) {
                    debugLog('Attaching event listener to Export Settings button');
                    exportSettingsBtn.addEventListener('click', async () => {
                        debugLog('Export Settings button clicked!');
                        try {

                            const success = await exportSettings();
                            if (success) {
                                showSuccess('设置已成功导出');
                            }
                        } catch (error) {
                            console.error('导出设置失败:', error);
                            showError('导出设置失败: ' + error.message);
                        }
                    });
                } else {
                    console.error('Export Settings button not found!');
                }

                // 导入设置按钮
                const importSettingsBtn = document.getElementById('modalImportSettings');
                debugLog('Import Settings Button:', importSettingsBtn);

                if (importSettingsBtn) {
                    debugLog('Attaching event listener to Import Settings button');
                    importSettingsBtn.addEventListener('click', async () => {
                        try {

                            const success = await importSettings();
                            if (success) {
                                // Reloading UI or Restarting logic might be handled in backend, but we can show a log just in case.
                                debugLog('导入设置完成');
                            }
                        } catch (error) {
                            console.error('导入设置失败:', error);
                            showError('导入设置失败: ' + error.message);
                        }
                    });
                }

                // 强制触发输入事件确保UI更新
                shortcutInput.dispatchEvent(new Event('input', { bubbles: true }));
                cacheInput.dispatchEvent(new Event('input', { bubbles: true }));
                if (chromePathInput) chromePathInput.dispatchEvent(new Event('input', { bubbles: true }));

                debugLog('Final input values:', {
                    shortcutPath: shortcutInput.value,
                    cacheDir: cacheInput.value,
                    shortcutPlaceholder: shortcutInput.placeholder,
                    cachePlaceholder: cacheInput.placeholder
                });
            } else {
                console.error('Settings loading failed:', {
                    hasSettings: !!settings,
                    hasShortcutInput: !!shortcutInput,
                    hasCacheInput: !!cacheInput
                });
            }
        } catch (error) {
            console.error('Failed to load settings for modal:', error);
        }
    };

    // 立即开始第一次尝试
    loadSettingsWithRetry();

    // 绑定关闭事件
    const closeModal = () => {
        overlay.remove();
    };

    modalCloseBtn.addEventListener('click', closeModal);
    cancelBtn.addEventListener('click', closeModal);

    // 点击遮罩层关闭
    overlay.addEventListener('click', (e) => {
        if (e.target === overlay) {
            closeModal();
        }
    });

    // 滑块事件
    speedSlider.addEventListener('input', (e) => {
        speedValue.textContent = `${e.target.value}秒/个`;
    });


    // === 分组管理工具函数 ===

    // 加载分组列表到模态框
    async function loadGroupsIntoModal() {
        try {

            const groups = await getGroups();
            const currentGroupSelect = document.getElementById('modalCurrentGroup');
            if (!currentGroupSelect) return;

            // 清空现有选项
            currentGroupSelect.innerHTML = '';

            // 添加分组选项
            groups.forEach(group => {
                if (group.name === '') return;
                const option = document.createElement('option');
                option.value = group.name;
                option.textContent = group.name;
                currentGroupSelect.appendChild(option);
            });

            // 设置当前选中的分组
            const settings = getCurrentSettings();
            if (settings && settings.currentGroup) {
                currentGroupSelect.value = settings.currentGroup;
            }

            debugLog('Groups loaded into modal:', groups);
        } catch (error) {
            console.error('Failed to load groups into modal:', error);
            showError('加载分组列表失败: ' + error.message);
        }
    }

    // 显示分组选择对话框
    async function showGroupSelectionDialog(groupNames, title) {
        return new Promise((resolve) => {
            const options = groupNames.map((name, index) => `${index + 1}. ${name}`).join('\n');
            const message = `${title}：\n\n${options}\n\n请输入数字选择：`;
            const input = prompt(message);

            if (input === null) {
                resolve(null); // 用户取消
                return;
            }

            const index = parseInt(input) - 1;
            if (index >= 0 && index < groupNames.length) {
                resolve(groupNames[index]);
            } else {
                alert('输入无效，请输入正确的数字');
                resolve(null);
            }
        });
    }

    // 显示分组编辑对话框
    function showGroupEditDialog(existingGroup = null) {
        return new Promise((resolve) => {
            const isEdit = !!existingGroup;
            const title = isEdit ? '编辑分组' : '添加分组';

        const dialogHtml = `
            <div class="settings-modal-overlay" id="groupEditModalOverlay">
                <div class="settings-modal" style="width: 500px; height: 400px;">
                    <div class="settings-titlebar">
                        <div class="titlebar-content">
                            <h1 class="settings-title">${title}</h1>
                            <button class="modal-close-btn" id="groupEditModalCloseBtn">×</button>
                        </div>
                    </div>
                    
                    <div class="settings-content">
                        <div class="settings-section">
                            <div class="setting-item">
                                <label class="setting-label">分组名称:</label>
                                <input type="text" id="groupEditName" class="setting-input" 
                                       placeholder="输入分组名称" value="${existingGroup?.name || ''}" autocomplete="off">
                            </div>
                            <div class="setting-item">
                                <label class="setting-label">快捷方式目录:</label>
                                <div class="setting-control-inline">
                                    <input type="text" id="groupEditShortcutPath" class="setting-input" 
                                           placeholder="选择快捷方式存放目录" value="${existingGroup?.shortcutPath || ''}" autocomplete="off">
                                    <button class="browse-btn" id="groupEditBrowseShortcut">浏览</button>
                                </div>
                            </div>
                            <div class="setting-item">
                                <label class="setting-label">缓存目录:</label>
                                <div class="setting-control-inline">
                                    <input type="text" id="groupEditCacheDir" class="setting-input" 
                                           placeholder="选择缓存目录" value="${existingGroup?.cacheDir || ''}" autocomplete="off">
                                    <button class="browse-btn" id="groupEditBrowseCache">浏览</button>
                                </div>
                            </div>
                        </div>
                        
                        <div class="settings-actions">
                            <button class="setting-btn primary" id="groupEditSaveBtn">${isEdit ? '保存' : '添加'}</button>
                            <button class="setting-btn secondary" id="groupEditCancelBtn">取消</button>
                        </div>
                    </div>
                </div>
            </div>
        `;

        document.body.insertAdjacentHTML('beforeend', dialogHtml);

        const overlay = document.getElementById('groupEditModalOverlay');
        const modal = overlay.querySelector('.settings-modal');

        // 模态框动画
        overlay.style.opacity = '0';
        overlay.style.display = 'flex';
        modal.style.transform = 'translateY(-20px)';

        setTimeout(() => {
            overlay.style.opacity = '1';
            modal.style.transform = 'translateY(0)';
        }, 10);

        // 关闭模态框
        function closeGroupEditModal(data = null) {
            overlay.style.opacity = '0';
            modal.style.transform = 'translateY(-20px)';
            setTimeout(() => {
                if (overlay.parentNode === document.body) {
                    document.body.removeChild(overlay);
                }
                resolve(data);
            }, 300);
        }

        // 事件监听器
        document.getElementById('groupEditModalCloseBtn').addEventListener('click', () => closeGroupEditModal(null));
        document.getElementById('groupEditCancelBtn').addEventListener('click', () => closeGroupEditModal(null));

        // 浏览按钮事件
        document.getElementById('groupEditBrowseShortcut').addEventListener('click', async () => {
            try {

                const selectedPath = await SelectFolder();
                if (selectedPath) {
                    document.getElementById('groupEditShortcutPath').value = selectedPath;
                }
            } catch (error) {
                console.error('Failed to select folder:', error);
                showError('选择文件夹失败: ' + error.message);
            }
        });

        document.getElementById('groupEditBrowseCache').addEventListener('click', async () => {
            try {

                const selectedPath = await SelectFolder();
                if (selectedPath) {
                    document.getElementById('groupEditCacheDir').value = selectedPath;
                }
            } catch (error) {
                console.error('Failed to select folder:', error);
                showError('选择文件夹失败: ' + error.message);
            }
        });

        // 保存按钮事件
        document.getElementById('groupEditSaveBtn').addEventListener('click', async () => {
            const name = document.getElementById('groupEditName').value.trim();
            const shortcutPath = document.getElementById('groupEditShortcutPath').value.trim();
            const cacheDir = document.getElementById('groupEditCacheDir').value.trim();

            if (!name) {
                showError('请输入分组名称');
                return;
            }

            const groupData = { name, shortcutPath, cacheDir };

            try {
                let success = false;
                if (isEdit) {

                    success = await updateGroup(existingGroup.name, groupData);
                } else {

                    success = await addGroup(groupData);
                }

                if (success) {
                    // 刷新主界面的分组下拉菜单
                    await initializeGroupSelects();
                    closeGroupEditModal(groupData);
                }
            } catch (error) {
                console.error('Failed to save group:', error);
                showError(`${isEdit ? '更新' : '添加'}分组失败: ` + error.message);
            }
        });

        // 点击遮罩层关闭
        overlay.addEventListener('click', (e) => {
            if (e.target === overlay) {
                closeGroupEditModal(null);
            }
        });
    });
}

    // 保存设置
    saveBtn.addEventListener('click', async () => {
        debugLog('开始保存设置...');

        try {
            // 重新获取最新的后端设置，确保不会覆盖其他字段

            const currentSettings = await GetSettingsForFrontend();
            debugLog('当前设置获取成功');

            // 获取输入框的值，如果为空则保持原有值
            const shortcutPath = document.getElementById('modalShortcutPath').value.trim();
            const cacheDir = document.getElementById('modalCacheDir').value.trim();
            const chromePath = document.getElementById('modalChromePath')?.value.trim() || '';
            const screenSelection = document.getElementById('modalScreenSelection')?.value || '';
            const syncHotkeyInput = document.getElementById('modalSyncHotkey');
            let syncToggleHotkey = syncHotkeyInput ? (syncHotkeyInput.dataset.hotkey || syncHotkeyInput.value || '').trim() : '';
            
            // On Mac, convert Cmd back to Win so the backend Go layer can parse it correctly
            if (/Mac|iPod|iPhone|iPad/.test(navigator.platform)) {
                syncToggleHotkey = syncToggleHotkey.replace(/Cmd/g, 'Win');
            }

            const newSettings = {
                ...currentSettings, // 保留所有现有设置
                ShortcutPath: shortcutPath || currentSettings.ShortcutPath || '',
                CacheDir: cacheDir || currentSettings.CacheDir || '',
                ChromePath: chromePath || currentSettings.ChromePath || '',
                ScreenSelection: screenSelection || currentSettings.ScreenSelection || '',
                SyncToggleHotkey: syncToggleHotkey,
                AutoModifyShortcutIcon: document.getElementById('modalAutoModifyIcon')?.checked || false,
                WindowOpenSpeed: parseFloat(document.getElementById('modalWindowSpeed')?.value || 0.6),
                EnableAPI: document.getElementById('modalEnableAPI')?.checked || false,
                APIPort: parseInt(document.getElementById('modalAPIPort')?.value || 18923, 10),
                APIToken: document.getElementById('modalAPIToken')?.value || ''
            };

            debugLog('准备保存的设置:', newSettings);

            // 导入并调用保存设置的方法

            await UpdateSettings(newSettings);
            debugLog('设置更新成功');

            // 重新加载设置到内存
            await loadSettings();
            debugLog('设置重新加载成功');

            // 刷新主界面的分组下拉菜单
            await initializeGroupSelects();
            debugLog('分组选择器刷新成功');

            debugLog('显示成功消息并关闭窗口');
            showSuccess('设置保存成功');

            // 确保在成功消息显示后再关闭窗口
            setTimeout(() => {
                debugLog('执行关闭模态框');
                closeModal();
            }, 100);

        } catch (error) {
            console.error('保存设置失败详细信息:', error);
            showError('保存设置失败: ' + error.message);
            // 即使出错也不关闭窗口，让用户能够看到错误信息并重试
        }
    });

    // ESC键关闭
    const handleEscape = (e) => {
        if (e.key === 'Escape') {
            closeModal();
            document.removeEventListener('keydown', handleEscape);
        }
    };
    document.addEventListener('keydown', handleEscape);
}

// 显示确认对话框
function showConfirm(title, message) {
    return new Promise((resolve) => {
        // 创建模态对话框
        const modal = document.createElement('div');
        modal.className = 'confirm-modal';
        modal.innerHTML = `
            <div class="confirm-modal-content">
                <div class="confirm-modal-header">
                    <h3>${title}</h3>
                </div>
                <div class="confirm-modal-body">
                    <p>${message}</p>
                </div>
                <div class="confirm-modal-footer">
                    <button class="confirm-btn confirm-btn-cancel">取消</button>
                    <button class="confirm-btn confirm-btn-ok">确定</button>
                </div>
            </div>
        `;

        // 添加样式
        const style = document.createElement('style');
        style.textContent = `
            .confirm-modal {
                position: fixed;
                top: 0;
                left: 0;
                width: 100%;
                height: 100%;
                background: rgba(0, 0, 0, 0.5);
                display: flex;
                justify-content: center;
                align-items: center;
                z-index: 10000;
            }
            .confirm-modal-content {
                background: #2b2b2b;
                color: #ffffff;
                border-radius: 8px;
                min-width: 400px;
                max-width: 500px;
                box-shadow: 0 4px 20px rgba(0, 0, 0, 0.3);
            }
            .confirm-modal-header {
                padding: 20px 20px 10px;
                border-bottom: 1px solid #444;
            }
            .confirm-modal-header h3 {
                margin: 0;
                font-size: 18px;
                font-weight: 600;
            }
            .confirm-modal-body {
                padding: 20px;
                line-height: 1.5;
            }
            .confirm-modal-footer {
                padding: 10px 20px 20px;
                display: flex;
                justify-content: flex-end;
                gap: 10px;
            }
            .confirm-btn {
                padding: 8px 16px;
                border: none;
                border-radius: 4px;
                cursor: pointer;
                font-size: 14px;
                transition: background-color 0.2s;
            }
            .confirm-btn-cancel {
                background: #666;
                color: white;
            }
            .confirm-btn-cancel:hover {
                background: #777;
            }
            .confirm-btn-ok {
                background: #0078d4;
                color: white;
            }
            .confirm-btn-ok:hover {
                background: #106ebe;
            }
        `;
        document.head.appendChild(style);

        // 添加到页面
        document.body.appendChild(modal);

        // 绑定事件
        const cancelBtn = modal.querySelector('.confirm-btn-cancel');
        const okBtn = modal.querySelector('.confirm-btn-ok');

        const cleanup = () => {
            document.body.removeChild(modal);
            document.head.removeChild(style);
        };

        cancelBtn.addEventListener('click', () => {
            cleanup();
            resolve(false);
        });

        okBtn.addEventListener('click', () => {
            cleanup();
            resolve(true);
        });

        // ESC键取消
        const handleKeydown = (e) => {
            if (e.key === 'Escape') {
                cleanup();
                resolve(false);
                document.removeEventListener('keydown', handleKeydown);
            }
        };
        document.addEventListener('keydown', handleKeydown);

        // 点击模态背景取消
        modal.addEventListener('click', (e) => {
            if (e.target === modal) {
                cleanup();
                resolve(false);
            }
        });
    });
}

function showNoticeDialog(title, message) {
    return new Promise((resolve) => {
        const modalHTML = `
            <div class="settings-modal-overlay" id="noticeDialogOverlay">
                <div class="settings-modal" style="max-width: 560px;">
                    <div class="settings-titlebar">
                        <div class="titlebar-content">
                            <h1 class="settings-title">${escapeHTML(title)}</h1>
                            <button class="modal-close-btn" id="noticeDialogClose">×</button>
                        </div>
                    </div>
                    <div class="settings-content">
                        <div class="settings-section" style="border-bottom: none;">
                            <div style="white-space: pre-wrap; font-size: 13px; line-height: 1.7; color: #4b5563;">${escapeHTML(message)}</div>
                        </div>
                        <div class="settings-actions">
                            <button class="setting-btn gray" id="noticeDialogConfirm">关闭</button>
                        </div>
                    </div>
                </div>
            </div>
        `;

        document.body.insertAdjacentHTML('beforeend', modalHTML);

        const overlay = document.getElementById('noticeDialogOverlay');
        const closeBtn = document.getElementById('noticeDialogClose');
        const confirmBtn = document.getElementById('noticeDialogConfirm');

        const closeDialog = () => {
            if (overlay?.parentNode) {
                overlay.parentNode.removeChild(overlay);
            }
            resolve();
        };

        closeBtn.addEventListener('click', closeDialog);
        confirmBtn.addEventListener('click', closeDialog);
        overlay.addEventListener('click', (event) => {
            if (event.target === overlay) {
                closeDialog();
            }
        });
    });
}

function showAccessibilityPermissionAssistModal(options = {}) {
    const existing = document.getElementById('accessibilityPermissionAssistOverlay');
    if (existing) return;

    const {
        reason = '',
        showSkipForever = true
    } = options;

    const modalHTML = `
        <div class="settings-modal-overlay" id="accessibilityPermissionAssistOverlay">
            <div class="settings-modal" style="max-width: 620px;">
                <div class="settings-titlebar">
                    <div class="titlebar-content">
                        <h1 class="settings-title">辅助功能权限引导</h1>
                        <button class="modal-close-btn" id="accessibilityPermissionAssistClose">×</button>
                    </div>
                </div>
                <div class="settings-content">
                    <div class="settings-section" style="border-bottom: none;">
                        ${reason ? `<div style="font-size: 13px; line-height: 1.7; color: #b45309; background: #fffbeb; border: 1px solid #fde68a; border-radius: 10px; padding: 10px 12px; margin-bottom: 12px;">${reason}</div>` : ''}
                        <div style="white-space: pre-wrap; font-size: 13px; line-height: 1.75; color: #4b5563;">
首次使用请在 macOS“辅助功能”中授权：
1. ChromeManager.app（软件窗口控制能力）
2. 终端.app（后续与 Agent 自动化配合时需要）

如果同步键鼠没有效果，请再到“输入监控”中授权 ChromeManager.app。

可按下面按钮逐步打开入口。
请手动将两个程序拖进辅助功能面板给予其权限。
                        </div>
                        <div style="display: flex; gap: 8px; flex-wrap: wrap; margin-top: 14px;">
                            <button class="setting-btn secondary permission-action-btn" id="permissionAssistOpenSettings">打开辅助功能设置</button>
                            <button class="setting-btn secondary permission-action-btn" id="permissionAssistOpenInputMonitoring">打开输入监控设置</button>
		                            <button class="setting-btn secondary permission-action-btn" id="permissionAssistRevealApp">定位 ChromeManager.app</button>
		                            <button class="setting-btn secondary permission-action-btn" id="permissionAssistRevealTerminal">定位 终端.app</button>
	                        </div>
	                        <div style="margin-top: 12px; ${showSkipForever ? '' : 'display: none;'}">
	                            <label class="checkbox-label" style="font-size: 12px; color: #6b7280;">
	                                <input type="checkbox" id="permissionAssistSkipForever">
	                                <span>今后不再提醒</span>
                            </label>
                        </div>
                    </div>
                    <div class="settings-actions">
                        <button class="setting-btn gray" id="permissionAssistDone">我已了解</button>
                    </div>
                </div>
            </div>
        </div>
    `;

    document.body.insertAdjacentHTML('beforeend', modalHTML);

    const overlay = document.getElementById('accessibilityPermissionAssistOverlay');
    const closeBtn = document.getElementById('accessibilityPermissionAssistClose');
    const doneBtn = document.getElementById('permissionAssistDone');
    const openSettingsBtn = document.getElementById('permissionAssistOpenSettings');
    const openInputMonitoringBtn = document.getElementById('permissionAssistOpenInputMonitoring');
    const revealAppBtn = document.getElementById('permissionAssistRevealApp');
    const revealTerminalBtn = document.getElementById('permissionAssistRevealTerminal');
    const skipForeverCheckbox = document.getElementById('permissionAssistSkipForever');

    const closeDialog = () => {
        if (skipForeverCheckbox?.checked) {
            markAccessibilityPermissionAssistSkipForever();
        }
        if (overlay?.parentNode) {
            overlay.parentNode.removeChild(overlay);
        }
    };

    closeBtn?.addEventListener('click', closeDialog);
    doneBtn?.addEventListener('click', closeDialog);

    overlay?.addEventListener('click', (event) => {
        if (event.target === overlay) {
            closeDialog();
        }
    });

    openSettingsBtn?.addEventListener('click', async () => {
        try {
            await OpenAccessibilitySettings();
            showInfo('已打开“系统设置 > 隐私与安全性 > 辅助功能”');
        } catch (error) {
            console.error('Open accessibility settings failed:', error);
            showError('打开辅助功能设置失败: ' + error.message);
        }
    });

    openInputMonitoringBtn?.addEventListener('click', async () => {
        try {
            await OpenExternalURL('x-apple.systempreferences:com.apple.preference.security?Privacy_ListenEvent');
            showInfo('已打开“系统设置 > 隐私与安全性 > 输入监控”');
        } catch (error) {
            console.error('Open input monitoring settings failed:', error);
            showError('打开输入监控设置失败: ' + error.message);
        }
    });

    revealAppBtn?.addEventListener('click', async () => {
        try {
            await RevealChromeManagerAppInFinder();
            showInfo('已在 Finder 中定位 ChromeManager.app');
        } catch (error) {
            console.error('Reveal app failed:', error);
            showError('定位 ChromeManager.app 失败: ' + error.message);
        }
    });

    revealTerminalBtn?.addEventListener('click', async () => {
        try {
            await RevealTerminalAppInFinder();
            showInfo('已在 Finder 中定位 终端.app');
        } catch (error) {
            console.error('Reveal terminal failed:', error);
            showError('定位 终端.app 失败: ' + error.message);
        }
    });
}

function shouldShowAccessibilityPermissionAssist() {
    try {
        const skipForever = localStorage.getItem(ACCESSIBILITY_ASSIST_SKIP_KEY);
        return skipForever !== '1';
    } catch (error) {
        return true;
    }
}

function markAccessibilityPermissionAssistSkipForever() {
    try {
        localStorage.setItem(ACCESSIBILITY_ASSIST_SKIP_KEY, '1');
    } catch (error) {
        console.warn('Failed to persist accessibility reminder preference:', error);
    }
}

function isAccessibilityPermissionErrorMessage(message) {
    const text = String(message || '');
    return text.includes('辅助功能权限') ||
        text.includes('Accessibility') ||
        text.includes('CGEventTapCreate') ||
        text.includes('事件监听启动失败');
}

function formatBytes(bytes) {
    const value = Number(bytes) || 0;
    if (value <= 0) {
        return '0 B';
    }

    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let size = value;
    let unitIndex = 0;

    while (size >= 1024 && unitIndex < units.length - 1) {
        size /= 1024;
        unitIndex++;
    }

    const precision = unitIndex === 0 ? 0 : size >= 100 ? 0 : size >= 10 ? 1 : 2;
    return `${size.toFixed(precision)} ${units[unitIndex]}`;
}

function escapeHTML(value) {
    return String(value ?? '')
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

async function copyPlainText(text, successMessage) {
    try {
        await navigator.clipboard.writeText(text);
        showSuccess(successMessage);
    } catch (error) {
        console.error('Failed to copy text:', error);
        showError('复制失败，请手动复制');
    }
}

function getCacheProfileTone(profile) {
    if (!profile?.exists) {
        return 'neutral';
    }
    if (profile?.running) {
        return 'warning';
    }
    if ((profile?.status || '').startsWith('清理失败')) {
        return 'danger';
    }
    if (profile?.status === '已清理') {
        return 'success';
    }
    if (profile?.canClean) {
        return 'ready';
    }
    return 'neutral';
}

function renderBrowserCacheSummary(scanResult) {
    const scannedProfiles = scanResult?.scannedProfiles ?? 0;
    const cleanableProfiles = scanResult?.cleanableProfiles ?? 0;
    const skippedRunning = scanResult?.skippedRunning ?? 0;
    const totalBytes = scanResult?.totalBytes ?? 0;

    if (scannedProfiles === 0) {
        return `
            <div class="cache-clean-empty">
                当前没有找到可扫描的环境目录，请检查分组和窗口编号是否正确。
            </div>
        `;
    }

    return `
        <div class="cache-clean-summary">
            <div class="cache-clean-card">
                <span class="cache-clean-card-label">已扫描环境</span>
                <strong>${scannedProfiles}</strong>
            </div>
            <div class="cache-clean-card">
                <span class="cache-clean-card-label">可清理环境</span>
                <strong>${cleanableProfiles}</strong>
            </div>
            <div class="cache-clean-card">
                <span class="cache-clean-card-label">运行中跳过</span>
                <strong>${skippedRunning}</strong>
            </div>
            <div class="cache-clean-card">
                <span class="cache-clean-card-label">预计可释放</span>
                <strong>${formatBytes(totalBytes)}</strong>
            </div>
        </div>
    `;
}

function renderBrowserCacheProfiles(scanResult) {
    const profiles = scanResult?.profiles || [];
    if (profiles.length === 0) {
        return '';
    }

    return `
        <div class="cache-clean-results">
            ${profiles.map((profile) => {
                const tone = getCacheProfileTone(profile);
                const details = profile?.details || [];
                const detailHtml = details.length > 0
                    ? `
                        <details class="cache-clean-breakdown">
                            <summary>查看明细</summary>
                            <div class="cache-clean-breakdown-list">
                                ${details.map((detail) => `
                                    <div class="cache-clean-breakdown-row">
                                        <span>${escapeHTML(detail.relativePath || detail.name)}</span>
                                        <strong>${formatBytes(detail.bytes)}</strong>
                                    </div>
                                `).join('')}
                            </div>
                        </details>
                    `
                    : '<div class="cache-clean-empty-inline">没有发现可清理的安全缓存目录。</div>';

                return `
                    <div class="cache-clean-item">
                        <div class="cache-clean-item-header">
                            <div class="cache-clean-item-main">
                                <div class="cache-clean-item-title">窗口 ${profile.windowNumber || '-'}</div>
                                <div class="cache-clean-item-path">${escapeHTML(profile.userDataDir || '')}</div>
                            </div>
                            <div class="cache-clean-item-side">
                                <div class="cache-clean-item-size">${formatBytes(profile.bytes)}</div>
                                <span class="cache-clean-tag ${tone}">${escapeHTML(profile.status || '')}</span>
                            </div>
                        </div>
                        ${detailHtml}
                    </div>
                `;
            }).join('')}
        </div>
    `;
}

function renderBrowserCacheTargets(targets) {
    const safeTargets = Array.isArray(targets) ? targets : [];
    if (safeTargets.length === 0) {
        return '';
    }

    return `
        <div class="cache-clean-targets">
            ${safeTargets.map((target) => `<span class="cache-clean-target-chip">${escapeHTML(target)}</span>`).join('')}
        </div>
    `;
}

function showBrowserCacheCleanupModal(groupName, windowNumbers) {
    const modalHTML = `
        <div class="settings-modal-overlay" id="browserCacheCleanupModalOverlay">
            <div class="settings-modal browser-cache-modal">
                <div class="settings-titlebar">
                    <div class="titlebar-content">
                        <h1 class="settings-title">浏览器缓存清理</h1>
                        <button class="modal-close-btn" id="browserCacheCleanupModalClose">×</button>
                    </div>
                </div>

                <div class="settings-content">
                    <div class="settings-section">
                        <h2 class="section-title">安全清理说明</h2>
                        <div class="cache-clean-intro">
                            这里只会扫描已导入环境中的安全缓存目录，默认跳过仍在运行的浏览器环境，不触碰插件、书签、Cookie、Local Storage、IndexedDB、Login Data、Secure Preferences 等关键数据。
                        </div>
                    </div>

                    <div class="settings-section">
                        <div class="cache-clean-toolbar">
                            <button class="setting-btn primary" id="browserCacheScanBtn">开始扫描</button>
                            <button class="setting-btn secondary" id="browserCacheExecuteBtn" disabled>执行清理</button>
                            <span class="cache-clean-toolbar-note">建议先关闭对应浏览器窗口后再清理，这样释放空间更多，也更稳妥。</span>
                        </div>
                        <div id="browserCacheTargets"></div>
                        <div class="cache-clean-status" id="browserCacheStatus">点击“开始扫描”后，将统计当前已选择环境的可清理空间。</div>
                    </div>

                    <div class="settings-section">
                        <div id="browserCacheSummary"></div>
                        <div id="browserCacheResults"></div>
                    </div>

                    <div class="settings-actions">
                        <button class="setting-btn gray" id="browserCacheCleanupCloseBtn">关闭</button>
                    </div>
                </div>
            </div>
        </div>
    `;

    document.body.insertAdjacentHTML('beforeend', modalHTML);

    const overlay = document.getElementById('browserCacheCleanupModalOverlay');
    const closeBtn = document.getElementById('browserCacheCleanupModalClose');
    const footerCloseBtn = document.getElementById('browserCacheCleanupCloseBtn');
    const scanBtn = document.getElementById('browserCacheScanBtn');
    const cleanBtn = document.getElementById('browserCacheExecuteBtn');
    const statusEl = document.getElementById('browserCacheStatus');
    const summaryEl = document.getElementById('browserCacheSummary');
    const resultsEl = document.getElementById('browserCacheResults');
    const targetsEl = document.getElementById('browserCacheTargets');

    let currentScanResult = null;
    let busy = false;

    const closeDialog = () => {
        if (overlay?.parentNode) {
            overlay.parentNode.removeChild(overlay);
        }
    };

    const updateActionButtons = () => {
        const hasCleanableProfiles = Boolean(
            currentScanResult?.profiles?.some((profile) => profile?.canClean)
        );
        scanBtn.disabled = busy;
        cleanBtn.disabled = busy || !hasCleanableProfiles;
    };

    const setStatus = (message, tone = 'info') => {
        statusEl.textContent = message;
        statusEl.className = `cache-clean-status ${tone}`;
    };

    const renderScanResult = (scanResult) => {
        currentScanResult = scanResult || {
            profiles: [],
            safeTargets: [],
            scannedProfiles: 0,
            cleanableProfiles: 0,
            skippedRunning: 0,
            totalBytes: 0
        };

        targetsEl.innerHTML = renderBrowserCacheTargets(currentScanResult.safeTargets);
        summaryEl.innerHTML = renderBrowserCacheSummary(currentScanResult);
        resultsEl.innerHTML = renderBrowserCacheProfiles(currentScanResult);
        updateActionButtons();
    };

    const scanCaches = async (statusMessage = '正在扫描浏览器缓存，请稍候...') => {
        busy = true;
        updateActionButtons();
        setStatus(statusMessage, 'info');

        try {
            const scanResult = await ScanBrowserCaches(groupName, windowNumbers);
            renderScanResult(scanResult);

            if ((scanResult?.scannedProfiles || 0) === 0) {
                setStatus('扫描完成，但没有找到可扫描的环境目录。', 'warning');
            } else if ((scanResult?.cleanableProfiles || 0) > 0) {
                setStatus(`扫描完成，可清理 ${scanResult.cleanableProfiles} 个环境，预计释放 ${formatBytes(scanResult.totalBytes)}。`, 'success');
            } else if ((scanResult?.skippedRunning || 0) > 0) {
                setStatus('扫描完成，但当前环境仍在运行中，暂时没有可直接清理的缓存。', 'warning');
            } else {
                setStatus('扫描完成，当前没有发现可清理的安全缓存。', 'success');
            }
        } catch (error) {
            console.error('Failed to scan browser caches:', error);
            setStatus('扫描失败：' + (error?.message || error), 'danger');
        } finally {
            busy = false;
            updateActionButtons();
        }
    };

    const cleanCaches = async () => {
        const cleanableProfiles = currentScanResult?.profiles?.filter((profile) => profile?.canClean) || [];
        if (cleanableProfiles.length === 0) {
            setStatus('当前没有可执行清理的环境。', 'warning');
            updateActionButtons();
            return;
        }

        busy = true;
        updateActionButtons();
        setStatus('正在执行缓存清理，请稍候...', 'info');

        try {
            const cleanResult = await CleanBrowserCaches(groupName, windowNumbers);
            const cleanedProfiles = cleanResult?.cleanedProfiles || 0;
            const skippedRunning = cleanResult?.skippedRunning || 0;
            const failedProfiles = cleanResult?.failedProfiles || 0;
            const freedBytes = cleanResult?.freedBytes || 0;

            if (cleanedProfiles > 0) {
                setStatus(`清理完成，已处理 ${cleanedProfiles} 个环境，释放 ${formatBytes(freedBytes)}。`, 'success');
            } else if (failedProfiles > 0) {
                setStatus(`清理失败：有 ${failedProfiles} 个环境清理失败。`, 'danger');
            } else if (skippedRunning > 0) {
                setStatus('清理已跳过运行中的环境，请关闭浏览器后再试。', 'warning');
            } else {
                setStatus('没有执行任何清理操作。', 'warning');
            }

            const refreshedResult = await ScanBrowserCaches(groupName, windowNumbers);
            renderScanResult(refreshedResult);
        } catch (error) {
            console.error('Failed to clean browser caches:', error);
            setStatus('清理失败：' + (error?.message || error), 'danger');
        } finally {
            busy = false;
            updateActionButtons();
        }
    };

    closeBtn.addEventListener('click', closeDialog);
    footerCloseBtn.addEventListener('click', closeDialog);
    scanBtn.addEventListener('click', () => {
        scanCaches();
    });
    cleanBtn.addEventListener('click', () => {
        cleanCaches();
    });

    overlay.addEventListener('click', (event) => {
        if (event.target === overlay) {
            closeDialog();
        }
    });

    updateActionButtons();
}

function showAutomationOpenClawModal() {
    const modalHTML = `
        <div class="settings-modal-overlay" id="automationOpenclawModalOverlay">
            <div class="settings-modal automation-modal">
                <div class="settings-titlebar">
                    <div class="titlebar-content">
                        <h1 class="settings-title">OpenClaw 接入</h1>
                        <button class="modal-close-btn" id="automationOpenclawModalClose">×</button>
                    </div>
                </div>

                <div class="settings-content">
                    <div class="settings-section">
                        <h2 class="section-title">功能定位</h2>
                        <div class="automation-intro">
                            通过向 OpenClaw 发送自然语言任务，让其调用 ChromeManager 的本地 API 完成窗口准备、批量导航、同步配合以及环境级自动化操作。
                        </div>
                    </div>

                    <div class="settings-section">
                        <h2 class="section-title">Skills 仓库</h2>
                        <div class="automation-repo-card">
                            <div class="automation-repo-url">${escapeHTML(AUTOMATION_SKILL_REPO_URL)}</div>
                            <div class="automation-repo-actions">
                                <button class="setting-btn primary" id="automationCopyRepoBtn">复制链接</button>
                                <button class="setting-btn secondary" id="automationOpenRepoBtn">打开仓库</button>
                            </div>
                        </div>
                    </div>

                    <div class="settings-section">
                        <h2 class="section-title">快速接入</h2>
                        <div class="automation-step-list">
                            <div class="automation-step-item">
                                <strong>1. 导入 skills</strong>
                                <span>将上方仓库里的 <code>chromemanager-skill-openclaw</code> 文件夹导入 OpenClaw 的 <code>~/.openclaw/workspace/skills/</code> 目录。</span>
                            </div>
                            <div class="automation-step-item">
                                <strong>2. 启用本地 API</strong>
                                <span>在 ChromeManager 的“设置 → OpenClaw API 集成”中启用 API，并把生成的 Token 填入 <code>chromemanager-skill-openclaw/config.json</code>。</span>
                            </div>
                            <div class="automation-step-item">
                                <strong>3. 让 OpenClaw 发起任务</strong>
                                <span>通过 <code>/cm</code> 或 <code>/chromemanager</code> 前缀触发 skill，先做窗口准备，再读取 <code>debugPort</code>，对每个窗口做运行时 attach 和页面级操作。</span>
                            </div>
                        </div>
                    </div>

                    <div class="settings-section">
                        <h2 class="section-title">关键说明</h2>
                        <div class="automation-note-list">
                            <div class="automation-note-item">ChromeManager 负责多窗口打开、导入、全选、排布、批量交互与关闭。</div>
                            <div class="automation-note-item">网页内部精确交互优先通过 CDP / DevTools attach 完成。</div>
                            <div class="automation-note-item">执行成功率与 OpenClaw skills 配置有关，用户可自行优化。</div>
                        </div>
                    </div>

                    <div class="settings-actions">
                        <button class="setting-btn gray" id="automationOpenclawModalConfirm">关闭</button>
                    </div>
                </div>
            </div>
        </div>
    `;

    document.body.insertAdjacentHTML('beforeend', modalHTML);

    const overlay = document.getElementById('automationOpenclawModalOverlay');
    const closeBtn = document.getElementById('automationOpenclawModalClose');
    const confirmBtn = document.getElementById('automationOpenclawModalConfirm');
    const copyRepoBtn = document.getElementById('automationCopyRepoBtn');
    const openRepoBtn = document.getElementById('automationOpenRepoBtn');

    const closeDialog = () => {
        if (overlay?.parentNode) {
            overlay.parentNode.removeChild(overlay);
        }
    };

    closeBtn.addEventListener('click', closeDialog);
    confirmBtn.addEventListener('click', closeDialog);
    overlay.addEventListener('click', (event) => {
        if (event.target === overlay) {
            closeDialog();
        }
    });

    copyRepoBtn.addEventListener('click', async () => {
        await copyPlainText(AUTOMATION_SKILL_REPO_URL, 'Skills 仓库链接已复制');
    });

    openRepoBtn.addEventListener('click', async () => {
        try {
            await OpenExternalURL(AUTOMATION_SKILL_REPO_URL);
        } catch (error) {
            console.error('Failed to open skill repo url:', error);
            showError('打开仓库失败: ' + error.message);
        }
    });
}

function showAutomationExamplesModal() {
    const examples = [
        '/cm 帮助',
        '/cm 准备 1-3，导航到 www.baidu.com',
        '/cm 准备 1-5，打开百度并搜索 hello',
        '/cm 准备 1-5，打开www.xxxx.com,点击页面上的“签到”按钮',
    ];

    const modalHTML = `
        <div class="settings-modal-overlay" id="automationExamplesModalOverlay">
            <div class="settings-modal automation-modal">
                <div class="settings-titlebar">
                    <div class="titlebar-content">
                        <h1 class="settings-title">示例指令</h1>
                        <button class="modal-close-btn" id="automationExamplesModalClose">×</button>
                    </div>
                </div>

                <div class="settings-content">
                    <div class="settings-section">
                        <h2 class="section-title">使用建议</h2>
                        <div class="automation-intro">
                            你可以直接把下面的示例发给 OpenClaw，也可以在它们的基础上替换分组、窗口编号、目标网址和任务动作。建议优先使用 <code>/cm</code> 或 <code>/chromemanager</code> 前缀。
                        </div>
                    </div>

                    <div class="settings-section">
                        <h2 class="section-title">常用示例</h2>
                        <div class="automation-example-list">
                            ${examples.map((example, index) => `
                                <div class="automation-example-item">
                                    <div class="automation-example-text">${escapeHTML(example)}</div>
                                    <button class="setting-btn secondary automation-copy-btn" data-example-index="${index}">复制</button>
                                </div>
                            `).join('')}
                        </div>
                    </div>

                    <div class="settings-section">
                        <h2 class="section-title">流程提醒</h2>
                        <div class="automation-note-list">
                            <div class="automation-note-item">窗口类任务优先走 ChromeManager API。</div>
                            <div class="automation-note-item">网页内部精确交互优先走运行时 attach。</div>
                            <div class="automation-note-item">遇到 Cloudflare / 验证码等挑战页面时，默认暂停并提示人工处理，而不是盲目继续。</div>
                        </div>
                    </div>

                    <div class="settings-actions">
                        <button class="setting-btn gray" id="automationExamplesModalConfirm">关闭</button>
                    </div>
                </div>
            </div>
        </div>
    `;

    document.body.insertAdjacentHTML('beforeend', modalHTML);

    const overlay = document.getElementById('automationExamplesModalOverlay');
    const closeBtn = document.getElementById('automationExamplesModalClose');
    const confirmBtn = document.getElementById('automationExamplesModalConfirm');

    const closeDialog = () => {
        if (overlay?.parentNode) {
            overlay.parentNode.removeChild(overlay);
        }
    };

    closeBtn.addEventListener('click', closeDialog);
    confirmBtn.addEventListener('click', closeDialog);
    overlay.addEventListener('click', (event) => {
        if (event.target === overlay) {
            closeDialog();
        }
    });

    document.querySelectorAll('.automation-copy-btn').forEach((button, index) => {
        button.addEventListener('click', async () => {
            const example = examples[index];
            await copyPlainText(example, '示例指令已复制');
        });
    });
}

// 加载屏幕信息到选择框
async function loadScreensIntoSelect(selectElement, selectedValue = null) {
    if (!selectElement) return;

    try {
        // 调用后端API获取屏幕信息

        const screens = await GetScreensInfo();

        debugLog('获取到的屏幕信息:', screens);

        // 清空现有选项
        selectElement.innerHTML = '';

        // 添加屏幕选项
        screens.forEach((screen, index) => {
            const option = document.createElement('option');
            option.value = screen.name;
            option.textContent = screen.name;
            selectElement.appendChild(option);
        });

        // 如果没有检测到屏幕，添加默认选项
        if (screens.length === 0) {
            const option = document.createElement('option');
            option.value = '主屏幕 - 1920x1080';
            option.textContent = '主屏幕 - 1920x1080';
            selectElement.appendChild(option);
        }

        // 设置选中值
        if (selectedValue && selectElement.querySelector(`option[value="${selectedValue}"]`)) {
            selectElement.value = selectedValue;
        } else if (screens.length > 0) {
            // 默认选择主屏幕
            const primaryScreen = screens.find(s => s.primary) || screens[0];
            selectElement.value = primaryScreen.name;
        }

        debugLog('屏幕列表加载完成，当前选中:', selectElement.value);

    } catch (error) {
        console.error('加载屏幕信息失败:', error);

        // 失败时添加默认选项
        selectElement.innerHTML = '<option value="主屏幕 - 1920x1080">主屏幕 - 1920x1080</option>';
        showError('获取屏幕信息失败，使用默认屏幕');
    }
}

/**
 * 显示自定义网址管理对话框
 */
async function showCustomURLsModal() {
    try {
        // 获取当前预设网址
        const presetUrls = await GetPresetURLs();

        // 创建模态框HTML - 使用标准的 Tech Utility 主题结构
        const modalHTML = `
            <div class="settings-modal-overlay" id="customUrlsModalOverlay">
                <div class="settings-modal" style="width: 550px; height: 500px;">
                    <div class="settings-titlebar">
                        <div class="titlebar-content">
                            <h1 class="settings-title">自定义网址管理</h1>
                            <button class="modal-close-btn" id="customUrlsCloseBtn">×</button>
                        </div>
                    </div>
                    
                    <div class="settings-content">
                        <!-- 添加新网址 -->
                        <div class="settings-section">
                            <h2 class="section-title">添加新网址</h2>
                            <div class="setting-row">
                                <label class="setting-label">新网址信息</label>
                                <div class="setting-control" style="gap: 8px;">
                                    <input type="text" id="newUrlName" placeholder="名称" class="setting-input" style="flex: 1;">
                                    <input type="text" id="newUrlValue" placeholder="https://..." class="setting-input" style="flex: 2;">
                                    <button id="addUrlBtn" class="setting-btn primary" style="min-width: 60px;">添加</button>
                                </div>
                            </div>
                        </div>
                        
                        <!-- 现有网址列表 -->
                        <div class="settings-section" style="flex: 1; display: flex; flex-direction: column; min-height: 0;">
                            <h2 class="section-title">现有网址</h2>
                            <div class="urls-list" id="urlsList" style="flex: 1;">
                                <!-- 动态生成 -->
                            </div>
                        </div>
                        
                        <!-- 底部操作栏 -->
                        <div class="settings-actions">
                            <button id="saveUrlsBtn" class="setting-btn primary">保存并关闭</button>
                        </div>
                    </div>
                </div>
            </div>
        `;

        // 移除旧的样式注入逻辑 (使用 style.css 中的全局样式)

        document.body.insertAdjacentHTML('beforeend', modalHTML);

        const overlay = document.getElementById('customUrlsModalOverlay');
        const modal = overlay.querySelector('.settings-modal');
        const urlsList = document.getElementById('urlsList');

        // 模态框动画
        overlay.style.opacity = '0';
        overlay.style.display = 'flex';
        modal.style.transform = 'translateY(-20px)';

        setTimeout(() => {
            overlay.style.opacity = '1';
            modal.style.transform = 'translateY(0)';
        }, 10);

        // 关闭模态框
        function closeCustomUrlsModal() {
            overlay.style.opacity = '0';
            modal.style.transform = 'translateY(-20px)';
            setTimeout(() => {
                document.body.removeChild(overlay);
                // 重新加载预设网址选择器，确保主界面更新
                if (typeof initCustomUrlSelect === 'function') {
                    initCustomUrlSelect();
                }
            }, 300);
        }

        // 事件监听器
        document.getElementById('customUrlsCloseBtn').addEventListener('click', closeCustomUrlsModal);
        document.getElementById('saveUrlsBtn').addEventListener('click', closeCustomUrlsModal); // 保存操作已在添加/删除时实时生效，这里仅作为关闭按钮

        // 渲染列表函数
        async function renderUrlsList() {
            urlsList.innerHTML = '';
            const urls = await GetPresetURLs();

            if (Object.keys(urls).length === 0) {
                urlsList.innerHTML = '<div style="padding: 16px; text-align: center; color: #9ca3af; font-size: 13px;">暂无自定义网址</div>';
                return;
            }

            Object.entries(urls).forEach(([name, url]) => {
                const item = document.createElement('div');
                item.className = 'url-item';
                item.innerHTML = `
                    <div class="url-item-info">
                        <div class="url-item-name">${name}</div>
                        <div class="url-item-value">${url}</div>
                    </div>
                    <button class="setting-btn secondary" data-name="${name}" style="padding: 4px 8px; min-width: auto; height: 28px;">删除</button>
                `;

                // 删除事件
                item.querySelector('button').addEventListener('click', async () => {
                    if (await showConfirm('删除网址', `确定要删除 "${name}" 吗？`)) {
                        try {

                            await DeleteCustomURL(name);
                            await renderUrlsList(); // 重新渲染
                        } catch (e) {
                            showError('删除失败: ' + e.message);
                        }
                    }
                });

                urlsList.appendChild(item);
            });
        }

        // 初始渲染
        await renderUrlsList();

        // 添加按钮事件
        document.getElementById('addUrlBtn').addEventListener('click', async () => {
            const nameInput = document.getElementById('newUrlName');
            const urlInput = document.getElementById('newUrlValue');

            const name = nameInput.value.trim();
            let url = urlInput.value.trim();

            if (!name || !url) {
                showError('请填写名称和网址');
                return;
            }

            if (!url.startsWith('http://') && !url.startsWith('https://')) {
                url = 'https://' + url;
            }

            try {
                // 检查是否已存在同名网址
                const currentUrls = await GetPresetURLs();
                if (currentUrls && currentUrls[name]) {
                    const confirmed = await showConfirm('重名提醒', `网址名称 "${name}" 已存在，确定要覆盖它吗？`);
                    if (!confirmed) return;
                }

                await SaveCustomURL(name, url);

                // 清空输入并刷新
                nameInput.value = '';
                urlInput.value = '';
                await renderUrlsList();
                showSuccess('网址添加成功');
            } catch (e) {
                showError('添加失败: ' + e.message);
            }
        });

        // 点击遮罩层关闭
        overlay.addEventListener('click', (e) => {
            if (e.target === overlay) {
                closeCustomUrlsModal();
            }
        });

    } catch (error) {
        console.error('Failed to show custom URLs modal:', error);
        showError('无法打开网址管理: ' + error.message);
    }
}

// 应用程序启动
if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
} else {
    init();
}

/**
 * 显示随机数字输入对话框
 */
async function showRandomNumberDialog(selectedWindows) {
    // 先读取保存的配置
    let savedConfig = null;
    try {

        savedConfig = await GetRandomInputConfig();
    } catch (error) {
        debugLog('读取随机输入配置失败，使用默认值:', error);
    }

    // 设置默认值或使用保存的配置
    const defaultMinValue = savedConfig?.minValue ?? 1;
    const defaultMaxValue = savedConfig?.maxValue ?? 100;
    const defaultOverwrite = savedConfig?.overwrite ?? true;
    const defaultDelayed = savedConfig?.delayed ?? false;

    const modalHTML = `
        <div class="settings-modal-overlay" id="randomNumberModalOverlay">
            <div class="settings-modal" style="width: 450px; height: 400px;">
                <div class="settings-titlebar">
                    <div class="titlebar-content">
                        <h1 class="settings-title">随机数字输入</h1>
                        <button class="modal-close-btn" id="randomModalCloseBtn">×</button>
                    </div>
                </div>
                
                <div class="settings-content">
                    <div class="settings-section">
                        <h2 class="section-title">数字范围</h2>
                        <div class="setting-item">
                            <div style="display: flex; align-items: center; gap: 15px;">
                                <div style="display: flex; align-items: center; gap: 5px;">
                                    <label class="setting-label">最小值:</label>
                                    <input type="text" id="randomMinValue" class="setting-input" value="${defaultMinValue}" style="width: 160px;" autocomplete="off" placeholder="如 1 或 1.5">
                                </div>
                                <div style="display: flex; align-items: center; gap: 5px;">
                                    <label class="setting-label">最大值:</label>
                                    <input type="text" id="randomMaxValue" class="setting-input" value="${defaultMaxValue}" style="width: 160px;" autocomplete="off" placeholder="如 100 或 100.5">
                                </div>
                            </div>
                            <div style="margin-top: 8px; font-size: 12px; color: #666;">
                                提示：输入整数生成整数（如 1-100），输入小数生成小数（如 1.5-10.8）
                            </div>
                        </div>
                    </div>

                    <div class="settings-section">
                        <h2 class="section-title">输入选项</h2>
                        <div class="setting-item">
                            <div class="setting-control">
                                <label class="checkbox-label">
                                    <input type="checkbox" id="randomOverwrite" ${defaultOverwrite ? 'checked' : ''}>
                                    <span>覆盖原有内容</span>
                                </label>
                            </div>
                            <div class="setting-control">
                                <label class="checkbox-label">
                                    <input type="checkbox" id="randomDelayed" ${defaultDelayed ? 'checked' : ''}>
                                    <span>模拟人工输入（逐字输入并添加延迟）</span>
                                </label>
                            </div>
                        </div>
                    </div>

                    <div class="settings-actions">
                        <button class="setting-btn primary" id="randomInputConfirm">开始输入</button>
                        <button class="setting-btn gray" id="randomInputCancel">取消</button>
                    </div>
                </div>
            </div>
        </div>
    `;

    document.body.insertAdjacentHTML('beforeend', modalHTML);
    const overlay = document.getElementById('randomNumberModalOverlay');

    // 绑定事件
    const closeBtn = document.getElementById('randomModalCloseBtn');
    const cancelBtn = document.getElementById('randomInputCancel');
    const confirmBtn = document.getElementById('randomInputConfirm');

    // 关闭对话框
    const closeDialog = () => {
        document.body.removeChild(overlay);
    };

    let isDragging = false;
    let dragStartedInDialog = false;

    // 监听拖选开始
    overlay.addEventListener('mousedown', (e) => {
        // 检查是否在对话框内部开始拖选
        const modal = overlay.querySelector('.settings-modal');
        if (modal && modal.contains(e.target)) {
            dragStartedInDialog = true;
        }
        isDragging = true;
    });

    // 监听拖选结束
    document.addEventListener('mouseup', () => {
        isDragging = false;
        dragStartedInDialog = false;
    });

    closeBtn.addEventListener('click', closeDialog);
    cancelBtn.addEventListener('click', closeDialog);

    // 修改点击外部关闭的逻辑，防止拖选时误关闭
    overlay.addEventListener('click', (e) => {
        if (e.target === overlay && !isDragging && !dragStartedInDialog) {
            closeDialog();
        }
    });

    // 确认输入 - 和指定文本输入保持一致的处理方式
    confirmBtn.addEventListener('click', async () => {
        try {
            const minStr = document.getElementById('randomMinValue').value.trim();
            const maxStr = document.getElementById('randomMaxValue').value.trim();
            const overwrite = document.getElementById('randomOverwrite').checked;
            const delayed = document.getElementById('randomDelayed').checked;

            if (!minStr || !maxStr) {
                showError('请输入有效的数字范围');
                return;
            }

            // 自动检测是整数还是小数（模拟旧程序逻辑）
            const isFloat = minStr.includes('.') || maxStr.includes('.');
            let minValue, maxValue, decimalPlaces = 2;

            try {
                if (isFloat) {
                    minValue = parseFloat(minStr);
                    maxValue = parseFloat(maxStr);
                    // 自动计算小数位数
                    const minDecimalPlaces = minStr.includes('.') ? minStr.split('.')[1].length : 0;
                    const maxDecimalPlaces = maxStr.includes('.') ? maxStr.split('.')[1].length : 0;
                    decimalPlaces = Math.max(minDecimalPlaces, maxDecimalPlaces);
                    decimalPlaces = Math.min(decimalPlaces, 10); // 最多10位小数
                } else {
                    minValue = parseInt(minStr);
                    maxValue = parseInt(maxStr);
                }
            } catch (error) {
                showError('请输入有效的数字范围');
                return;
            }

            if (isNaN(minValue) || isNaN(maxValue)) {
                showError('请输入有效的数字范围');
                return;
            }

            if (maxValue <= minValue) {
                showError('最大值必须大于最小值');
                return;
            }

            const config = {
                minValue: minValue,
                maxValue: maxValue,
                isFloat: isFloat,
                decimalPlaces: decimalPlaces,
                overwrite: overwrite,
                delayed: delayed
            };

            const windowNumbers = selectedWindows.map(w => w.id);

            // 和指定文本输入一样，等待操作完成


            // 先保存配置（和文本输入保持一致的错误处理）
            await SaveRandomInputConfig(config);
            debugLog('随机输入配置已保存');

            // 执行输入操作（直接调用，和文本输入保持一致）
            await InputRandomNumbers(windowNumbers, config);

            showSuccess(`成功为 ${selectedWindows.length} 个窗口输入随机数字`);
            closeDialog();
        } catch (error) {
            console.error('随机数字输入失败:', error);
            showError('输入失败: ' + error.message);
        }
    });
}

/**
 * 显示文本输入对话框
 */
async function showTextInputDialog(selectedWindows) {
    const modalHTML = `
        <div class="settings-modal-overlay" id="textInputModalOverlay">
            <div class="settings-modal" style="width: 550px; height: 500px;">
                <div class="settings-titlebar">
                    <div class="titlebar-content">
                        <h1 class="settings-title">指定文本输入</h1>
                        <button class="modal-close-btn" id="textModalCloseBtn">×</button>
                    </div>
                </div>
                
                <div class="settings-content">
                    <div class="settings-section">
                        <h2 class="section-title">文本文件</h2>
                        <div class="setting-item">
                            <div style="display: flex; align-items: center; gap: 8px;">
                                <input type="text" id="textFilePath" class="setting-input" placeholder="选择文本文件或直接在下方预览框输入内容..." readonly style="flex: 1;" autocomplete="off">
                                <button class="browse-btn" id="textFileBrowse" style="flex-shrink: 0;">浏览</button>
                            </div>
                            <input type="file" id="textFileInput" accept=".txt,.text" style="display: none;">
                        </div>
                    </div>

                    <div class="settings-section">
                        <h2 class="section-title">文本内容（支持直接编辑）</h2>
                        <div class="setting-item">
                            <textarea id="textFilePreview" class="setting-input" 
                                style="width: 100%; max-width: 100%; height: 120px; resize: vertical; font-family: monospace; font-size: 12px; box-sizing: border-box;" 
                                placeholder="可以选择文件加载，或直接在此输入/粘贴文本内容（每行一条）..."></textarea>
                        </div>
                    </div>

                    <div class="settings-section">
                        <h2 class="section-title">输入方式</h2>
                        <div class="setting-item">
                            <div class="setting-control">
                                <label class="checkbox-label">
                                    <input type="radio" name="inputMethod" value="sequence" checked id="textSequential">
                                    <span>顺序输入（按行依次分配给各窗口）</span>
                                </label>
                            </div>
                            <div class="setting-control">
                                <label class="checkbox-label">
                                    <input type="radio" name="inputMethod" value="random" id="textRandom">
                                    <span>随机输入（随机选择行分配给各窗口）</span>
                                </label>
                            </div>
                        </div>
                    </div>

                    <div class="settings-section">
                        <h2 class="section-title">输入选项</h2>
                        <div class="setting-item">
                            <div class="setting-control">
                                <label class="checkbox-label">
                                    <input type="checkbox" id="textOverwrite" checked>
                                    <span>覆盖原有内容</span>
                                </label>
                            </div>
                            <div class="setting-control">
                                <label class="checkbox-label">
                                    <input type="checkbox" id="textDelayed">
                                    <span>模拟人工输入（逐字输入并添加延迟）</span>
                                </label>
                            </div>
                        </div>
                    </div>

                    <div class="settings-actions">
                        <button class="setting-btn primary" id="textInputConfirm">开始输入</button>
                        <button class="setting-btn gray" id="textInputCancel">取消</button>
                    </div>
                </div>
            </div>
        </div>
    `;

    document.body.insertAdjacentHTML('beforeend', modalHTML);
    const overlay = document.getElementById('textInputModalOverlay');

    // 绑定事件
    const closeBtn = document.getElementById('textModalCloseBtn');
    const cancelBtn = document.getElementById('textInputCancel');
    const confirmBtn = document.getElementById('textInputConfirm');
    const browseBtn = document.getElementById('textFileBrowse');
    const fileInput = document.getElementById('textFileInput');
    const filePathInput = document.getElementById('textFilePath');
    const previewTextarea = document.getElementById('textFilePreview');

    let selectedFilePath = '';

    // 关闭对话框
    const closeDialog = () => {
        document.body.removeChild(overlay);
    };

    let isDragging = false;
    let dragStartedInDialog = false;

    // 监听拖选开始
    overlay.addEventListener('mousedown', (e) => {
        // 检查是否在对话框内部开始拖选
        const modal = overlay.querySelector('.settings-modal');
        if (modal && modal.contains(e.target)) {
            dragStartedInDialog = true;
        }
        isDragging = true;
    });

    // 监听拖选结束
    document.addEventListener('mouseup', () => {
        isDragging = false;
        dragStartedInDialog = false;
    });

    // 文件选择 - 使用后端API（在Wails中原生input可能不工作）
    browseBtn.addEventListener('click', async (e) => {
        e.preventDefault();
        e.stopPropagation();
        debugLog('[TextInput] Browse button clicked!');

        try {
            debugLog('[TextInput] Importing SelectFile...');

            debugLog('[TextInput] Calling SelectFile...');
            const filePath = await SelectFile("选择文本文件", "Text Files|*.txt");
            debugLog('[TextInput] SelectFile returned:', filePath);

            if (filePath && filePath.trim() !== '') {
                selectedFilePath = filePath;
                // 提取文件名显示
                const fileName = filePath.split('/').pop() || filePath.split('\\').pop() || filePath;
                filePathInput.value = `已选择文件: ${fileName}`;

                // 尝试使用后端获取文件预览内容
                try {

                    const lines = await GetFilePreview(filePath);
                    if (lines && Array.isArray(lines) && lines.length > 0) {
                        previewTextarea.value = lines.join('\n');
                    } else {
                        previewTextarea.value = '(无法读取文件内容，请手动输入)';
                    }
                } catch (readError) {
                    console.error('读取文件内容失败:', readError);
                    previewTextarea.value = '读取文件失败: ' + readError.message;
                }
            }
        } catch (error) {
            console.error('选择文件失败:', error);
            // 用户取消不显示错误
            if (!error.message?.includes('cancelled')) {
                showError('选择文件失败: ' + error.message);
            }
        }
    });

    // 保留原生file input作为备用
    fileInput.addEventListener('change', async (e) => {
        const file = e.target.files[0];
        if (file) {
            selectedFilePath = file.name; // 只显示文件名
            filePathInput.value = `已选择文件: ${file.name}`;

            // 读取文件内容并显示到预览框
            try {
                const reader = new FileReader();
                reader.onload = function (event) {
                    const content = event.target.result;
                    previewTextarea.value = content;
                };
                reader.readAsText(file, 'utf-8');
            } catch (error) {
                console.error('读取文件失败:', error);
                previewTextarea.value = '读取文件失败: ' + error.message;
            }
        }
    });

    closeBtn.addEventListener('click', closeDialog);
    cancelBtn.addEventListener('click', closeDialog);

    // 修改点击外部关闭的逻辑，防止拖选时误关闭
    overlay.addEventListener('click', (e) => {
        if (e.target === overlay && !isDragging && !dragStartedInDialog) {
            closeDialog();
        }
    });

    // 确认输入
    confirmBtn.addEventListener('click', async () => {
        try {
            // 检查预览框是否有内容
            const textContent = previewTextarea.value.trim();
            if (!textContent) {
                showError('请选择文件或直接输入文本内容');
                return;
            }

            const inputMethod = document.querySelector('input[name="inputMethod"]:checked').value;
            const overwrite = document.getElementById('textOverwrite').checked;
            const delayed = document.getElementById('textDelayed').checked;

            // 将预览框内容按行分割
            const lines = textContent.split('\n').map(line => line.trim()).filter(line => line.length > 0);
            if (lines.length === 0) {
                showError('文本内容为空或无有效行');
                return;
            }

            // 直接使用预览框内容进行输入，而不是通过文件
            const windowNumbers = selectedWindows.map(w => w.id);

            // 调用后端API进行文本输入

            if (InputTextFromLines) {
                // 如果有新的API，使用行数组直接输入
                await InputTextFromLines(windowNumbers, lines, inputMethod, overwrite, delayed);
            } else {
                // 回退到原来的文件API，创建临时内容

                const config = {
                    filePath: 'DIRECT_CONTENT',
                    inputMethod: inputMethod,
                    overwrite: overwrite,
                    delayed: delayed,
                    directContent: lines  // 添加直接内容
                };
                await InputTextFromFile(windowNumbers, config);
            }

            showSuccess(`成功为 ${selectedWindows.length} 个窗口输入文本（共 ${lines.length} 行内容）`);
            closeDialog();
        } catch (error) {
            console.error('文本输入失败:', error);
            showError('输入失败: ' + error.message);
        }
    });
}
