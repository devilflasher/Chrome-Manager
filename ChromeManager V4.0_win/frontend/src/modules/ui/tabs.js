// 标签页管理模块
// 负责管理应用程序的标签页切换功能

/**
 * 初始化标签页功能
 */
export function initTabs() {
    
    
    // 获取所有标签页按钮和内容
    const tabButtons = document.querySelectorAll('input[name="tab-radio"]');
    const tabPanes = document.querySelectorAll('.tab-pane');
    const selection = document.querySelector('.selection');
    
    
    
    
    if (tabButtons.length === 0) {
        console.error('No tab buttons found!');
        return false;
    }
    
    // 绑定标签页切换事件
    tabButtons.forEach((button, index) => {
        button.addEventListener('change', function() {
            if (this.checked) {
                switchToTab(this.dataset.tab, index);
            }
        });
    });
    
    // 确保第一个标签页被激活
    if (tabButtons[0]) {
        tabButtons[0].checked = true;
        switchToTab(tabButtons[0].dataset.tab, 0);
    }
    
    
    return true;
}

/**
 * 切换到指定标签页
 * @param {string} tabId 标签页ID
 * @param {number} tabIndex 标签页索引
 */
export function switchToTab(tabId, tabIndex) {
    
    
    // 隐藏所有标签页内容
    const allPanes = document.querySelectorAll('.tab-pane');
    allPanes.forEach(pane => {
        pane.classList.remove('active');
        pane.style.display = 'none';
    });
    
    // 显示目标标签页
    const targetPane = document.getElementById(tabId);
    if (targetPane) {
        targetPane.classList.add('active');
        targetPane.style.display = 'block';
        
    } else {
        console.error('Target tab pane not found:', tabId);
        return false;
    }
    
    // 更新选择器位置
    updateTabSelector(tabIndex);
    
    return true;
}

/**
 * 更新标签页选择器位置
 * @param {number} tabIndex 标签页索引
 */
function updateTabSelector(tabIndex) {
    const selection = document.querySelector('.selection');
    const labels = document.querySelectorAll('.radio-input label');
    
    if (selection && labels.length > 0) {
        // 移除所有label的active类
        labels.forEach(label => label.classList.remove('active'));
        
        // 添加当前label的active类
        if (labels[tabIndex]) {
            labels[tabIndex].classList.add('active');
        }
        
        // 计算并设置选择器位置
        const itemWidth = 100 / labels.length;
        const leftPosition = tabIndex * itemWidth;
        selection.style.width = `${itemWidth}%`;
        selection.style.left = `${leftPosition}%`;
        
    }
}

/**
 * 获取当前激活的标签页
 * @returns {Object|null} 当前标签页信息
 */
export function getCurrentTab() {
    const activeTabButton = document.querySelector('input[name="tab-radio"]:checked');
    if (activeTabButton) {
        return {
            id: activeTabButton.dataset.tab,
            index: Array.from(document.querySelectorAll('input[name="tab-radio"]')).indexOf(activeTabButton),
            element: activeTabButton
        };
    }
    return null;
}

/**
 * 切换到下一个标签页
 * @returns {boolean} 是否成功切换
 */
export function nextTab() {
    const currentTab = getCurrentTab();
    if (!currentTab) return false;
    
    const tabButtons = document.querySelectorAll('input[name="tab-radio"]');
    const nextIndex = (currentTab.index + 1) % tabButtons.length;
    const nextButton = tabButtons[nextIndex];
    
    if (nextButton) {
        nextButton.checked = true;
        switchToTab(nextButton.dataset.tab, nextIndex);
        return true;
    }
    
    return false;
}

/**
 * 切换到上一个标签页
 * @returns {boolean} 是否成功切换
 */
export function prevTab() {
    const currentTab = getCurrentTab();
    if (!currentTab) return false;
    
    const tabButtons = document.querySelectorAll('input[name="tab-radio"]');
    const prevIndex = (currentTab.index - 1 + tabButtons.length) % tabButtons.length;
    const prevButton = tabButtons[prevIndex];
    
    if (prevButton) {
        prevButton.checked = true;
        switchToTab(prevButton.dataset.tab, prevIndex);
        return true;
    }
    
    return false;
}

/**
 * 根据ID切换到标签页
 * @param {string} tabId 标签页ID
 * @returns {boolean} 是否成功切换
 */
export function switchToTabById(tabId) {
    const tabButton = document.querySelector(`input[name="tab-radio"][data-tab="${tabId}"]`);
    if (tabButton) {
        const tabButtons = document.querySelectorAll('input[name="tab-radio"]');
        const tabIndex = Array.from(tabButtons).indexOf(tabButton);
        
        tabButton.checked = true;
        switchToTab(tabId, tabIndex);
        return true;
    }
    
    console.warn(`Tab with ID "${tabId}" not found`);
    return false;
} 
