// DOM元素管理模块
// 负责管理所有DOM元素的引用

/**
 * 获取所有DOM元素的引用
 * @returns {Object} 包含所有DOM元素引用的对象
 */
export function getDOMElements() {
    return {
        // 窗口控制按钮
        minimizeBtn: document.getElementById('minimize-btn'),
        maximizeBtn: document.getElementById('maximize-btn'),
        closeBtn: document.getElementById('close-btn'),
        
        // 工具栏按钮
        importWindowsBtn: document.getElementById('import-windows-btn'),
        configureAllBtn: document.getElementById('configure-all-btn'),
        autoArrangeBtn: document.getElementById('auto-arrange-btn'),
        closeWindowsBtn: document.getElementById('close-windows-btn'),
        syncToggleBtn: document.getElementById('sync-toggle-btn'),
        settingsBtn: document.getElementById('settings-btn'),
        
        // 窗口列表
        windowTableContent: document.getElementById('window-table-content'),
        
        // 配置表单
        startX: document.getElementById('startX'),
        startY: document.getElementById('startY'),
        width: document.getElementById('width'),
        height: document.getElementById('height'),
        horizontalSpacing: document.getElementById('horizontalSpacing'),
        verticalSpacing: document.getElementById('verticalSpacing'),
        windowsPerRow: document.getElementById('windowsPerRow'),
        customArrangeBtn: document.getElementById('custom-arrange-btn'),
        
        // 打开窗口标签页
        windowRange: document.getElementById('window-range'),
        openWindowBtn: document.getElementById('open-window-btn'),
        
        // 批量打开网页标签页
        urlInput: document.getElementById('url-input'),
        batchOpenBtn: document.getElementById('batch-open-btn'),
        customUrlSelect: document.getElementById('custom-url-select'),
        manageUrlsBtn: document.getElementById('manage-urls-btn'),
        
        // 标签页管理按钮
        keepCurrentTabBtn: document.getElementById('keep-current-tab-btn'),
        keepNewTabBtn: document.getElementById('keep-new-tab-btn'),

        // 批量文本输入按钮
        randomNumberBtn: document.getElementById('random-number-btn'),
        textInputBtn: document.getElementById('text-input-btn'),

        
        // 批量创建环境
        envNumbers: document.getElementById('env-numbers'),
        createEnvBtn: document.getElementById('create-env-btn'),

        // 浏览器缓存清理
        browserCacheCleanupBtn: document.getElementById('browser-cache-clean-btn'),
        browserCacheGroupSelect: document.getElementById('browser-cache-group-select'),
        browserCacheWindowRange: document.getElementById('browser-cache-window-range'),

        // 自动化
        automationOpenclawBtn: document.getElementById('automation-openclaw-btn'),
        automationHermesBtn: document.getElementById('automation-hermes-btn')
    };
}

/**
 * 验证关键DOM元素是否存在
 * @param {Object} elements DOM元素对象
 * @returns {Array} 缺失的元素名称列表
 */
export function validateElements(elements) {
    const missing = [];
    const criticalElements = [
        'settingsBtn',
        'openWindowBtn',
        'windowRange',
        'windowTableContent'
    ];
    
    criticalElements.forEach(elementName => {
        if (!elements[elementName]) {
            missing.push(elementName);
            console.error(`Critical element not found: ${elementName}`);
        }
    });
    
    return missing;
}

/**
 * 获取单个DOM元素
 * @param {string} id 元素ID
 * @returns {HTMLElement|null} DOM元素或null
 */
export function getElement(id) {
    const element = document.getElementById(id);
    if (!element) {
        console.warn(`Element not found: ${id}`);
    }
    return element;
}

/**
 * 安全地为元素添加事件监听器
 * @param {HTMLElement} element DOM元素
 * @param {string} event 事件名称
 * @param {Function} handler 事件处理函数
 * @param {Object} options 事件监听器选项
 */
export function safeAddEventListener(element, event, handler, options = {}) {
    if (element && typeof handler === 'function') {
        element.addEventListener(event, handler, options);
    } else {
        console.warn('Cannot add event listener: element or handler is invalid');
    }
}

/**
 * 批量为元素添加事件监听器
 * @param {Object} elementEventMap 元素和事件的映射
 */
export function addEventListeners(elementEventMap) {
    Object.entries(elementEventMap).forEach(([elementName, events]) => {
        const element = getElement(elementName);
        if (element) {
            Object.entries(events).forEach(([eventType, handler]) => {
                safeAddEventListener(element, eventType, handler);
            });
        }
    });
} 
