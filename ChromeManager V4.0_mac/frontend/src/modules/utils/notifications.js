// 通知系统模块
// 负责显示各种类型的通知消息

// 用于跟踪当前显示的通知数量，以便正确堆叠
let notificationCount = 0;

/**
 * 显示通知消息
 * @param {string} message 通知消息内容
 * @param {string} type 通知类型: 'info', 'success', 'warning', 'error'
 * @param {number} duration 显示持续时间(毫秒)，默认3000
 */
export function showNotification(message, type = 'info', duration = 3000) {
    // 创建通知元素
    const notification = document.createElement('div');
    notification.className = `notification ${type}`;
    notification.textContent = message;
    
    // 计算通知的垂直位置（支持多个通知堆叠）
    const notificationHeight = 60; // 估算通知高度
    const spacing = 10; // 通知之间的间距
    const bottomOffset = 30 + (notificationCount * (notificationHeight + spacing));
    
    // 添加样式
    notification.style.cssText = `
        position: fixed;
        bottom: ${bottomOffset}px;
        right: 20px;
        padding: 12px 24px;
        border-radius: 6px;
        color: white;
        font-size: 14px;
        z-index: 10000;
        max-width: 300px;
        word-wrap: break-word;
        opacity: 0;
        transform: translateX(100%);
        transition: all 0.3s ease;
        cursor: pointer;
    `;
    
    // 设置不同类型的颜色
    switch (type) {
        case 'success':
            notification.style.backgroundColor = '#10b981';
            break;
        case 'error':
            notification.style.backgroundColor = '#ef4444';
            break;
        case 'warning':
            notification.style.backgroundColor = '#f59e0b';
            break;
        default:
            notification.style.backgroundColor = '#3b82f6';
    }
    
    // 添加到页面
    document.body.appendChild(notification);
    
    // 增加通知计数
    notificationCount++;
    notification.dataset.notificationIndex = notificationCount - 1;
    
    // 点击关闭功能
    notification.addEventListener('click', () => {
        hideNotification(notification);
    });
    
    // 显示动画
    setTimeout(() => {
        notification.style.opacity = '1';
        notification.style.transform = 'translateX(0)';
    }, 100);
    
    // 自动消失
    setTimeout(() => {
        hideNotification(notification);
    }, duration);
    
    return notification;
}

/**
 * 隐藏通知
 * @param {HTMLElement} notification 通知元素
 */
function hideNotification(notification) {
    if (!notification || !notification.parentNode) return;
    
    notification.style.opacity = '0';
    notification.style.transform = 'translateX(100%)';
    
    // 移除元素
    setTimeout(() => {
        if (notification.parentNode) {
            notification.parentNode.removeChild(notification);
            // 减少通知计数
            notificationCount = Math.max(0, notificationCount - 1);
            // 重新调整剩余通知的位置
            adjustNotificationPositions();
        }
    }, 300);
}

/**
 * 重新调整所有通知的位置
 */
function adjustNotificationPositions() {
    const notifications = document.querySelectorAll('.notification');
    notifications.forEach((notification, index) => {
        const notificationHeight = 60;
        const spacing = 10;
        const bottomOffset = 30 + (index * (notificationHeight + spacing));
        notification.style.bottom = `${bottomOffset}px`;
        notification.dataset.notificationIndex = index;
    });
}

/**
 * 显示成功消息
 * @param {string} message 消息内容
 */
export function showSuccess(message) {
    return showNotification(message, 'success');
}

/**
 * 显示错误消息
 * @param {string} message 消息内容
 */
export function showError(message) {
    return showNotification(message, 'error');
}

/**
 * 显示警告消息
 * @param {string} message 消息内容
 */
export function showWarning(message) {
    return showNotification(message, 'warning');
}

/**
 * 显示信息消息
 * @param {string} message 消息内容
 */
export function showInfo(message) {
    return showNotification(message, 'info');
}

/**
 * 清除所有通知
 */
export function clearAllNotifications() {
    const notifications = document.querySelectorAll('.notification');
    notifications.forEach(notification => {
        hideNotification(notification);
    });
    // 重置计数器
    notificationCount = 0;
} 