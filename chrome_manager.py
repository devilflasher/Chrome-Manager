import tkinter as tk
from tkinter import ttk, messagebox
import os
import subprocess
import win32gui
import win32process
import win32con
import win32api
import win32com.client
import json
from typing import List, Dict, Optional
import math
import ctypes
from ctypes import wintypes
import threading
import time
import sys
import keyboard
import mouse
import webbrowser
import sv_ttk

def is_admin():
    # 检查是否具有管理员权限
    try:
        return ctypes.windll.shell32.IsUserAnAdmin()
    except:
        return False

def run_as_admin():
    # 以管理员权限重新运行程序
    ctypes.windll.shell32.ShellExecuteW(None, "runas", sys.executable, " ".join(sys.argv), None, 1)

class ChromeManager:
    def __init__(self):
        
        if not is_admin():
            if messagebox.askyesno("权限不足", "需要管理员权限才能运行同步功能。\n是否以管理员身份重新启动程序？"):
                run_as_admin()
                sys.exit()
                
        self.root = tk.Tk()
        self.root.title("NoBiggie社区Chrome多窗口管理器 V1.0")
        
        try:
            icon_path = os.path.join(os.path.dirname(__file__), "app.ico")
            if os.path.exists(icon_path):
                self.root.iconbitmap(icon_path)
        except Exception as e:
            print(f"设置图标失败: {str(e)}")
        
        last_position = self.load_window_position()
        if last_position:
            self.root.geometry(last_position)
        
        sv_ttk.set_theme("light")  # 使用 light 主题
        
        self.window_list = None  # 先初始化为 None
        self.windows = []
        self.master_window = None
        self.shortcut_path = self.load_settings().get('shortcut_path', '')
        self.shell = win32com.client.Dispatch("WScript.Shell")
        self.select_all_var = tk.StringVar(value="全部选择")
        
        self.is_syncing = False
        self.sync_button = None
        self.mouse_hook_id = None
        self.keyboard_hook = None
        self.hook_thread = None
        self.user32 = ctypes.WinDLL('user32', use_last_error=True)
        self.sync_windows = []
        
        self.chrome_drivers = {}
        self.debug_ports = {}
        self.base_debug_port = 9222
        
        self.DWMWA_BORDER_COLOR = 34
        self.DWM_MAGIC_COLOR = 0x00FF0000
        
        self.popup_mappings = {}
        
        self.popup_monitor_thread = None
        
        self.mouse_threshold = 3
        self.last_mouse_position = (0, 0)
        self.last_move_time = 0
        self.move_interval = 0.016
        
        self.shortcut_hook = None
        self.current_shortcut = None
        
        # 从设置中加载快捷键
        settings = self.load_settings()
        if 'sync_shortcut' in settings:
            self.set_shortcut(settings['sync_shortcut'])
        
        self.root.protocol("WM_DELETE_WINDOW", self.on_closing)

        # 创建界面
        self.create_widgets()  
        self.create_styles()   
        
       
        self.root.update()
        current_width = self.root.winfo_width()
        current_height = self.root.winfo_height()
        self.root.geometry(f"{current_width}x{current_height}")
        self.root.resizable(False, False)

    def create_styles(self):
        style = ttk.Style()
        
        default_font = ('Microsoft YaHei UI', 9)
        
        style.configure('Small.TEntry',
            padding=(4, 0),
            font=default_font
        )
                
        style.configure('TButton', font=default_font)
        style.configure('TLabel', font=default_font)
        style.configure('TEntry', font=default_font)
        style.configure('Treeview', font=default_font)
        style.configure('Treeview.Heading', font=default_font)
        style.configure('TLabelframe.Label', font=default_font)
        style.configure('TNotebook.Tab', font=default_font)
        
        if self.window_list:
            self.window_list.tag_configure("master", 
                background="#0d6efd",
                foreground='white'
            )
        
        # 链接样式
        style.configure('Link.TLabel',
            foreground='#0d6efd',
            cursor='hand2',
            font=('Microsoft YaHei UI', 9, 'underline')
        )

    def create_widgets(self):
        # 创建界面元素
        main_frame = ttk.Frame(self.root)
        main_frame.pack(fill=tk.X, padx=10, pady=5)
        
        upper_frame = ttk.Frame(main_frame)
        upper_frame.pack(fill=tk.X)
        
        arrange_frame = ttk.LabelFrame(upper_frame, text="自定义排列")
        arrange_frame.pack(side=tk.RIGHT, fill=tk.Y, padx=(3, 0))
        
        manage_frame = ttk.LabelFrame(upper_frame, text="窗口管理")
        manage_frame.pack(side=tk.LEFT, fill=tk.BOTH, expand=True, padx=(0, 5))
        
        button_frame = ttk.Frame(manage_frame)
        button_frame.pack(fill=tk.X)
        
        ttk.Button(button_frame, text="导入窗口", command=self.import_windows, style='Accent.TButton').pack(side=tk.LEFT, padx=2)
        select_all_label = ttk.Label(button_frame, textvariable=self.select_all_var, style='Link.TLabel')
        select_all_label.pack(side=tk.LEFT, padx=5)
        select_all_label.bind('<Button-1>', self.toggle_select_all)
        ttk.Button(button_frame, text="自动排列", command=self.auto_arrange_windows).pack(side=tk.LEFT, padx=2)
        ttk.Button(button_frame, text="关闭选中", command=self.close_selected_windows).pack(side=tk.LEFT, padx=2)
        
        self.sync_button = ttk.Button(
            button_frame, 
            text="▶ 开始同步",
            command=self.toggle_sync,
            style='Accent.TButton'
        )
        self.sync_button.pack(side=tk.LEFT, padx=5)
        
        ttk.Button(
            button_frame,
            text="快捷键",
            command=self.show_shortcut_dialog,
            style='Accent.TButton'
        ).pack(side=tk.LEFT, padx=5)
        
        # 在 button_frame 中添加屏幕选择下拉框
        screen_frame = ttk.Frame(button_frame)
        screen_frame.pack(side=tk.LEFT, padx=2)
        ttk.Label(screen_frame, text="屏幕:").pack(side=tk.LEFT)
        
        # 创建屏幕选择下拉框
        self.screen_var = tk.StringVar()
        self.screen_combo = ttk.Combobox(
            screen_frame, 
            textvariable=self.screen_var,
            width=8,
            state="readonly"
        )
        self.screen_combo.pack(side=tk.LEFT)
        
        # 获取并设置屏幕列表
        self.update_screen_list()
        
        list_frame = ttk.Frame(manage_frame)
        list_frame.pack(fill=tk.BOTH, expand=True, pady=2)
        
        # 创建窗口列表
        self.window_list = ttk.Treeview(list_frame, 
            columns=("select", "number", "title", "master", "hwnd"),
            show="headings", 
            height=4,  
            style='Accent.Treeview'
        )
        self.window_list.pack(side=tk.LEFT, fill=tk.BOTH, expand=True)
        
        self.window_list.heading("select", text="选择")
        self.window_list.heading("number", text="序号")
        self.window_list.heading("title", text="标题")
        self.window_list.heading("master", text="主控")
        self.window_list.heading("hwnd", text="")
        
        self.window_list.column("select", width=40, anchor="center")
        self.window_list.column("number", width=40, anchor="center")
        self.window_list.column("title", width=300)
        self.window_list.column("master", width=40, anchor="center")
        self.window_list.column("hwnd", width=0, stretch=False)  # 隐藏hwnd列
        
        self.window_list.tag_configure("master", background="lightblue")
        
        self.window_list.bind('<Button-1>', self.on_click)
        
        # 添加滚动条
        scrollbar = ttk.Scrollbar(list_frame, orient=tk.VERTICAL, command=self.window_list.yview)
        scrollbar.pack(side=tk.RIGHT, fill=tk.Y)
        self.window_list.configure(yscrollcommand=scrollbar.set)
        
        params_frame = ttk.Frame(arrange_frame)
        params_frame.pack(fill=tk.X, padx=5, pady=2)
        
        left_frame = ttk.Frame(params_frame)
        left_frame.pack(side=tk.LEFT, padx=(0, 5))
        right_frame = ttk.Frame(params_frame)
        right_frame.pack(side=tk.LEFT)
        
        ttk.Label(left_frame, text="起始X坐标").pack(anchor=tk.W)
        self.start_x = ttk.Entry(left_frame, width=8, style='Small.TEntry')
        self.start_x.pack(fill=tk.X, pady=(0, 2))
        self.start_x.insert(0, "0")
        
        ttk.Label(left_frame, text="窗口宽度").pack(anchor=tk.W)
        self.window_width = ttk.Entry(left_frame, width=8, style='Small.TEntry')
        self.window_width.pack(fill=tk.X, pady=(0, 2))
        self.window_width.insert(0, "500")
        
        ttk.Label(left_frame, text="水平间距").pack(anchor=tk.W)
        self.h_spacing = ttk.Entry(left_frame, width=8, style='Small.TEntry')
        self.h_spacing.pack(fill=tk.X, pady=(0, 2))
        self.h_spacing.insert(0, "0")
        
        ttk.Label(right_frame, text="起始Y坐标").pack(anchor=tk.W)
        self.start_y = ttk.Entry(right_frame, width=8, style='Small.TEntry')
        self.start_y.pack(fill=tk.X, pady=(0, 2))
        self.start_y.insert(0, "0")
        
        ttk.Label(right_frame, text="窗口高度").pack(anchor=tk.W)
        self.window_height = ttk.Entry(right_frame, width=8, style='Small.TEntry')
        self.window_height.pack(fill=tk.X, pady=(0, 2))
        self.window_height.insert(0, "400")
        
        ttk.Label(right_frame, text="垂直间距").pack(anchor=tk.W)
        self.v_spacing = ttk.Entry(right_frame, width=8, style='Small.TEntry')
        self.v_spacing.pack(fill=tk.X, pady=(0, 2))
        self.v_spacing.insert(0, "0")
        
        for widget in left_frame.winfo_children() + right_frame.winfo_children():
            if isinstance(widget, ttk.Entry):
                widget.pack_configure(pady=(0, 2))
        
        bottom_frame = ttk.Frame(arrange_frame)
        bottom_frame.pack(fill=tk.X, padx=5, pady=2)
        
        row_frame = ttk.Frame(bottom_frame)
        row_frame.pack(side=tk.LEFT)
        ttk.Label(row_frame, text="每行窗口数").pack(anchor=tk.W)
        self.windows_per_row = ttk.Entry(row_frame, width=8, style='Small.TEntry')
        self.windows_per_row.pack(pady=(2, 0))
        self.windows_per_row.insert(0, "5")
        
        ttk.Button(bottom_frame, text="自定义排列", 
            command=self.custom_arrange_windows,
            style='Accent.TButton'
        ).pack(side=tk.RIGHT, pady=(15, 0))
        
        bottom_frame = ttk.Frame(self.root)
        bottom_frame.pack(fill=tk.X, padx=10, pady=(5, 0))
        
        self.tab_control = ttk.Notebook(bottom_frame)
        self.tab_control.pack(side=tk.LEFT, fill=tk.X, expand=True)
        
        open_window_tab = ttk.Frame(self.tab_control)
        self.tab_control.add(open_window_tab, text="打开窗口")
        
        input_frame = ttk.Frame(open_window_tab)
        input_frame.pack(fill=tk.X, padx=5, pady=5)
        
        ttk.Label(input_frame, text="快捷方式目录:").pack(side=tk.LEFT)
        self.path_entry = ttk.Entry(input_frame)
        self.path_entry.pack(side=tk.LEFT, fill=tk.X, expand=True)
        self.path_entry.insert(0, self.shortcut_path)
        
        numbers_frame = ttk.Frame(input_frame)
        numbers_frame.pack(pady=5, padx=10, fill=tk.X)
        ttk.Label(numbers_frame, text="窗口编号:").pack(side=tk.LEFT)
        self.numbers_entry = ttk.Entry(numbers_frame)
        self.numbers_entry.pack(side=tk.LEFT, padx=5, fill=tk.X, expand=True)
        
        settings = self.load_settings()
        if 'last_window_numbers' in settings:
            self.numbers_entry.insert(0, settings['last_window_numbers'])
            
        self.numbers_entry.bind('<Return>', lambda e: self.open_windows())
        
        ttk.Button(
            numbers_frame,
            text="打开窗口",
            command=self.open_windows
        ).pack(side=tk.LEFT)
        
        url_tab = ttk.Frame(self.tab_control)
        self.tab_control.add(url_tab, text="批量打开网页")
        
        url_frame = ttk.Frame(url_tab)
        url_frame.pack(fill=tk.X, padx=5, pady=5)
        ttk.Label(url_frame, text="网址:").pack(side=tk.LEFT)
        self.url_entry = ttk.Entry(url_frame)
        self.url_entry.pack(side=tk.LEFT, fill=tk.X, expand=True)
        self.url_entry.insert(0, "www.google.com")
        
        self.url_entry.bind('<Return>', lambda e: self.batch_open_urls())
        
        ttk.Button(url_frame, text="批量打开", command=self.batch_open_urls).pack(side=tk.LEFT, padx=5)
        
        icon_tab = ttk.Frame(self.tab_control)
        self.tab_control.add(icon_tab, text="替换图标")
        
        icon_frame = ttk.Frame(icon_tab)
        icon_frame.pack(fill=tk.X, padx=5, pady=5)
        
        ttk.Label(icon_frame, text="图标目录:").pack(side=tk.LEFT)
        self.icon_path_entry = ttk.Entry(icon_frame)
        self.icon_path_entry.pack(side=tk.LEFT, fill=tk.X, expand=True)
        
        ttk.Label(icon_frame, text="窗口编号:").pack(side=tk.LEFT, padx=(10, 0))
        self.icon_window_numbers = ttk.Entry(icon_frame, width=15)
        self.icon_window_numbers.pack(side=tk.LEFT, padx=(0, 5))
        ttk.Label(icon_frame, text="示例: 1-5,7,9-12").pack(side=tk.LEFT)
        ttk.Button(icon_frame, text="替换图标", command=self.set_taskbar_icons).pack(side=tk.LEFT, padx=5)
        
        footer_frame = ttk.Frame(self.root)
        footer_frame.pack(side=tk.BOTTOM, fill=tk.X, padx=10, pady=5)

        author_frame = ttk.Frame(footer_frame)
        author_frame.pack(side=tk.RIGHT)

        ttk.Label(author_frame, text="Compiled by Devilflasher").pack(side=tk.LEFT)

        ttk.Label(author_frame, text="  ").pack(side=tk.LEFT)

        twitter_label = ttk.Label(
            author_frame, 
            text="Twitter",
            cursor="hand2",
            font=("Arial", 9)
        )
        twitter_label.pack(side=tk.LEFT)
        twitter_label.bind("<Button-1>", lambda e: webbrowser.open("https://x.com/DevilflasherX"))

        ttk.Label(author_frame, text="  ").pack(side=tk.LEFT)

        telegram_label = ttk.Label(
            author_frame, 
            text="Telegram",
            cursor="hand2",
            font=("Arial", 9)
        )
        telegram_label.pack(side=tk.LEFT)
        telegram_label.bind("<Button-1>", lambda e: webbrowser.open("https://t.me/devilflasher0"))

    def toggle_select_all(self, event=None):
        #切换全选状态
        try:
            items = self.window_list.get_children()
            if not items:
                return
                
            
            current_text = self.select_all_var.get()
            
            
            if current_text == "全部选择":
                
                for item in items:
                    self.window_list.set(item, "select", "√")
            else:  
                
                for item in items:
                    self.window_list.set(item, "select", "")
            
            # 更新按钮状态
            self.update_select_all_status()
            
        except Exception as e:
            print(f"切换全选状态失败: {str(e)}")

    def update_select_all_status(self):
        # 更新全选状态
        try:
            # 获取所有项目
            items = self.window_list.get_children()
            if not items:
                self.select_all_var.set("全部选择")
                return
            
            # 检查是否全部选中
            selected_count = sum(1 for item in items if self.window_list.set(item, "select") == "√")
            
            # 根据选中数量设置按钮文本
            if selected_count == len(items):
                self.select_all_var.set("取消全选")
            else:
                self.select_all_var.set("全部选择")
            
        except Exception as e:
            print(f"更新全选状态失败: {str(e)}")

    def on_click(self, event):
        # 处理点击事件
        try:
            region = self.window_list.identify_region(event.x, event.y)
            if region == "cell":
                column = self.window_list.identify_column(event.x)
                item = self.window_list.identify_row(event.y)
                
                if column == "#1":  # 选择列
                    current = self.window_list.set(item, "select")
                    self.window_list.set(item, "select", "" if current == "√" else "√")
                    # 更新全选按钮状态
                    self.update_select_all_status()
                elif column == "#4":  # 主控列
                    self.set_master_window(item)
        except Exception as e:
            print(f"处理点击事件失败: {str(e)}")

    def set_master_window(self, item):
        # 设置主控窗口
        if not item:
            return
        
        try:
            # 清除其他窗口的主控状态和标题
            for i in self.window_list.get_children():
                values = self.window_list.item(i)['values']
                if values and len(values) >= 5:
                    hwnd = int(values[4])
                    title = values[2]
                    if title.startswith("[主控]"):
                        new_title = title.replace("[主控]", "").strip()
                        win32gui.SetWindowText(hwnd, new_title)
                    # 恢复默认边框颜色
                    try:
                        ctypes.windll.dwmapi.DwmSetWindowAttribute(
                            hwnd,
                            self.DWMWA_BORDER_COLOR,
                            ctypes.byref(ctypes.c_int(0)),
                            ctypes.sizeof(ctypes.c_int)
                        )
                    except:
                        pass
                self.window_list.set(i, "master", "")
                self.window_list.item(i, tags=())
            
            # 设置新的主控窗口
            values = self.window_list.item(item)['values']
            self.master_window = int(values[4])
            
            # 设置主控标记和蓝色背景
            self.window_list.set(item, "master", "√")
            self.window_list.item(item, tags=("master",))
            
            # 修改窗口标题和边框颜色
            title = values[2]
            if not title.startswith("[主控]"):
                new_title = f"[主控] {title}"
                win32gui.SetWindowText(self.master_window, new_title)
                try:
                    # 设置红色边框
                    ctypes.windll.dwmapi.DwmSetWindowAttribute(
                        self.master_window,
                        self.DWMWA_BORDER_COLOR,
                        ctypes.byref(ctypes.c_int(0x000000FF)),
                        ctypes.sizeof(ctypes.c_int)
                    )
                except:
                    pass
            
        except Exception as e:
            print(f"设置主控窗口失败: {str(e)}")

    def toggle_sync(self, event=None):
        # 切换同步状态
        if not self.window_list.get_children():
            messagebox.showinfo("提示", "请先导入窗口！")
            return
        
        # 获取选中的窗口
        selected = []
        for item in self.window_list.get_children():
            if self.window_list.set(item, "select") == "√":
                selected.append(item)
        
        if not selected:
            messagebox.showinfo("提示", "请选择要同步的窗口！")
            return
        
        # 检查主控窗口
        master_items = [item for item in self.window_list.get_children() 
                       if self.window_list.set(item, "master") == "√"]
        
        if not master_items:
            # 如果没有主控窗口，设置第一个选中的窗口为主控
            self.set_master_window(selected[0])
        
        # 切换同步状态
        if not self.is_syncing:
            try:
                self.start_sync(selected)
                self.sync_button.configure(text="■ 停止同步", style='Accent.TButton')
                self.is_syncing = True
                print("同步已开启")
            except Exception as e:
                print(f"开启同步失败: {str(e)}")
                # 确保状态正确
                self.is_syncing = False
                self.sync_button.configure(text="▶ 开始同步", style='Accent.TButton')
                # 重新显示错误消息
                messagebox.showerror("错误", str(e))
        else:
            try:
                self.stop_sync()
                self.sync_button.configure(text="▶ 开始同步", style='Accent.TButton')
                self.is_syncing = False
                print("同步已停止")
            except Exception as e:
                print(f"停止同步失败: {str(e)}")

    def start_sync(self, selected_items):
        try:
            # 确保主控窗口存在
            if not self.master_window:
                raise Exception("未设置主控窗口")
            
            # 保存选中的窗口列表，并按编号排序
            self.sync_windows = []
            window_info = []
            
            # 收集所有选中的窗口
            for item in selected_items:
                values = self.window_list.item(item)['values']
                if values and len(values) >= 5:
                    number = int(values[1])
                    hwnd = int(values[4])
                    if hwnd != self.master_window:  # 排除主控窗口
                        window_info.append((number, hwnd))
            
            # 按编号排序
            window_info.sort(key=lambda x: x[0])
            
            # 保存所有同步窗口的句柄
            self.sync_windows = [hwnd for _, hwnd in window_info]
            
            # 启动键盘和鼠标钩子
            if not self.hook_thread:
                self.is_syncing = True
                self.hook_thread = threading.Thread(target=self.message_loop)
                self.hook_thread.daemon = True
                self.hook_thread.start()
                
                keyboard.hook(self.on_keyboard_event)
                mouse.hook(self.on_mouse_event)
                
                # 更新按钮状态
                self.sync_button.configure(text="■ 停止同步", style='Accent.TButton')
                
                # 启动插件窗口监控线程
                self.popup_monitor_thread = threading.Thread(target=self.monitor_popups)
                self.popup_monitor_thread.daemon = True
                self.popup_monitor_thread.start()
                
                print(f"已启动同步，主控窗口: {self.master_window}, 同步窗口: {self.sync_windows}")
                
        except Exception as e:
            self.stop_sync()  # 确保清理资源
            print(f"开启同步失败: {str(e)}")
            raise e

    def message_loop(self):
        # 消息循环
        while self.is_syncing:
            time.sleep(0.001)

    def on_mouse_event(self, event):
        try:
            if self.is_syncing:
                current_window = win32gui.GetForegroundWindow()
                
                # 检查是否是主控窗口或其插件窗口
                is_master = current_window == self.master_window
                master_popups = self.get_chrome_popups(self.master_window)
                is_popup = current_window in master_popups
                
                if is_master or is_popup:
                    # 对于移动事件进行优化
                    if isinstance(event, mouse.MoveEvent):
                        # 检查移动距离和时间间隔
                        current_time = time.time()
                        if current_time - self.last_move_time < self.move_interval:
                            return
                            
                        dx = abs(event.x - self.last_mouse_position[0])
                        dy = abs(event.y - self.last_mouse_position[1])
                        if dx < self.mouse_threshold and dy < self.mouse_threshold:
                            return
                            
                        self.last_mouse_position = (event.x, event.y)
                        self.last_move_time = current_time

                    # 获取鼠标位置
                    x, y = mouse.get_position()
                    
                    # 获取当前窗口的相对坐标
                    current_rect = win32gui.GetWindowRect(current_window)
                    rel_x = (x - current_rect[0]) / (current_rect[2] - current_rect[0])
                    rel_y = (y - current_rect[1]) / (current_rect[3] - current_rect[1])
                    
                    # 同步到其他窗口
                    for hwnd in self.sync_windows:
                        try:
                            # 确定目标窗口
                            if is_master:
                                target_hwnd = hwnd
                            else:
                                # 查找对应的扩展程序窗口
                                target_popups = self.get_chrome_popups(hwnd)
                                # 按照相对位置匹配
                                best_match = None
                                min_diff = float('inf')
                                for popup in target_popups:
                                    popup_rect = win32gui.GetWindowRect(popup)
                                    master_rect = win32gui.GetWindowRect(current_window)
                                    # 计算相对位置差异
                                    master_rel_x = master_rect[0] - win32gui.GetWindowRect(self.master_window)[0]
                                    master_rel_y = master_rect[1] - win32gui.GetWindowRect(self.master_window)[1]
                                    popup_rel_x = popup_rect[0] - win32gui.GetWindowRect(hwnd)[0]
                                    popup_rel_y = popup_rect[1] - win32gui.GetWindowRect(hwnd)[1]
                                    
                                    diff = abs(master_rel_x - popup_rel_x) + abs(master_rel_y - popup_rel_y)
                                    if diff < min_diff:
                                        min_diff = diff
                                        best_match = popup
                                target_hwnd = best_match if best_match else hwnd
                            
                            if not target_hwnd:
                                continue
                            
                            # 获取目标窗口尺寸
                            target_rect = win32gui.GetWindowRect(target_hwnd)
                            
                            # 计算目标坐标
                            client_x = int((target_rect[2] - target_rect[0]) * rel_x)
                            client_y = int((target_rect[3] - target_rect[1]) * rel_y)
                            lparam = win32api.MAKELONG(client_x, client_y)
                            
                            # 处理滚轮事件
                            if isinstance(event, mouse.WheelEvent):
                                try:
                                    wheel_delta = int(event.delta)
                                    if keyboard.is_pressed('ctrl'):
                                        
                                        if wheel_delta > 0:                                            
                                            win32gui.PostMessage(target_hwnd, win32con.WM_KEYDOWN, win32con.VK_CONTROL, 0)
                                            win32gui.PostMessage(target_hwnd, win32con.WM_KEYDOWN, 0xBB, 0)  # VK_OEM_PLUS
                                            win32gui.PostMessage(target_hwnd, win32con.WM_KEYUP, 0xBB, 0)
                                            win32gui.PostMessage(target_hwnd, win32con.WM_KEYUP, win32con.VK_CONTROL, 0)
                                        else:
                                            win32gui.PostMessage(target_hwnd, win32con.WM_KEYDOWN, win32con.VK_CONTROL, 0)
                                            win32gui.PostMessage(target_hwnd, win32con.WM_KEYDOWN, win32con.VK_CONTROL, 0)
                                            win32gui.PostMessage(target_hwnd, win32con.WM_KEYDOWN, 0xBD, 0)  # VK_OEM_MINUS
                                            win32gui.PostMessage(target_hwnd, win32con.WM_KEYUP, 0xBD, 0)
                                            win32gui.PostMessage(target_hwnd, win32con.WM_KEYUP, win32con.VK_CONTROL, 0)
                                    else:
                                        vk_code = win32con.VK_UP if wheel_delta > 0 else win32con.VK_DOWN
                                        vk_code = win32con.VK_UP if wheel_delta > 0 else win32con.VK_DOWN
                                        repeat_count = min(abs(wheel_delta) * 3, 6)
                                        for _ in range(repeat_count):
                                            win32gui.PostMessage(target_hwnd, win32con.WM_KEYDOWN, vk_code, 0)
                                            win32gui.PostMessage(target_hwnd, win32con.WM_KEYUP, vk_code, 0)
                                
                                except Exception as e:
                                    print(f"处理滚轮事件失败: {str(e)}")
                                    continue
                            
                            # 处理鼠标点击
                            elif isinstance(event, mouse.ButtonEvent):
                                if event.event_type == mouse.DOWN:
                                    if event.button == mouse.LEFT:
                                        win32gui.PostMessage(target_hwnd, win32con.WM_LBUTTONDOWN, win32con.MK_LBUTTON, lparam)
                                    elif event.button == mouse.RIGHT:
                                        win32gui.PostMessage(target_hwnd, win32con.WM_RBUTTONDOWN, win32con.MK_RBUTTON, lparam)
                                elif event.event_type == mouse.UP:
                                    if event.button == mouse.LEFT:
                                        win32gui.PostMessage(target_hwnd, win32con.WM_LBUTTONUP, 0, lparam)
                                    elif event.button == mouse.RIGHT:
                                        win32gui.PostMessage(target_hwnd, win32con.WM_RBUTTONUP, 0, lparam)
                            
                            # 处理鼠标移动
                            elif isinstance(event, mouse.MoveEvent):
                                win32gui.PostMessage(target_hwnd, win32con.WM_MOUSEMOVE, 0, lparam)
                                
                        except Exception as e:
                            print(f"同步到窗口 {target_hwnd} 失败: {str(e)}")
                            continue
                            
        except Exception as e:
            print(f"处理鼠标事件失败: {str(e)}")

    def on_keyboard_event(self, event):
        # 改进的键盘事件处理
        try:
            if self.is_syncing:
                current_window = win32gui.GetForegroundWindow()
                
                # 检查是否是主控窗口或其插件窗口
                is_master = current_window == self.master_window
                master_popups = self.get_chrome_popups(self.master_window)
                is_popup = current_window in master_popups
                
                if is_master or is_popup:
                    # 获取实际的输入目标窗口
                    input_hwnd = win32gui.GetFocus()
                    
                    # 同步到其他窗口
                    for hwnd in self.sync_windows:
                        try:
                            # 确定目标窗口
                            if is_master:
                                target_hwnd = hwnd
                            else:
                                # 查找对应的扩展程序窗口
                                target_popups = self.get_chrome_popups(hwnd)
                                # 按照相对位置匹配
                                best_match = None
                                min_diff = float('inf')
                                for popup in target_popups:
                                    popup_rect = win32gui.GetWindowRect(popup)
                                    master_rect = win32gui.GetWindowRect(current_window)
                                    # 计算相对位置差异
                                    master_rel_x = master_rect[0] - win32gui.GetWindowRect(self.master_window)[0]
                                    master_rel_y = master_rect[1] - win32gui.GetWindowRect(self.master_window)[1]
                                    popup_rel_x = popup_rect[0] - win32gui.GetWindowRect(hwnd)[0]
                                    popup_rel_y = popup_rect[1] - win32gui.GetWindowRect(hwnd)[1]
                                    
                                    diff = abs(master_rel_x - popup_rel_x) + abs(master_rel_y - popup_rel_y)
                                    if diff < min_diff:
                                        min_diff = diff
                                        best_match = popup
                                target_hwnd = best_match if best_match else hwnd

                            if not target_hwnd:
                                continue

                            # 处理 Ctrl 组合键
                            if keyboard.is_pressed('ctrl'):
                                # 发送 Ctrl 按下
                                win32gui.PostMessage(target_hwnd, win32con.WM_KEYDOWN, win32con.VK_CONTROL, 0)
                                
                                # 处理常用组合键
                                if event.name in ['a', 'c', 'v', 'x']:
                                    vk_code = ord(event.name.upper())
                                    if event.event_type == keyboard.KEY_DOWN:
                                        win32gui.PostMessage(target_hwnd, win32con.WM_KEYDOWN, vk_code, 0)
                                        win32gui.PostMessage(target_hwnd, win32con.WM_KEYUP, vk_code, 0)
                                    win32gui.PostMessage(target_hwnd, win32con.WM_KEYUP, win32con.VK_CONTROL, 0)
                                    continue
                                    
                            # 处理普通按键
                            if event.name in ['enter', 'backspace', 'tab', 'esc', 'space', 
                                            'up', 'down', 'left', 'right',  # 添加左右键
                                            'home', 'end', 'page up', 'page down', 'delete']:  
                                vk_map = {
                                    'enter': win32con.VK_RETURN,
                                    'backspace': win32con.VK_BACK,
                                    'tab': win32con.VK_TAB,
                                    'esc': win32con.VK_ESCAPE,
                                    'space': win32con.VK_SPACE,
                                    'up': win32con.VK_UP,
                                    'down': win32con.VK_DOWN,
                                    'left': win32con.VK_LEFT,      
                                    'right': win32con.VK_RIGHT,    
                                    'home': win32con.VK_HOME,
                                    'end': win32con.VK_END,
                                    'page up': win32con.VK_PRIOR,
                                    'page down': win32con.VK_NEXT,
                                    'delete': win32con.VK_DELETE  
                                }
                                vk_code = vk_map[event.name]
                            else:
                                # 处理普通字符
                                if len(event.name) == 1:
                                    vk_code = win32api.VkKeyScan(event.name[0]) & 0xFF
                                    if event.event_type == keyboard.KEY_DOWN:
                                        # 发送字符消息
                                        win32gui.PostMessage(target_hwnd, win32con.WM_CHAR, ord(event.name[0]), 0)
                                    continue
                                else:
                                    continue

                            # 发送按键消息
                            if event.event_type == keyboard.KEY_DOWN:
                                win32gui.PostMessage(target_hwnd, win32con.WM_KEYDOWN, vk_code, 0)
                            else:
                                win32gui.PostMessage(target_hwnd, win32con.WM_KEYUP, vk_code, 0)
                                
                            # 释放组合键
                            if keyboard.is_pressed('ctrl'):
                                win32gui.PostMessage(target_hwnd, win32con.WM_KEYUP, win32con.VK_CONTROL, 0)
                                
                        except Exception as e:
                            print(f"同步到窗口 {target_hwnd} 失败: {str(e)}")
                            
        except Exception as e:
            print(f"处理键盘事件失败: {str(e)}")

    def stop_sync(self):
        # 停止同步
        try:
            self.is_syncing = False
            
            # 移除键盘钩子
            if self.keyboard_hook:
                keyboard.unhook(self.keyboard_hook)
                self.keyboard_hook = None
            
            # 移除鼠标钩子
            if self.mouse_hook_id:
                mouse.unhook(self.mouse_hook_id)
                self.mouse_hook_id = None
            
            # 等待监控线程结束
            if self.hook_thread and self.hook_thread.is_alive():
                self.hook_thread.join(timeout=1.0)
            
            # 清理资源（保留主窗口设置）
            self.sync_windows.clear()
            self.popup_mappings.clear()
            
            # 清理调试端口映射
            self.debug_ports.clear()
            
            # 重置鼠标状态
            self.last_mouse_position = (0, 0)
            self.last_move_time = 0
            
            # 更新按钮状态
            if self.sync_button:
                self.sync_button.configure(text="▶ 开始同步", style='Accent.TButton')
            
        except Exception as e:
            print(f"停止同步失败: {str(e)}")

    def on_closing(self):
        # 窗口关闭事件
        try:
            self.stop_sync()
            # 清理快捷键
            if self.shortcut_hook:
                keyboard.clear_all_hotkeys()
                keyboard.unhook_all()
                self.shortcut_hook = None
            self.save_settings()
        except Exception as e:
            print(f"程序关闭时出错: {str(e)}")
        finally:
            self.root.destroy()

    def auto_arrange_windows(self):
        """自动排列窗口"""
        try:
            # 先停止同步
            was_syncing = self.is_syncing
            if was_syncing:
                self.stop_sync()
            
            # 获取选中的窗口并按编号排序
            selected = []
            for item in self.window_list.get_children():
                if self.window_list.set(item, "select") == "√":
                    values = self.window_list.item(item)['values']
                    if values and len(values) >= 5:
                        number = int(values[1])  
                        hwnd = int(values[4])
                        selected.append((number, hwnd, item))
            
            if not selected:
                messagebox.showinfo("提示", "请先选择要排列的窗口！")
                return
            
            # 按编号正序排序
            selected.sort(key=lambda x: x[0])
            
            # 获取选中的屏幕信息
            screen_index = self.screen_combo.current()
            if screen_index < 0 or screen_index >= len(self.screens):
                messagebox.showerror("错误", "请选择有效的屏幕！")
                return
            
            screen = self.screens[screen_index]
            screen_rect = screen['work_rect']  # 使用工作区而不是完整显示区
            
            # 计算屏幕尺寸
            screen_width = screen_rect[2] - screen_rect[0]
            screen_height = screen_rect[3] - screen_rect[1]
            
            # 计算最佳布局
            count = len(selected)
            cols = int(math.sqrt(count))
            if cols * cols < count:
                cols += 1
            rows = (count + cols - 1) // cols
            
            # 计算窗口大小
            width = screen_width // cols
            height = screen_height // rows
            
            # 创建位置映射（从左到右，从上到下）
            positions = []
            for i in range(count):
                row = i // cols
                col = i % cols
                x = screen_rect[0] + col * width
                y = screen_rect[1] + row * height
                positions.append((x, y))
            
            # 应用窗口位置
            for i, (_, hwnd, _) in enumerate(selected):
                try:
                    x, y = positions[i]
                    # 确保窗口可见并移动到指定位置
                    win32gui.ShowWindow(hwnd, win32con.SW_RESTORE)
                    # 先设置窗口样式确保可以移动
                    style = win32gui.GetWindowLong(hwnd, win32con.GWL_STYLE)
                    style |= win32con.WS_SIZEBOX | win32con.WS_SYSMENU
                    win32gui.SetWindowLong(hwnd, win32con.GWL_STYLE, style)
                    # 移动窗口
                    win32gui.MoveWindow(hwnd, x, y, width, height, True)
                    # 强制重绘
                    win32gui.UpdateWindow(hwnd)
                except Exception as e:
                    print(f"移动窗口 {hwnd} 失败: {str(e)}")
                    continue
            
            # 如果之前在同步，重新开启同步
            if was_syncing:
                self.start_sync([item for _, _, item in selected])
            
        except Exception as e:
            messagebox.showerror("错误", f"自动排列失败: {str(e)}")

    def custom_arrange_windows(self):
        # 自定义排列窗口
        try:
            # 先停止同步
            was_syncing = self.is_syncing
            if was_syncing:
                self.stop_sync()
            
            selected = []
            for item in self.window_list.get_children():
                if self.window_list.set(item, "select") == "√":
                    selected.append(item)
                    
            if not selected:
                messagebox.showinfo("提示", "请选择要排列的窗口！")
                return
            
            try:
                # 获取参数
                start_x = int(self.start_x.get())
                start_y = int(self.start_y.get())
                width = int(self.window_width.get())
                height = int(self.window_height.get())
                h_spacing = int(self.h_spacing.get())
                v_spacing = int(self.v_spacing.get())
                windows_per_row = int(self.windows_per_row.get())
                
                # 排列窗口
                for i, item in enumerate(selected):
                    values = self.window_list.item(item)['values']
                    if values and len(values) >= 5:  # 确保有足够的值
                        hwnd = int(values[4])
                        row = i // windows_per_row
                        col = i % windows_per_row
                        
                        x = start_x + col * (width + h_spacing)
                        y = start_y + row * (height + v_spacing)
                        
                        # 确保窗口可见并移动到指定位置
                        win32gui.ShowWindow(hwnd, win32con.SW_RESTORE)
                        win32gui.MoveWindow(hwnd, x, y, width, height, True)
                
                # 保存参数
                self.save_settings()
                    
            except ValueError:
                messagebox.showerror("错误", "请输入有效的数字参数！")
            except Exception as e:
                messagebox.showerror("错误", f"排列窗口失败: {str(e)}")
            
            # 如果之前在同步，重新开启同步
            if was_syncing:
                self.start_sync(selected)
            
        except Exception as e:
            messagebox.showerror("错误", f"排列窗口失败: {str(e)}")

    def load_settings(self) -> dict:
        # 加载设置
        try:
            with open('settings.json', 'r') as f:
                return json.load(f)
        except:
            return {}

    def save_settings(self):
        # 保存设置
        try:
            settings = {
                'shortcut_path': self.path_entry.get(),
                'window_position': self.root.geometry(),
                'last_window_numbers': self.numbers_entry.get(),  # 添加保存窗口编号
                'arrange_params': {
                    'start_x': self.start_x.get(),
                    'start_y': self.start_y.get(),
                    'window_width': self.window_width.get(),
                    'window_height': self.window_height.get(),
                    'h_spacing': self.h_spacing.get(),
                    'v_spacing': self.v_spacing.get(),
                    'windows_per_row': self.windows_per_row.get()
                },
                'sync_shortcut': self.current_shortcut
            }
            with open('settings.json', 'w', encoding='utf-8') as f:
                json.dump(settings, f, ensure_ascii=False, indent=4)
        except Exception as e:
            print(f"保存设置失败: {str(e)}")

    def load_arrange_params(self):
        # 加载排列参数
        settings = self.load_settings()
        if 'arrange_params' in settings:
            params = settings['arrange_params']
            self.start_x.delete(0, tk.END)
            self.start_x.insert(0, params.get('start_x', '0'))
            self.start_y.delete(0, tk.END)
            self.start_y.insert(0, params.get('start_y', '0'))
            self.window_width.delete(0, tk.END)
            self.window_width.insert(0, params.get('window_width', '500'))
            self.window_height.delete(0, tk.END)
            self.window_height.insert(0, params.get('window_height', '400'))
            self.h_spacing.delete(0, tk.END)
            self.h_spacing.insert(0, params.get('h_spacing', '0'))
            self.v_spacing.delete(0, tk.END)
            self.v_spacing.insert(0, params.get('v_spacing', '0'))
            self.windows_per_row.delete(0, tk.END)
            self.windows_per_row.insert(0, params.get('windows_per_row', '5'))

    def parse_window_numbers(self, numbers_str: str) -> List[int]:
        # 解析窗口编号字符串
        if not numbers_str.strip():
            return list(range(1, 49))  # 如果为空，返回所有编号
            
        result = []
        # 分割逗号分隔的部分
        parts = numbers_str.split(',')
        for part in parts:
            part = part.strip()
            if '-' in part:
                # 处理范围，如 "1-5"
                start, end = map(int, part.split('-'))
                result.extend(range(start, end + 1))
            else:
                # 处理单个数字
                result.append(int(part))
        return sorted(list(set(result)))  # 去重并排序

    def open_windows(self):
        # 打开Chrome窗口
        path = self.path_entry.get()
        numbers = self.numbers_entry.get()
        
        if not path or not numbers:
            messagebox.showwarning("警告", "请输入快捷方式路径和窗口编号！")
            return
            
        try:
            window_numbers = self.parse_window_numbers(numbers)
            for num in window_numbers:
                shortcut = os.path.join(path, f"{num}.lnk")
                if os.path.exists(shortcut):
                    subprocess.Popen(["start", "", shortcut], shell=True)
                    time.sleep(0.5)  # 添加延时确保按顺序打开
                else:
                    messagebox.showwarning("警告", f"快捷方式不存在: {shortcut}")
            
            # 保存路径
            self.save_settings()
            
        except Exception as e:
            messagebox.showerror("错误", f"打开窗口失败: {str(e)}")

    def get_shortcut_number(self, shortcut_path):
        # 从快捷方式中获取窗口编号
        handle = None
        try:
            # 获取目标进程
            shortcut = self.shell.CreateShortCut(shortcut_path)
            cmd_line = shortcut.Arguments
            
            # 打开进程获取命令行
            handle = win32api.OpenProcess(
                win32con.PROCESS_QUERY_INFORMATION | win32con.PROCESS_VM_READ,
                False,
                self.pid
            )
            
            # 处理命令行
            if '--user-data-dir=' in cmd_line:
                data_dir = cmd_line.split('--user-data-dir=')[1].strip('"')
                number = os.path.basename(data_dir)
                return number
                
            return None
            
        except Exception as e:
            print(f"获取快捷方式编号失败: {str(e)}")
            return None
            
        finally:
            # 确保句柄被关闭
            if handle:
                try:
                    win32api.CloseHandle(handle)
                except Exception as e:
                    print(f"关闭进程句柄失败: {str(e)}")

    def import_windows(self):
        # 导入当前打开的Chrome窗口
        try:
            # 清空列表
            for item in self.window_list.get_children():
                self.window_list.delete(item)
            
            def callback(hwnd, windows):
                if win32gui.IsWindowVisible(hwnd):
                    _, pid = win32process.GetWindowThreadProcessId(hwnd)
                    try:
                        handle = win32api.OpenProcess(win32con.PROCESS_QUERY_INFORMATION | win32con.PROCESS_VM_READ, False, pid)
                        path = win32process.GetModuleFileNameEx(handle, 0)
                        win32api.CloseHandle(handle)
                        
                        if "chrome.exe" in path.lower():
                            title = win32gui.GetWindowText(hwnd)
                            if title and not title.startswith("Chrome 传递"):
                                # 为每个窗口分配一个调试端口
                                port = self.base_debug_port + len(windows)
                                self.debug_ports[hwnd] = port
                                windows.append((title, hwnd))
                            
                    except Exception as e:
                        print(f"获取进程信息失败: {str(e)}")
                    
            windows = []
            win32gui.EnumWindows(callback, windows)
            
            # 反转windows列表，这样就会按照相反的顺序添加到列表中
            windows.reverse()
            
            # 添加到列表
            for i, (title, hwnd) in enumerate(windows, 1):
                self.window_list.insert("", "end", values=("", f"{i}", title, "", hwnd))
            
        except Exception as e:
            messagebox.showerror("错误", f"导入窗口失败: {str(e)}")

    def enum_window_callback(self, hwnd, windows):
        # 枚举窗口回调函数
        try:
            # 检查窗口是否可见
            if not win32gui.IsWindowVisible(hwnd):
                return
            
            # 获取窗口标题
            title = win32gui.GetWindowText(hwnd)
            if not title:
                return
            
            # 检查是否是Chrome窗口
            if " - Google Chrome" in title:
                # 提取窗口编号
                number = None
                if title.startswith("[主控]"):
                    title = title[4:].strip()  # 移除[主控]标记
                
                # 从进程命令行参数中获取窗口编号
                try:
                    _, pid = win32process.GetWindowThreadProcessId(hwnd)
                    handle = win32api.OpenProcess(win32con.PROCESS_QUERY_INFORMATION | win32con.PROCESS_VM_READ, False, pid)
                    if handle:
                        cmd_line = win32process.GetModuleFileNameEx(handle, 0)
                        win32api.CloseHandle(handle)
                        
                        # 从路径中提取编号
                        if "\\Data\\" in cmd_line:
                            number = int(cmd_line.split("\\Data\\")[-1].split("\\")[0])
                except:
                    pass
                
                if number is not None:
                    windows.append({
                        'hwnd': hwnd,
                        'title': title,
                        'number': number
                    })
                
        except Exception as e:
            print(f"枚举窗口失败: {str(e)}")

    def close_selected_windows(self):
        # 关闭选中的窗口
        selected = []
        for item in self.window_list.get_children():
            if self.window_list.set(item, "select") == "√":
                selected.append(item)
                
        if not selected:
            messagebox.showinfo("提示", "请先选择要关闭的窗口！")
            return
            
        try:
            for item in selected:
                # 从values中获取hwnd
                hwnd = int(self.window_list.item(item)['values'][4])
                try:
                    # 检查窗口是否还存在
                    if win32gui.IsWindow(hwnd):
                        win32gui.PostMessage(hwnd, win32con.WM_CLOSE, 0, 0)
                except:
                    pass  # 忽略已关闭窗口的错误
            
            # 等待窗口关闭后刷新列表
            time.sleep(0.5)
            self.import_windows()
            
        except Exception as e:
            print(f"关闭窗口失败: {str(e)}")  # 只打印错误，不显示错误对话框

    def set_taskbar_icons(self):
        # 设置独立任务栏图标
        if not self.path_entry.get():
            messagebox.showinfo("提示", "请先设置快捷方式目录！")
            return
            
        if not os.path.exists(self.path_entry.get()):
            messagebox.showerror("错误", "快捷方式目录不存在！")
            return
            
        # 确认操作
        choice = messagebox.askyesnocancel("选择操作", "选择要执行的操作：\n是 - 设置自定义图标\n否 - 恢复原始设置\n取消 - 不执行任何操作")
        if choice is None:  # 用户点击取消
            return
            
        try:
            data_dir = self.path_entry.get()
            icon_dir = self.icon_path_entry.get()
            shell = win32com.client.Dispatch("WScript.Shell")
            modified_count = 0
            
            # 获取要修改的窗口编号列表
            window_numbers = self.parse_window_numbers(self.icon_window_numbers.get())
            
            if choice:  # 设置自定义图标
                # 确保图标目录存在
                if not os.path.exists(icon_dir):
                    os.makedirs(icon_dir)
                
                # 修改指定的快捷方式
                for i in window_numbers:
                    shortcut_path = os.path.join(data_dir, f"{i}.lnk")
                    if not os.path.exists(shortcut_path):
                        continue
                        
                    # 修改快捷方式
                    shortcut = shell.CreateShortCut(shortcut_path)
                    
                    # 设置自定义图标
                    icon_path = os.path.join(icon_dir, f"{i}.ico")
                    if os.path.exists(icon_path):
                        shortcut.IconLocation = icon_path
                        # 保存修改
                        shortcut.save()
                        modified_count += 1
                
                messagebox.showinfo("成功", f"已成功修改 {modified_count} 个快捷方式的图标！")
            else:  # 恢复原始设置
                chrome_path = r"C:\Program Files\Google\Chrome\Application\chrome.exe"
                if not os.path.exists(chrome_path):
                    chrome_path = r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe"
                
                # 恢复指定的快捷方式
                for i in window_numbers:
                    shortcut_path = os.path.join(data_dir, f"{i}.lnk")
                    if not os.path.exists(shortcut_path):
                        continue
                        
                    # 修改快捷方式
                    shortcut = shell.CreateShortCut(shortcut_path)
                    
                    # 恢复默认图标
                    shortcut.IconLocation = f"{chrome_path},0"
                    
                    # 恢复原始启动参数
                    original_args = f'--user-data-dir="D:\\chrom duo\\Data\\{i}"'
                    shortcut.TargetPath = chrome_path
                    shortcut.Arguments = original_args
                    
                    # 保存修改
                    shortcut.save()
                    modified_count += 1
                
                messagebox.showinfo("成功", f"已成功恢复 {modified_count} 个快捷方式的原始设置！")
            
        except Exception as e:
            messagebox.showerror("错误", f"操作失败: {str(e)}")

    def batch_open_urls(self):
        # 批量打开网页
        try:
            # 获取输入的网址
            url = self.url_entry.get() 
            if not url:
                messagebox.showwarning("警告", "请输入要打开的网址！")
                return
            
            # 确保 URL 格式正确
            if not url.startswith(('http://', 'https://')):
                url = 'https://' + url
            
            # 获取选中的窗口
            selected_windows = []
            for item in self.window_list.get_children():
                if self.window_list.set(item, "select") == "√":
                    hwnd = int(self.window_list.item(item)['values'][-1])
                    selected_windows.append(hwnd)
            
            if not selected_windows:
                messagebox.showwarning("警告", "请先选择要操作的窗口！")
                return
            
            # 在每个选中的窗口中打开网页
            for hwnd in selected_windows:
                try:
                    # 激活窗口
                    win32gui.ShowWindow(hwnd, win32con.SW_RESTORE)
                    win32gui.SetForegroundWindow(hwnd)
                    time.sleep(0.1) 
                    
                    # 打开新标签页
                    keyboard.press_and_release('ctrl+t')
                    time.sleep(0.1) 
                    
                    # 输入网址并回车
                    keyboard.write(url)
                    time.sleep(0.1) 
                    keyboard.press_and_release('enter')
                    time.sleep(0.2) 
                    
                except Exception as e:
                    print(f"在窗口 {hwnd} 打开URL失败: {str(e)}")
            
            messagebox.showinfo("成功", "批量打开网页完成！")
            
        except Exception as e:
            messagebox.showerror("错误", f"批量打开网页失败: {str(e)}")

    def run(self):
        """运行程序"""
        self.root.mainloop()

    def load_window_position(self):
        # 从 settings.json 加载窗口位置
        try:
            settings = self.load_settings()
            return settings.get('window_position')
        except:
            return None

    def save_window_position(self):
        # 保存窗口位置到 settings.json
        try:
            position = self.root.geometry()
            settings = self.load_settings()
            settings['window_position'] = position
            
            with open('settings.json', 'w', encoding='utf-8') as f:
                json.dump(settings, f, ensure_ascii=False, indent=4)
        except Exception as e:
            print(f"保存窗口位置失败: {str(e)}")

    def get_chrome_popups(self, chrome_hwnd):
        # 改进的插件窗口检测
        popups = []
        def enum_windows_callback(hwnd, _):
            try:
                if not win32gui.IsWindowVisible(hwnd):
                    return
                    
                class_name = win32gui.GetClassName(hwnd)
                title = win32gui.GetWindowText(hwnd)
                _, chrome_pid = win32process.GetWindowThreadProcessId(chrome_hwnd)
                _, popup_pid = win32process.GetWindowThreadProcessId(hwnd)
                
                # 检查是否是Chrome相关窗口
                if popup_pid == chrome_pid:
                    # 检查窗口类型
                    if "Chrome_WidgetWin_1" in class_name:
                        # 检查是否是扩展程序相关窗口，放宽检测条件
                        style = win32gui.GetWindowLong(hwnd, win32con.GWL_STYLE)
                        ex_style = win32gui.GetWindowLong(hwnd, win32con.GWL_EXSTYLE)
                        
                        # 扩展窗口的特征
                        is_popup = (
                            "扩展程序" in title or 
                            "插件" in title or
                            win32gui.GetParent(hwnd) == chrome_hwnd or
                            (style & win32con.WS_POPUP) != 0 or
                            (style & win32con.WS_CHILD) != 0 or
                            (ex_style & win32con.WS_EX_TOOLWINDOW) != 0  # 添加工具窗口检查
                        )
                        
                        if is_popup:
                            popups.append(hwnd)
                    
            except Exception as e:
                print(f"枚举窗口失败: {str(e)}")
                
        win32gui.EnumWindows(enum_windows_callback, None)
        return popups

    def monitor_popups(self):
        # 监控插件窗口变化
        while self.is_syncing:
            try:
                self.sync_chrome_popups()
            except:
                pass
            time.sleep(0.1)  

    def show_shortcut_dialog(self):
        # 显示快捷键设置对话框
        # 创建对话框
        dialog = tk.Toplevel(self.root)
        dialog.title("设置同步功能快捷键")
        dialog.geometry("300x150")
        dialog.resizable(False, False)
        
        # 使对话框模态
        dialog.transient(self.root)
        dialog.grab_set()
        
        # 当前快捷键显示
        current_label = ttk.Label(dialog, text=f"当前快捷键: {self.current_shortcut}")
        current_label.pack(pady=10)
        
        # 快捷键输入框
        shortcut_var = tk.StringVar(value="点击下方按钮开始录制快捷键...")
        shortcut_label = ttk.Label(dialog, textvariable=shortcut_var)
        shortcut_label.pack(pady=5)
        
        # 记录按键状态
        keys_pressed = set()
        recording = False
        
        def start_recording():
            # 开始录制快捷键
            nonlocal recording
            recording = True
            keys_pressed.clear()
            shortcut_var.set("请按下快捷键组合...")
            record_btn.configure(state='disabled')
            
            def on_key_event(e):
                if not recording:
                    return
                if e.event_type == keyboard.KEY_DOWN:
                    keys_pressed.add(e.name)
                    shortcut_var.set('+'.join(sorted(keys_pressed)))
                elif e.event_type == keyboard.KEY_UP:
                    if e.name in keys_pressed:
                        keys_pressed.remove(e.name)
                    if not keys_pressed:  
                        stop_recording()
            
            keyboard.hook(on_key_event)
        
        def stop_recording():
            # 停止录制快捷键
            nonlocal recording
            recording = False
            keyboard.unhook_all()
            # 重新设置当前快捷键
            if self.current_shortcut:
                self.set_shortcut(self.current_shortcut)
            record_btn.configure(state='normal')
        
        # 录制按钮
        record_btn = ttk.Button(
            dialog,
            text="开始录制",
            command=start_recording
        )
        record_btn.pack(pady=10)
        
        def save_shortcut():
            # 保存快捷键设置
            new_shortcut = shortcut_var.get()
            if new_shortcut and new_shortcut != "点击下方按钮开始录制快捷键..." and new_shortcut != "请按下快捷键组合...":
                try:
                    # 设置新快捷键
                    self.set_shortcut(new_shortcut)
                    
                    # 保存到设置文件
                    settings = self.load_settings()
                    settings['sync_shortcut'] = new_shortcut
                    with open('settings.json', 'w', encoding='utf-8') as f:
                        json.dump(settings, f, ensure_ascii=False, indent=4)
                    
                    messagebox.showinfo("成功", f"快捷键已设置为: {new_shortcut}")
                    dialog.destroy()
                except Exception as e:
                    messagebox.showerror("错误", f"设置快捷键失败: {str(e)}")
            else:
                messagebox.showwarning("警告", "请先录制快捷键！")
        
        # 保存按钮
        ttk.Button(
            dialog,
            text="保存",
            command=save_shortcut
        ).pack(pady=5)
        
        # 确保关闭对话框时停止录制
        dialog.protocol("WM_DELETE_WINDOW", lambda: [stop_recording(), dialog.destroy()])
        
        # 居中显示对话框
        dialog.update_idletasks()
        width = dialog.winfo_width()
        height = dialog.winfo_height()
        x = (dialog.winfo_screenwidth() // 2) - (width // 2)
        y = (dialog.winfo_screenheight() // 2) - (height // 2)
        dialog.geometry(f'{width}x{height}+{x}+{y}')

    def set_shortcut(self, shortcut):
        # 设置快捷键
        try:
            # 先完全清理所有快捷键和钩子
            keyboard.unhook_all()
            keyboard.clear_all_hotkeys()
            
            if self.shortcut_hook:
                self.shortcut_hook = None
            
            if shortcut:
                # 添加错误重试机制
                max_retries = 3
                for attempt in range(max_retries):
                    try:
                        # 注册新快捷键
                        self.shortcut_hook = keyboard.add_hotkey(
                            shortcut,
                            self.toggle_sync,
                            suppress=True,
                            trigger_on_release=True  # 在按键释放时触发，避免卡键
                        )
                        self.current_shortcut = shortcut
                        print(f"快捷键 {shortcut} 设置成功")
                        break
                    except Exception as e:
                        print(f"第 {attempt + 1} 次设置快捷键失败: {str(e)}")
                        if attempt == max_retries - 1:
                            raise
                        time.sleep(0.5)  # 重试前等待
                    
        except Exception as e:
            print(f"设置快捷键失败: {str(e)}")
            self.shortcut_hook = None
            # 尝试恢复之前的快捷键
            if self.current_shortcut and self.current_shortcut != shortcut:
                try:
                    self.shortcut_hook = keyboard.add_hotkey(
                        self.current_shortcut,
                        self.toggle_sync,
                        suppress=True,
                        trigger_on_release=True
                    )
                except:
                    self.current_shortcut = None

    def update_screen_list(self):
        """更新屏幕列表"""
        try:
            screens = []
            
            def callback(hmonitor, hdc, lprect, lparam):
                try:
                    # 获取显示器信息
                    monitor_info = win32api.GetMonitorInfo(hmonitor)
                    screen_name = f"屏幕 {len(screens) + 1}"
                    if monitor_info['Flags'] & 1:  # MONITORINFOF_PRIMARY
                        screen_name += " (主屏幕)"
                    screens.append({
                        'name': screen_name,
                        'rect': monitor_info['Monitor'],
                        'work_rect': monitor_info['Work'],
                        'monitor': hmonitor
                    })
                except Exception as e:
                    print(f"处理显示器信息失败: {str(e)}")
                return True

            # 定义回调函数类型
            MONITORENUMPROC = ctypes.WINFUNCTYPE(
                ctypes.c_bool,
                ctypes.c_ulong,
                ctypes.c_ulong,
                ctypes.POINTER(wintypes.RECT),
                ctypes.c_longlong
            )

            # 创建回调函数
            callback_function = MONITORENUMPROC(callback)

            # 枚举显示器
            if ctypes.windll.user32.EnumDisplayMonitors(0, 0, callback_function, 0) == 0:
                # EnumDisplayMonitors 失败，尝试使用备用方法
                try:
                    # 获取虚拟屏幕范围
                    virtual_width = win32api.GetSystemMetrics(win32con.SM_CXVIRTUALSCREEN)
                    virtual_height = win32api.GetSystemMetrics(win32con.SM_CYVIRTUALSCREEN)
                    virtual_left = win32api.GetSystemMetrics(win32con.SM_XVIRTUALSCREEN)
                    virtual_top = win32api.GetSystemMetrics(win32con.SM_YVIRTUALSCREEN)
                    
                    # 获取主屏幕信息
                    primary_monitor = win32api.MonitorFromPoint((0, 0), win32con.MONITOR_DEFAULTTOPRIMARY)
                    primary_info = win32api.GetMonitorInfo(primary_monitor)
                    
                    # 添加主屏幕
                    screens.append({
                        'name': "屏幕 1 (主屏幕)",
                        'rect': primary_info['Monitor'],
                        'work_rect': primary_info['Work'],
                        'monitor': primary_monitor
                    })
                    
                    # 尝试获取第二个屏幕
                    try:
                        second_monitor = win32api.MonitorFromPoint(
                            (virtual_left + virtual_width - 1, 
                             virtual_top + virtual_height // 2),
                            win32con.MONITOR_DEFAULTTONULL
                        )
                        if second_monitor and second_monitor != primary_monitor:
                            second_info = win32api.GetMonitorInfo(second_monitor)
                            screens.append({
                                'name': "屏幕 2",
                                'rect': second_info['Monitor'],
                                'work_rect': second_info['Work'],
                                'monitor': second_monitor
                            })
                    except:
                        pass
                    
                except Exception as e:
                    print(f"备用方法失败: {str(e)}")
            
            if not screens:
                # 如果仍然没有找到屏幕，使用基本方案
                screen_width = win32api.GetSystemMetrics(win32con.SM_CXSCREEN)
                screen_height = win32api.GetSystemMetrics(win32con.SM_CYSCREEN)
                screens.append({
                    'name': "屏幕 1 (主屏幕)",
                    'rect': (0, 0, screen_width, screen_height),
                    'work_rect': (0, 0, screen_width, screen_height),
                    'monitor': None
                })
            
            # 按照屏幕位置排序（从左到右）
            screens.sort(key=lambda x: x['rect'][0])
            
            # 更新下拉框选项
            self.screen_combo['values'] = [screen['name'] for screen in screens]
            self.screens = screens  # 保存屏幕信息
            
            # 默认选择主屏幕
            for i, screen in enumerate(screens):
                if '主屏幕' in screen['name']:
                    self.screen_combo.current(i)
                    break
            else:
                self.screen_combo.current(0)
            
            # 打印调试信息
            print("检测到的屏幕:")
            for screen in screens:
                print(f"名称: {screen['name']}")
                print(f"位置: {screen['rect']}")
                print(f"工作区: {screen['work_rect']}")
                print("---")
            
        except Exception as e:
            print(f"获取屏幕列表失败: {str(e)}")
            # 使用最基本的方案
            try:
                screen_width = win32api.GetSystemMetrics(win32con.SM_CXSCREEN)
                screen_height = win32api.GetSystemMetrics(win32con.SM_CYSCREEN)
                screens = [{
                    'name': "主屏幕",
                    'rect': (0, 0, screen_width, screen_height),
                    'work_rect': (0, 0, screen_width, screen_height),
                    'monitor': None
                }]
                self.screen_combo['values'] = [screen['name'] for screen in screens]
                self.screens = screens
                self.screen_combo.current(0)
            except Exception as e2:
                print(f"基本方案也失败了: {str(e2)}")

if __name__ == "__main__":
    app = ChromeManager()
    app.run() 