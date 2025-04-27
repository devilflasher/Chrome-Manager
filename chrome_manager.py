import tkinter as tk
from tkinter import ttk, messagebox, filedialog
import os
import subprocess
import win32gui
import win32process
import win32con
import win32api
import win32com.client
import json
import requests
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
import win32security
import PIL.Image as Image
import PIL.ImageDraw as ImageDraw
import PIL.ImageFont as ImageFont

# 添加通知错误处理
try:
    from win11toast import notify, toast
except ImportError:
    # 简单的空函数替代
    def toast(title, message, **kwargs):
        pass

    def notify(title, message, **kwargs):
        pass


import re
import socket
import traceback
import wmi
import pythoncom  # 添加pythoncom导入
import concurrent.futures
import random


# 添加滚轮钩子所需的结构体定义
class POINT(ctypes.Structure):
    _fields_ = [("x", ctypes.c_long), ("y", ctypes.c_long)]


class MSLLHOOKSTRUCT(ctypes.Structure):
    _fields_ = [
        ("pt", POINT),
        ("mouseData", wintypes.DWORD),
        ("flags", wintypes.DWORD),
        ("time", wintypes.DWORD),
        ("dwExtraInfo", ctypes.POINTER(wintypes.ULONG)),
    ]


def is_admin():
    # 检查是否具有管理员权限
    try:
        return ctypes.windll.shell32.IsUserAnAdmin()
    except:
        return False


def run_as_admin():
    # 以管理员权限重新运行程序
    ctypes.windll.shell32.ShellExecuteW(
        None, "runas", sys.executable, " ".join(sys.argv), None, 1
    )


class ChromeManager:
    def __init__(self):
        """初始化"""
        # 记录启动时间用于性能分析
        self.start_time = time.time()

        # 默认设置值
        self.show_chrome_tip = True  # 是否显示Chrome后台运行提示

        # 加载设置
        self.settings = self.load_settings()

        self.enable_cdp = True  # 始终开启CDP

        # 从设置中读取是否显示Chrome提示的设置
        if "show_chrome_tip" in self.settings:
            self.show_chrome_tip = self.settings["show_chrome_tip"]

        # 滚轮钩子相关参数
        self.wheel_hook_id = None
        self.wheel_hook_proc = None
        self.standard_wheel_delta = 120  # 标准滚轮增量值
        self.last_wheel_time = 0
        self.wheel_threshold = 0.05  # 秒，防止事件触发过于频繁
        self.use_wheel_hook = True  # 是否使用滚轮钩子

        # 存储快捷方式编号和进程ID的映射关系
        self.shortcut_to_pid = {}
        # 存储进程ID和窗口编号的映射关系
        self.pid_to_number = {}

        if not is_admin():
            if messagebox.askyesno(
                "权限不足",
                "需要管理员权限才能运行同步功能。\n是否以管理员身份重新启动程序？",
            ):
                run_as_admin()
                sys.exit()

        # 确保settings.json文件存在
        if not os.path.exists("settings.json"):
            with open("settings.json", "w", encoding="utf-8") as f:
                json.dump({}, f, ensure_ascii=False, indent=4)

        self.root = tk.Tk()
        self.root.title("NoBiggie社区Chrome多窗口管理器 V2.0")

        # 先隐藏主窗口，避免闪烁
        self.root.withdraw()

        # 随机数字输入相关配置 - 移动到root创建之后
        self.random_min_value = tk.StringVar(value="1000")
        self.random_max_value = tk.StringVar(value="2000")
        self.random_overwrite = tk.BooleanVar(value=True)
        self.random_delayed = tk.BooleanVar(value=False)

        try:
            icon_path = os.path.join(os.path.dirname(__file__), "app.ico")
            if os.path.exists(icon_path):
                self.root.iconbitmap(icon_path)
        except Exception as e:
            print(f"设置图标失败: {str(e)}")

        # 设置固定的窗口大小
        self.window_width = 700
        self.window_height = 360
        self.root.geometry(f"{self.window_width}x{self.window_height}")
        self.root.resizable(False, False)

        # 加载主题
        sv_ttk.set_theme("light")
        print(f"[{time.time() - self.start_time:.3f}s] 主题加载完成")

        # 仅保存/加载窗口位置，不包括大小
        last_position = self.load_window_position()
        if last_position:
            try:
                # 直接使用返回的位置信息
                self.root.geometry(
                    f"{self.window_width}x{self.window_height}{last_position}"
                )
            except Exception as e:
                print(f"应用窗口位置时出错: {e}")

        self.window_list = None
        self.windows = []
        self.master_window = None
        self.screens = []  # 初始化屏幕列表

        # 从设置加载所有路径
        self.shortcut_path = self.settings.get("shortcut_path", "")
        self.cache_dir = self.settings.get("cache_dir", "")
        # 默认icons目录为软件目录下的icons
        default_icon_dir = os.path.join(
            os.path.dirname(os.path.abspath(__file__)), "icons"
        )
        self.icon_dir = self.settings.get("icon_dir") or default_icon_dir
        if not os.path.exists(self.icon_dir):
            try:
                os.makedirs(self.icon_dir, exist_ok=True)
            except Exception as e:
                print(f"创建图标目录失败: {str(e)}")
        # 自动修正settings.json
        if not self.settings.get("icon_dir"):
            self.settings["icon_dir"] = self.icon_dir
            self.save_settings()
        self.screen_selection = self.settings.get("screen_selection", "")

        print("初始化加载设置:", self.settings)  # 调试输出

        self.path_entry = None

        # 初始化快捷键相关属性
        self.shortcut_hook = None
        self.current_shortcut = self.settings.get("sync_shortcut", None)
        if self.current_shortcut:
            self.set_shortcut(self.current_shortcut)

        self.shell = win32com.client.Dispatch("WScript.Shell")
        self.select_all_var = tk.StringVar(value="全部选择")

        self.is_syncing = False
        self.sync_button = None
        self.mouse_hook_id = None
        self.keyboard_hook = None
        self.hook_thread = None
        self.user32 = ctypes.WinDLL("user32", use_last_error=True)
        self.sync_windows = []

        self.chrome_drivers = {}

        # 调试端口映射 - 将窗口号映射到调试端口
        self.debug_ports = {}
        # 基础调试端口
        self.base_debug_port = 9222

        self.DWMWA_BORDER_COLOR = 34
        self.DWM_MAGIC_COLOR = 0x00FF0000

        self.popup_mappings = {}

        self.popup_monitor_thread = None

        self.mouse_threshold = 3
        self.last_mouse_position = (0, 0)
        self.last_move_time = 0
        self.move_interval = 0.016

        # 设置关闭事件处理
        self.root.protocol("WM_DELETE_WINDOW", self.on_closing)

        # 创建界面
        self.create_widgets()
        self.create_styles()

        # 窗口尺寸已在初始化时固定，无需再次调整

        # 在初始化时设置进程缓解策略
        PROCESS_CREATION_MITIGATION_POLICY_BLOCK_NON_MICROSOFT_BINARIES_ALWAYS_ON = (
            0x100000000000
        )
        ctypes.windll.kernel32.SetProcessMitigationPolicy(
            0,  # ProcessSignaturePolicy
            ctypes.byref(
                ctypes.c_ulonglong(
                    PROCESS_CREATION_MITIGATION_POLICY_BLOCK_NON_MICROSOFT_BINARIES_ALWAYS_ON
                )
            ),
            ctypes.sizeof(ctypes.c_ulonglong),
        )

        # 检测Windows版本
        self.win_ver = sys.getwindowsversion()
        self.is_win11 = self.win_ver.build >= 22000

        # 初始化系统托盘通知
        try:
            if self.is_win11:
                # Windows 11使用toast通知
                self.notify_func = toast
            else:
                # Windows 10使用win32gui通知
                self.hwnd = win32gui.GetForegroundWindow()
                self.notification_flags = (
                    win32gui.NIF_ICON | win32gui.NIF_INFO | win32gui.NIF_TIP
                )

                # 加载app.ico图标
                try:
                    icon_path = os.path.join(os.path.dirname(__file__), "app.ico")
                    if os.path.exists(icon_path):
                        # 加载应用程序图标
                        icon_handle = win32gui.LoadImage(
                            0,
                            icon_path,
                            win32con.IMAGE_ICON,
                            0,
                            0,
                            win32con.LR_LOADFROMFILE | win32con.LR_DEFAULTSIZE,
                        )
                    else:
                        # 使用默认图标
                        icon_handle = win32gui.LoadIcon(0, win32con.IDI_APPLICATION)
                except Exception as e:
                    print(f"加载托盘图标失败: {str(e)}")
                    icon_handle = win32gui.LoadIcon(0, win32con.IDI_APPLICATION)

                self.notify_id = (
                    self.hwnd,
                    0,
                    self.notification_flags,
                    win32con.WM_USER + 20,
                    icon_handle,
                    "Chrome多窗口管理器",
                )

                # 先注册托盘图标
                try:
                    win32gui.Shell_NotifyIcon(win32gui.NIM_ADD, self.notify_id)
                except Exception as e:
                    print(f"注册托盘图标失败: {str(e)}")
        except Exception as e:
            print(f"初始化通知功能失败: {str(e)}")

        # 创建右键菜单
        self.context_menu = tk.Menu(self.root, tearoff=0)
        self.context_menu.add_command(label="剪切", command=self.cut_text)
        self.context_menu.add_command(label="复制", command=self.copy_text)
        self.context_menu.add_command(label="粘贴", command=self.paste_text)
        self.context_menu.add_separator()
        self.context_menu.add_command(label="全选", command=self.select_all_text)

        # 保存当前焦点的文本框引用
        self.current_text_widget = None

        # 添加CDP WebSocket连接池
        # self.ws_connections = {}
        # self.ws_lock = threading.Lock()
        # self.scroll_sync_enabled = True  # 添加滚轮同步控制标志

        # 安排延迟初始化
        self.root.after(100, self.delayed_initialization)
        print(f"[{time.time() - self.start_time:.3f}s] __init__ 完成, 已安排延迟初始化")

    def create_styles(self):
        style = ttk.Style()

        default_font = ("Microsoft YaHei UI", 9)

        style.configure("Small.TEntry", padding=(4, 0), font=default_font)

        style.configure("TButton", font=default_font)
        style.configure("TLabel", font=default_font)
        style.configure("TEntry", font=default_font)
        style.configure("Treeview", font=default_font)
        style.configure("Treeview.Heading", font=default_font)
        style.configure("TLabelframe.Label", font=default_font)
        style.configure("TNotebook.Tab", font=default_font)

        if self.window_list:
            self.window_list.tag_configure(
                "master", background="#0d6efd", foreground="white"
            )

        # 链接样式
        style.configure(
            "Link.TLabel",
            foreground="#0d6efd",
            cursor="hand2",
            font=("Microsoft YaHei UI", 9, "underline"),
        )

    def create_widgets(self):
        """创建界面元素"""
        main_frame = ttk.Frame(self.root)
        main_frame.pack(fill=tk.X, padx=10, pady=5)

        upper_frame = ttk.Frame(main_frame)
        upper_frame.pack(fill=tk.X)

        arrange_frame = ttk.LabelFrame(upper_frame, text="自定义排列")
        arrange_frame.pack(side=tk.RIGHT, fill=tk.Y, padx=(3, 0))

        manage_frame = ttk.LabelFrame(upper_frame, text="窗口管理")
        manage_frame.pack(side=tk.LEFT, fill=tk.BOTH, expand=True, padx=(0, 5))

        # 创建两行按钮区域
        button_rows = ttk.Frame(manage_frame)
        button_rows.pack(fill=tk.X)

        # 第一行：基本操作按钮
        first_row = ttk.Frame(button_rows)
        first_row.pack(fill=tk.X)

        ttk.Button(
            first_row,
            text="导入窗口",
            command=self.import_windows,
            style="Accent.TButton",
        ).pack(side=tk.LEFT, padx=2)
        select_all_label = ttk.Label(
            first_row, textvariable=self.select_all_var, style="Link.TLabel"
        )
        select_all_label.pack(side=tk.LEFT, padx=5)
        select_all_label.bind("<Button-1>", self.toggle_select_all)
        ttk.Button(first_row, text="自动排列", command=self.auto_arrange_windows).pack(
            side=tk.LEFT, padx=2
        )
        ttk.Button(
            first_row, text="关闭选中", command=self.close_selected_windows
        ).pack(side=tk.LEFT, padx=2)

        self.sync_button = ttk.Button(
            first_row,
            text="▶ 开始同步",
            command=self.toggle_sync,
            style="Accent.TButton",
        )
        self.sync_button.pack(side=tk.LEFT, padx=5)

        # 添加设置按钮
        ttk.Button(
            first_row, text="🔗 设置", command=self.show_settings_dialog, width=8
        ).pack(side=tk.LEFT, padx=2)

        list_frame = ttk.Frame(manage_frame)
        list_frame.pack(fill=tk.BOTH, expand=True, pady=(2, 0))

        # 创建窗口列表
        self.window_list = ttk.Treeview(
            list_frame,
            columns=("select", "number", "title", "master", "hwnd"),
            show="headings",
            height=4,
            style="Accent.Treeview",
        )
        self.window_list.pack(side=tk.LEFT, fill=tk.BOTH, expand=True)

        self.window_list.heading("select", text="选择")
        self.window_list.heading("number", text="窗口序号")
        self.window_list.heading("title", text="页面标题")
        self.window_list.heading("master", text="主控")
        self.window_list.heading("hwnd", text="")

        self.window_list.column("select", width=50, anchor="center")
        self.window_list.column("number", width=60, anchor="center")
        self.window_list.column("title", width=260)
        self.window_list.column("master", width=50, anchor="center")
        self.window_list.column("hwnd", width=0, stretch=False)  # 隐藏hwnd列

        self.window_list.tag_configure("master", background="lightblue")

        self.window_list.bind("<Button-1>", self.on_click)

        # 添加右键菜单功能
        self.window_list_menu = tk.Menu(self.root, tearoff=0)
        self.window_list_menu.add_command(
            label="关闭此窗口", command=self.close_selected_window
        )
        self.window_list.bind("<Button-3>", self.show_window_list_menu)

        # 添加滚动条
        scrollbar = ttk.Scrollbar(
            list_frame, orient=tk.VERTICAL, command=self.window_list.yview
        )
        scrollbar.pack(side=tk.RIGHT, fill=tk.Y)
        self.window_list.configure(yscrollcommand=scrollbar.set)

        params_frame = ttk.Frame(arrange_frame)
        params_frame.pack(fill=tk.X, padx=5, pady=2)

        left_frame = ttk.Frame(params_frame)
        left_frame.pack(side=tk.LEFT, padx=(0, 5))
        right_frame = ttk.Frame(params_frame)
        right_frame.pack(side=tk.LEFT)

        ttk.Label(left_frame, text="起始X坐标").pack(anchor=tk.W)
        self.start_x = ttk.Entry(left_frame, width=8, style="Small.TEntry")
        self.start_x.pack(fill=tk.X, pady=(0, 2))
        self.start_x.insert(0, "0")
        self.setup_right_click_menu(self.start_x)

        ttk.Label(left_frame, text="窗口宽度").pack(anchor=tk.W)
        self.window_width = ttk.Entry(left_frame, width=8, style="Small.TEntry")
        self.window_width.pack(fill=tk.X, pady=(0, 2))
        self.window_width.insert(0, "500")
        self.setup_right_click_menu(self.window_width)

        ttk.Label(left_frame, text="水平间距").pack(anchor=tk.W)
        self.h_spacing = ttk.Entry(left_frame, width=8, style="Small.TEntry")
        self.h_spacing.pack(fill=tk.X, pady=(0, 2))
        self.h_spacing.insert(0, "0")
        self.setup_right_click_menu(self.h_spacing)

        ttk.Label(right_frame, text="起始Y坐标").pack(anchor=tk.W)
        self.start_y = ttk.Entry(right_frame, width=8, style="Small.TEntry")
        self.start_y.pack(fill=tk.X, pady=(0, 2))
        self.start_y.insert(0, "0")
        self.setup_right_click_menu(self.start_y)

        ttk.Label(right_frame, text="窗口高度").pack(anchor=tk.W)
        self.window_height = ttk.Entry(right_frame, width=8, style="Small.TEntry")
        self.window_height.pack(fill=tk.X, pady=(0, 2))
        self.window_height.insert(0, "400")
        self.setup_right_click_menu(self.window_height)

        ttk.Label(right_frame, text="垂直间距").pack(anchor=tk.W)
        self.v_spacing = ttk.Entry(right_frame, width=8, style="Small.TEntry")
        self.v_spacing.pack(fill=tk.X, pady=(0, 2))
        self.v_spacing.insert(0, "0")
        self.setup_right_click_menu(self.v_spacing)

        for widget in left_frame.winfo_children() + right_frame.winfo_children():
            if isinstance(widget, ttk.Entry):
                widget.pack_configure(pady=(0, 2))

        bottom_frame = ttk.Frame(arrange_frame)
        bottom_frame.pack(fill=tk.X, padx=5, pady=2)

        row_frame = ttk.Frame(bottom_frame)
        row_frame.pack(side=tk.LEFT)
        ttk.Label(row_frame, text="每行窗口数").pack(anchor=tk.W)
        self.windows_per_row = ttk.Entry(row_frame, width=8, style="Small.TEntry")
        self.windows_per_row.pack(pady=(2, 0))
        self.windows_per_row.insert(0, "5")
        self.setup_right_click_menu(self.windows_per_row)

        ttk.Button(
            bottom_frame,
            text="自定义排列",
            command=self.custom_arrange_windows,
            style="Accent.TButton",
        ).pack(side=tk.RIGHT, pady=(15, 0))

        bottom_frame = ttk.Frame(self.root)
        bottom_frame.pack(fill=tk.X, padx=10, pady=(5, 0))

        self.tab_control = ttk.Notebook(bottom_frame)
        self.tab_control.pack(side=tk.LEFT, fill=tk.X, expand=True)

        # 打开窗口标签
        open_window_tab = ttk.Frame(self.tab_control)
        self.tab_control.add(open_window_tab, text="打开窗口")

        # 简化布局结构，移除多余的嵌套frame
        numbers_frame = ttk.Frame(open_window_tab)
        numbers_frame.pack(fill=tk.X, padx=10, pady=10)  # 统一顶部边距
        ttk.Label(numbers_frame, text="窗口编号:").pack(side=tk.LEFT)
        self.numbers_entry = ttk.Entry(numbers_frame, width=20)
        self.numbers_entry.pack(side=tk.LEFT, padx=5)
        self.setup_right_click_menu(self.numbers_entry)

        settings = self.load_settings()
        if "last_window_numbers" in settings:
            self.numbers_entry.insert(0, settings["last_window_numbers"])

        self.numbers_entry.bind("<Return>", lambda e: self.open_windows())

        ttk.Button(
            numbers_frame,
            text="打开窗口",
            command=self.open_windows,
            style="Accent.TButton",
        ).pack(side=tk.LEFT, padx=5)

        # 添加示例文字
        ttk.Label(numbers_frame, text="示例: 1-5 或 1,3,5").pack(side=tk.LEFT, padx=5)

        # 批量打开网页标签
        url_tab = ttk.Frame(self.tab_control)
        self.tab_control.add(url_tab, text="批量打开网页")

        url_frame = ttk.Frame(url_tab)
        url_frame.pack(fill=tk.X, padx=10, pady=10)  # 统一边距
        ttk.Label(url_frame, text="网址:").pack(side=tk.LEFT)
        self.url_entry = ttk.Entry(url_frame, width=20)
        self.url_entry.pack(side=tk.LEFT, padx=5)
        self.url_entry.insert(0, "www.google.com")

        self.url_entry.bind("<Return>", lambda e: self.batch_open_urls())

        ttk.Button(
            url_frame,
            text="批量打开",
            command=self.batch_open_urls,
            style="Accent.TButton",  # 设置蓝色风格
        ).pack(side=tk.LEFT, padx=5)

        # 添加几个常用网站快速打开按钮
        twitter_button = ttk.Button(
            url_frame,
            text="Twitter",
            command=lambda: self.set_quick_url("https://twitter.com"),
            style="Quick.TButton",  # 使用自定义样式
            width=8,
        )
        twitter_button.pack(side=tk.LEFT, padx=2)

        discord_button = ttk.Button(
            url_frame,
            text="Discord",
            command=lambda: self.set_quick_url("https://discord.com/channels/@me"),
            style="Quick.TButton",  # 使用自定义样式
            width=8,
        )
        discord_button.pack(side=tk.LEFT, padx=2)

        gmail_button = ttk.Button(
            url_frame,
            text="Gmail",
            command=lambda: self.set_quick_url("https://mail.google.com"),
            style="Quick.TButton",  # 使用自定义样式
            width=8,
        )
        gmail_button.pack(side=tk.LEFT, padx=2)

        # 标签页管理标签
        tab_manage_tab = ttk.Frame(self.tab_control)
        self.tab_control.add(tab_manage_tab, text="标签页管理")

        tab_manage_frame = ttk.Frame(tab_manage_tab)
        tab_manage_frame.pack(fill=tk.X, padx=10, pady=10)

        ttk.Button(
            tab_manage_frame,
            text="仅保留当前标签页",
            command=self.keep_only_current_tab,
            width=20,
            style="Accent.TButton",  # 应用蓝色风格
        ).pack(side=tk.LEFT, padx=5)

        ttk.Button(
            tab_manage_frame,
            text="仅保留新标签页",
            command=self.keep_only_new_tab,
            width=20,
            style="Accent.TButton",  # 应用蓝色风格
        ).pack(side=tk.LEFT, padx=5)

        # 添加随机数字输入标签
        random_number_tab = ttk.Frame(self.tab_control)
        self.tab_control.add(random_number_tab, text="批量文本输入")

        # 简化界面，只添加两个按钮
        buttons_frame = ttk.Frame(random_number_tab)
        buttons_frame.pack(fill=tk.X, padx=10, pady=10)

        ttk.Button(
            buttons_frame,
            text="随机数字输入",
            command=self.show_random_number_dialog,
            width=20,
            style="Accent.TButton",  # 应用蓝色风格
        ).pack(side=tk.LEFT, padx=10)

        ttk.Button(
            buttons_frame,
            text="指定文本输入",
            command=self.show_text_input_dialog,
            width=20,
            style="Accent.TButton",  # 应用蓝色风格
        ).pack(side=tk.LEFT, padx=10)

        # 批量创建环境标签
        env_create_tab = ttk.Frame(self.tab_control)
        self.tab_control.add(env_create_tab, text="批量创建环境")

        # 统一框架布局
        input_row = ttk.Frame(env_create_tab)
        input_row.pack(fill=tk.X, padx=10, pady=10)  # 统一边距

        # 环境编号
        ttk.Label(input_row, text="创建编号:").pack(side=tk.LEFT)
        self.env_numbers = ttk.Entry(input_row, width=20)
        self.env_numbers.pack(side=tk.LEFT, padx=5)
        self.setup_right_click_menu(self.env_numbers)

        # 创建按钮
        ttk.Button(
            input_row,
            text="开始创建",
            command=self.create_environments,
            style="Accent.TButton",  # 设置蓝色风格
        ).pack(side=tk.LEFT, padx=5)

        # 示例文字
        ttk.Label(input_row, text="示例: 1-5,7,9-12").pack(side=tk.LEFT, padx=5)

        # 替换图标标签页
        icon_tab = ttk.Frame(self.tab_control)
        self.tab_control.add(icon_tab, text="替换图标")

        icon_frame = ttk.Frame(icon_tab)
        icon_frame.pack(fill=tk.X, padx=10, pady=10)  # 统一边距

        ttk.Label(icon_frame, text="窗口编号:").pack(side=tk.LEFT)
        self.icon_window_numbers = ttk.Entry(icon_frame, width=20)
        self.icon_window_numbers.pack(side=tk.LEFT, padx=5)

        ttk.Button(
            icon_frame,
            text="替换图标",
            command=self.set_taskbar_icons,
            style="Accent.TButton",  # 设置蓝色风格
        ).pack(side=tk.LEFT, padx=5)

        # 示例文字
        ttk.Label(icon_frame, text="示例: 1-5,7,9-12").pack(side=tk.LEFT, padx=5)

        # 底部按钮框架 - 在所有标签页设置完成后添加
        footer_frame = ttk.Frame(self.root)
        footer_frame.pack(side=tk.BOTTOM, fill=tk.X, padx=10, pady=5)

        # 添加左侧的超链接
        donate_frame = ttk.Frame(footer_frame)
        donate_frame.pack(side=tk.LEFT)

        donate_label = ttk.Label(
            donate_frame,
            text="铸造一个看上去没什么用的NFT 0.1SOL（其实就是打赏啦 😁）",
            cursor="hand2",
            foreground="black",
            # 移除字体设置，使用系统默认字体
        )
        donate_label.pack(side=tk.LEFT)
        donate_label.bind(
            "<Button-1>",
            lambda e: webbrowser.open("https://truffle.wtf/project/Devilflasher"),
        )

        author_frame = ttk.Frame(footer_frame)
        author_frame.pack(side=tk.RIGHT)

        ttk.Label(author_frame, text="Compiled by Devilflasher").pack(side=tk.LEFT)

        ttk.Label(author_frame, text="  ").pack(side=tk.LEFT)

        twitter_label = ttk.Label(
            author_frame, text="Twitter", cursor="hand2", font=("Arial", 9)
        )
        twitter_label.pack(side=tk.LEFT)
        twitter_label.bind(
            "<Button-1>", lambda e: webbrowser.open("https://x.com/DevilflasherX")
        )

        ttk.Label(author_frame, text="  ").pack(side=tk.LEFT)

        telegram_label = ttk.Label(
            author_frame, text="Telegram", cursor="hand2", font=("Arial", 9)
        )
        telegram_label.pack(side=tk.LEFT)
        telegram_label.bind(
            "<Button-1>", lambda e: webbrowser.open("https://t.me/devilflasher0")
        )

    def toggle_select_all(self, event=None):
        # 切换全选状态
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
            selected_count = sum(
                1 for item in items if self.window_list.set(item, "select") == "√"
            )

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
        """设置主控窗口"""
        try:
            # 如果正在同步，先停止同步
            if self.is_syncing:
                self.stop_sync()
                # 确保按钮状态更新
                self.sync_button.configure(text="▶ 开始同步", style="Accent.TButton")
                self.is_syncing = False
                # 显示通知
                self.show_notification("同步已关闭", "切换主控窗口，同步已停止")

            # 清除其他窗口的主控状态和标题
            for i in self.window_list.get_children():
                values = self.window_list.item(i)["values"]
                if values and len(values) >= 5:
                    hwnd = int(values[4])
                    title = values[2]
                    # 移除所有主控标记
                    if "★" in title or "[主控]" in title:
                        new_title = title.replace("[主控]", "").strip()
                        new_title = new_title.replace("★", "").strip()
                        win32gui.SetWindowText(hwnd, new_title)
                        # 更新列表中显示的标题
                        self.window_list.set(i, "title", new_title)
                    # 恢复默认边框颜色
                    try:
                        # 使用 LoadLibrary 显式加载 dwmapi.dll
                        dwmapi = ctypes.WinDLL("dwmapi.dll")

                        # 定义参数类型
                        DWMWA_BORDER_COLOR = 34
                        color = ctypes.c_uint(0)  # 默认颜色

                        # 恢复默认边框颜色
                        dwmapi.DwmSetWindowAttribute(
                            hwnd,
                            DWMWA_BORDER_COLOR,
                            ctypes.byref(color),
                            ctypes.sizeof(ctypes.c_int),
                        )

                        # 强制刷新窗口
                        win32gui.SetWindowPos(
                            hwnd,
                            0,
                            0,
                            0,
                            0,
                            0,
                            win32con.SWP_NOMOVE
                            | win32con.SWP_NOSIZE
                            | win32con.SWP_NOZORDER
                            | win32con.SWP_FRAMECHANGED,
                        )
                    except Exception as e:
                        print(f"重置窗口边框颜色失败: {str(e)}")
                self.window_list.set(i, "master", "")
                self.window_list.item(i, tags=())

            # 设置新的主控窗口
            values = self.window_list.item(item)["values"]
            self.master_window = int(values[4])

            # 设置主控标记和蓝色背景
            self.window_list.set(item, "master", "√")
            self.window_list.item(item, tags=("master",))

            # 修改窗口标题和边框颜色
            title = values[2]
            if not "[主控]" in title and not "★" in title:
                new_title = f"★ [主控] {title} ★"
                win32gui.SetWindowText(self.master_window, new_title)
                self.window_list.set(item, "title", new_title)
                try:
                    # 加载 dwmapi.dll
                    dwmapi = ctypes.WinDLL("dwmapi.dll")

                    # 设置窗口边框颜色为红色
                    color = ctypes.c_uint(0x0000FF)  # 红色 (BGR格式)
                    dwmapi.DwmSetWindowAttribute(
                        self.master_window,
                        34,  # DWMWA_BORDER_COLOR
                        ctypes.byref(color),
                        ctypes.sizeof(ctypes.c_int),
                    )

                    # 强制刷新窗口
                    win32gui.SetWindowPos(
                        self.master_window,
                        0,
                        0,
                        0,
                        0,
                        0,
                        win32con.SWP_NOMOVE
                        | win32con.SWP_NOSIZE
                        | win32con.SWP_NOZORDER
                        | win32con.SWP_FRAMECHANGED,
                    )
                except Exception as e:
                    print(f"设置主控窗口边框颜色失败: {str(e)}")

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
        master_items = [
            item
            for item in self.window_list.get_children()
            if self.window_list.set(item, "master") == "√"
        ]

        if not master_items:
            # 如果没有主控窗口，设置第一个选中的窗口为主控
            self.set_master_window(selected[0])

        # 切换同步状态
        if not self.is_syncing:
            try:
                self.start_sync(selected)
                self.sync_button.configure(text="■ 停止同步", style="Accent.TButton")
                self.is_syncing = True
                print("同步已开启")
                # 使用after方法异步显示通知
                self.root.after(
                    10,
                    lambda: self.show_notification(
                        "同步已开启", "Chrome多窗口同步功能已启动"
                    ),
                )
            except Exception as e:
                print(f"开启同步失败: {str(e)}")
                # 确保状态正确
                self.is_syncing = False
                self.sync_button.configure(text="▶ 开始同步", style="Accent.TButton")
                # 重新显示错误消息
                messagebox.showerror("错误", str(e))
        else:
            try:
                self.stop_sync()
                self.sync_button.configure(text="▶ 开始同步", style="Accent.TButton")
                self.is_syncing = False
                print("同步已停止")
                # 使用after方法异步显示通知
                self.root.after(
                    10,
                    lambda: self.show_notification(
                        "同步已关闭", "Chrome多窗口同步功能已停止"
                    ),
                )
            except Exception as e:
                print(f"停止同步失败: {str(e)}")

    def show_notification(self, title, message):
        """显示系统通知"""
        try:
            if self.is_win11:
                # Windows 11 使用toast通知
                try:
                    # 使用线程来显示通知
                    def show_toast():
                        try:
                            self.notify_func(
                                title,
                                message,
                                duration="short",
                                app_id="Chrome多开管理工具",
                                on_dismissed=lambda x: None,  # 忽略关闭回调
                            )
                        except Exception:
                            pass

                    threading.Thread(target=show_toast).start()
                except TypeError:
                    # 如果上面的方法失败，尝试使用另一种调用方式
                    def show_toast_alt():
                        try:
                            self.notify_func(
                                {
                                    "title": title,
                                    "message": message,
                                    "duration": "short",
                                    "app_id": "Chrome多开管理工具",
                                    "on_dismissed": lambda x: None,
                                }
                            )
                        except Exception:
                            pass

                    threading.Thread(target=show_toast_alt).start()
            else:
                # Windows 10 使用win32gui通知
                try:
                    # 确保托盘图标已注册
                    if not hasattr(self, "notify_id"):
                        self.hwnd = win32gui.GetForegroundWindow()
                        self.notification_flags = (
                            win32gui.NIF_ICON | win32gui.NIF_INFO | win32gui.NIF_TIP
                        )

                        # 加载app.ico图标
                        try:
                            icon_path = os.path.join(
                                os.path.dirname(__file__), "app.ico"
                            )
                            if os.path.exists(icon_path):
                                # 加载应用程序图标
                                icon_handle = win32gui.LoadImage(
                                    0,
                                    icon_path,
                                    win32con.IMAGE_ICON,
                                    0,
                                    0,
                                    win32con.LR_LOADFROMFILE | win32con.LR_DEFAULTSIZE,
                                )
                            else:
                                # 使用默认图标
                                icon_handle = win32gui.LoadIcon(
                                    0, win32con.IDI_APPLICATION
                                )
                        except Exception as e:
                            print(f"加载托盘图标失败: {str(e)}")
                            icon_handle = win32gui.LoadIcon(0, win32con.IDI_APPLICATION)

                        self.notify_id = (
                            self.hwnd,
                            0,
                            self.notification_flags,
                            win32con.WM_USER + 20,
                            icon_handle,
                            "Chrome多窗口管理器",
                        )
                        win32gui.Shell_NotifyIcon(win32gui.NIM_ADD, self.notify_id)

                    # 获取当前图标句柄
                    icon_handle = self.notify_id[4]

                    # 准备通知数据
                    notify_data = (
                        self.hwnd,
                        0,
                        self.notification_flags,
                        win32con.WM_USER + 20,
                        icon_handle,
                        "Chrome多窗口管理器",  # 托盘提示
                        message,  # 通知内容
                        1000,  # 1秒 = 1000毫秒
                        title,  # 通知标题
                        win32gui.NIIF_INFO,  # 通知类型
                    )
                    # 显示通知
                    win32gui.Shell_NotifyIcon(win32gui.NIM_MODIFY, notify_data)
                except Exception as e:
                    print(f"Windows 10 通知显示失败: {str(e)}")
        except Exception as e:
            print(f"显示通知失败: {str(e)}")

    def start_sync(self, selected_items):
        try:
            # 确保主控窗口存在
            if not self.master_window:
                raise Exception("未设置主控窗口")

            # 清除之前可能的同步状态
            if hasattr(self, "is_sync") and self.is_sync:
                self.stop_sync()
                time.sleep(0.2)  # 等待资源清理

            # 初始化同步状态变量
            self.is_sync = True
            self.popup_windows = []  # 储存所有弹出窗口
            self.last_mouse_position = (0, 0)
            self.last_move_time = time.time()

            # 保存选中的窗口列表，并按编号排序
            self.sync_windows = []
            window_info = []

            # 收集所有选中的窗口
            for item in selected_items:
                values = self.window_list.item(item)["values"]
                if values and len(values) >= 5:
                    number = int(values[1])
                    hwnd = int(values[4])
                    if hwnd != self.master_window:  # 排除主控窗口
                        window_info.append((number, hwnd))

            # 按编号排序
            window_info.sort(key=lambda x: x[0])

            # 保存所有同步窗口的句柄
            self.sync_windows = [hwnd for _, hwnd in window_info]

            # 检查是否存在有效的同步窗口
            if not self.sync_windows:
                messagebox.showwarning(
                    "警告", "没有可同步的窗口，请至少选择两个窗口（一个主控，一个被控）"
                )
                self.is_sync = False
                return

            # 启动键盘和鼠标钩子
            if (
                not hasattr(self, "hook_thread")
                or not self.hook_thread
                or not self.hook_thread.is_alive()
            ):
                self.hook_thread = threading.Thread(target=self.message_loop)
                self.hook_thread.daemon = True
                self.hook_thread.start()

                try:
                    # 设置键盘和鼠标钩子
                    keyboard.hook(self.on_keyboard_event)
                    mouse.hook(self.on_mouse_event)
                    print("已设置键盘和鼠标钩子")

                    # 尝试安装低级滚轮钩子，但不强制要求成功
                    if self.use_wheel_hook:
                        try:
                            self.setup_wheel_hook()
                            print("已安装低级滚轮钩子")
                        except Exception as e:
                            print(f"安装低级滚轮钩子失败: {str(e)}")
                            print("将使用常规鼠标钩子代替低级滚轮钩子")
                            # 失败后设置为不使用低级钩子
                            self.use_wheel_hook = False
                except Exception as e:
                    print(f"设置钩子失败: {str(e)}")
                    self.stop_sync()
                    raise Exception(f"无法设置输入钩子: {str(e)}")

                # 更新按钮状态
                self.sync_button.configure(text="■ 停止同步", style="Accent.TButton")

                # 启动插件窗口监控线程
                self.popup_monitor_thread = threading.Thread(target=self.monitor_popups)
                self.popup_monitor_thread.daemon = True
                self.popup_monitor_thread.start()

                print(
                    f"已启动同步，主控窗口: {self.master_window}, 同步窗口: {self.sync_windows}"
                )

            # 添加：将所有窗口设置为置顶
            for hwnd in self.sync_windows:
                try:
                    # 设置窗口为置顶
                    win32gui.SetWindowPos(
                        hwnd,
                        win32con.HWND_TOPMOST,
                        0,
                        0,
                        0,
                        0,
                        win32con.SWP_NOMOVE | win32con.SWP_NOSIZE,
                    )
                    # 取消置顶（但保持在所有窗口前面）
                    win32gui.SetWindowPos(
                        hwnd,
                        win32con.HWND_NOTOPMOST,
                        0,
                        0,
                        0,
                        0,
                        win32con.SWP_NOMOVE | win32con.SWP_NOSIZE,
                    )
                except Exception as e:
                    print(f"设置窗口 {hwnd} 置顶失败: {str(e)}")

            # 添加：将主窗口设置为活动窗口
            try:
                # 确保主窗口可见
                win32gui.ShowWindow(self.master_window, win32con.SW_RESTORE)
                # 设置主窗口为前台窗口
                win32gui.SetForegroundWindow(self.master_window)
                print(f"已激活主窗口: {self.master_window}")
            except Exception as e:
                print(f"激活主窗口失败: {str(e)}")

        except Exception as e:
            self.stop_sync()  # 确保清理资源
            messagebox.showerror("错误", f"开启同步失败: {str(e)}")
            print(f"开启同步失败: {str(e)}")

    def message_loop(self):
        # 消息循环 - 优化版本，降低CPU使用率
        while self.is_sync:
            # 增加更长的睡眠时间，减少CPU使用
            time.sleep(0.005)  # 5ms睡眠，平衡响应性和CPU使用率

    def on_mouse_event(self, event):
        try:
            if self.is_sync:
                current_window = win32gui.GetForegroundWindow()

                # 检查是否是主控窗口或其插件窗口
                is_master = current_window == self.master_window
                master_popups = self.get_chrome_popups(self.master_window)

                # 检查是否是主窗口的弹出窗口之一
                is_popup = False
                if not is_master and current_window in master_popups:
                    is_popup = True
                    # 确保这个弹出窗口在我们的同步列表中
                    if current_window not in self.popup_windows:
                        self.popup_windows.append(current_window)

                # 只有当当前窗口是主控窗口或其弹出窗口时才处理事件
                # 这样可以防止其他窗口控制同步
                if is_master or is_popup:
                    # 获取鼠标位置
                    x, y = mouse.get_position()

                    # 获取当前窗口的矩形区域
                    current_rect = win32gui.GetWindowRect(current_window)

                    # 检查鼠标是否在当前窗口范围内
                    mouse_in_window = (
                        x >= current_rect[0]
                        and x <= current_rect[2]
                        and y >= current_rect[1]
                        and y <= current_rect[3]
                    )

                    # 只有当鼠标在窗口范围内时才进行同步
                    if not mouse_in_window:
                        return

                    # 对于移动事件进行优化
                    if isinstance(event, mouse.MoveEvent):
                        # 改进的移动事件节流策略
                        current_time = time.time()
                        if not hasattr(self, "move_interval"):
                            self.move_interval = 0.01  # 10ms节流间隔

                        # 更精细的移动阈值控制
                        if not hasattr(self, "mouse_threshold"):
                            self.mouse_threshold = 2  # 像素移动阈值

                        # 时间节流：忽略过于频繁的移动事件
                        if (
                            current_time - getattr(self, "last_move_time", 0)
                            < self.move_interval
                        ):
                            return

                        # 距离节流：忽略过小的移动
                        last_pos = getattr(
                            self, "last_mouse_position", (event.x, event.y)
                        )
                        dx = abs(event.x - last_pos[0])
                        dy = abs(event.y - last_pos[1])
                        if dx < self.mouse_threshold and dy < self.mouse_threshold:
                            return

                        # 更新上次位置和时间
                        self.last_mouse_position = (event.x, event.y)
                        self.last_move_time = current_time

                    # 计算当前窗口的相对坐标
                    rel_x = (x - current_rect[0]) / max(
                        (current_rect[2] - current_rect[0]), 1
                    )
                    rel_y = (y - current_rect[1]) / max(
                        (current_rect[3] - current_rect[1]), 1
                    )

                    # 使用线程池批量处理事件分发
                    sync_tasks = []

                    # 同步到其他窗口
                    for hwnd in self.sync_windows:
                        try:
                            # 确定目标窗口
                            if is_master:
                                target_hwnd = hwnd
                            else:
                                # 查找对应的扩展程序窗口
                                target_popups = self.get_chrome_popups(hwnd)

                                # 检查当前窗口是否为弹出类型的浮动窗口
                                style = win32gui.GetWindowLong(
                                    current_window, win32con.GWL_STYLE
                                )
                                is_floating = (style & win32con.WS_POPUP) != 0
                                current_title = win32gui.GetWindowText(current_window)

                                if is_floating and target_popups:
                                    # 按照相对位置和窗口标题匹配浮动窗口
                                    best_match = None
                                    min_diff = float("inf")
                                    current_size = (
                                        current_rect[2] - current_rect[0],
                                        current_rect[3] - current_rect[1],
                                    )

                                    for popup in target_popups:
                                        # 获取目标弹出窗口信息
                                        popup_rect = win32gui.GetWindowRect(popup)
                                        popup_style = win32gui.GetWindowLong(
                                            popup, win32con.GWL_STYLE
                                        )
                                        popup_title = win32gui.GetWindowText(popup)

                                        # 检查是否也是浮动窗口
                                        if (popup_style & win32con.WS_POPUP) == 0:
                                            continue

                                        # 计算窗口大小差异
                                        popup_size = (
                                            popup_rect[2] - popup_rect[0],
                                            popup_rect[3] - popup_rect[1],
                                        )
                                        size_diff = abs(
                                            current_size[0] - popup_size[0]
                                        ) + abs(current_size[1] - popup_size[1])

                                        # 计算标题相似度
                                        title_sim = self.title_similarity(
                                            current_title, popup_title
                                        )

                                        # 综合评分
                                        diff = size_diff * (2.0 - title_sim)

                                        if diff < min_diff:
                                            min_diff = diff
                                            best_match = popup

                                    target_hwnd = best_match if best_match else hwnd
                                else:
                                    # 按照相对位置匹配
                                    best_match = None
                                    min_diff = float("inf")
                                    for popup in target_popups:
                                        popup_rect = win32gui.GetWindowRect(popup)
                                        master_rect = win32gui.GetWindowRect(
                                            current_window
                                        )
                                        # 计算相对位置差异
                                        master_rel_x = (
                                            master_rect[0]
                                            - win32gui.GetWindowRect(
                                                self.master_window
                                            )[0]
                                        )
                                        master_rel_y = (
                                            master_rect[1]
                                            - win32gui.GetWindowRect(
                                                self.master_window
                                            )[1]
                                        )
                                        popup_rel_x = (
                                            popup_rect[0]
                                            - win32gui.GetWindowRect(hwnd)[0]
                                        )
                                        popup_rel_y = (
                                            popup_rect[1]
                                            - win32gui.GetWindowRect(hwnd)[1]
                                        )

                                        diff = abs(master_rel_x - popup_rel_x) + abs(
                                            master_rel_y - popup_rel_y
                                        )
                                        if diff < min_diff:
                                            min_diff = diff
                                            best_match = popup
                                    target_hwnd = best_match if best_match else hwnd

                            if not target_hwnd:
                                continue

                            # 获取目标窗口尺寸
                            target_rect = win32gui.GetWindowRect(target_hwnd)

                            # 计算目标坐标 - 保护除以零
                            client_x = int((target_rect[2] - target_rect[0]) * rel_x)
                            client_y = int((target_rect[3] - target_rect[1]) * rel_y)
                            lparam = win32api.MAKELONG(client_x, client_y)

                            # 使用PostMessage代替SendMessage提高性能
                            # 处理滚轮事件
                            if isinstance(event, mouse.WheelEvent):
                                try:
                                    wheel_delta = int(event.delta)
                                    if keyboard.is_pressed("ctrl"):
                                        if wheel_delta > 0:
                                            win32gui.PostMessage(
                                                target_hwnd,
                                                win32con.WM_KEYDOWN,
                                                win32con.VK_CONTROL,
                                                0,
                                            )
                                            win32gui.PostMessage(
                                                target_hwnd,
                                                win32con.WM_KEYDOWN,
                                                0xBB,
                                                0,
                                            )  # VK_OEM_PLUS
                                            win32gui.PostMessage(
                                                target_hwnd, win32con.WM_KEYUP, 0xBB, 0
                                            )
                                            win32gui.PostMessage(
                                                target_hwnd,
                                                win32con.WM_KEYUP,
                                                win32con.VK_CONTROL,
                                                0,
                                            )
                                        else:
                                            win32gui.PostMessage(
                                                target_hwnd,
                                                win32con.WM_KEYDOWN,
                                                win32con.VK_CONTROL,
                                                0,
                                            )
                                            win32gui.PostMessage(
                                                target_hwnd,
                                                win32con.WM_KEYDOWN,
                                                0xBD,
                                                0,
                                            )  # VK_OEM_MINUS
                                            win32gui.PostMessage(
                                                target_hwnd, win32con.WM_KEYUP, 0xBD, 0
                                            )
                                            win32gui.PostMessage(
                                                target_hwnd,
                                                win32con.WM_KEYUP,
                                                win32con.VK_CONTROL,
                                                0,
                                            )
                                    else:
                                        # 获取滚轮方向和绝对值
                                        abs_delta = abs(wheel_delta)
                                        scroll_up = wheel_delta > 0

                                        # 主要使用PageUp/PageDown键来实现更大的滚动幅度
                                        # 对于小幅度滚动，使用箭头键；对于大幅度滚动，使用Page键

                                        # 根据滚动大小决定策略，微调使同步窗口滚动幅度更接近主窗口
                                        if abs_delta <= 1:
                                            # 对于小幅度滚动，减少到2次箭头键
                                            vk_code = (
                                                win32con.VK_UP
                                                if scroll_up
                                                else win32con.VK_DOWN
                                            )
                                            for _ in range(2):
                                                win32gui.PostMessage(
                                                    target_hwnd,
                                                    win32con.WM_KEYDOWN,
                                                    vk_code,
                                                    0,
                                                )
                                                win32gui.PostMessage(
                                                    target_hwnd,
                                                    win32con.WM_KEYUP,
                                                    vk_code,
                                                    0,
                                                )
                                        elif abs_delta <= 3:
                                            # 对于中等幅度滚动，使用一次Page键但减少额外的箭头键
                                            page_vk = (
                                                win32con.VK_PRIOR
                                                if scroll_up
                                                else win32con.VK_NEXT
                                            )  # Page Up/Down
                                            win32gui.PostMessage(
                                                target_hwnd,
                                                win32con.WM_KEYDOWN,
                                                page_vk,
                                                0,
                                            )
                                            win32gui.PostMessage(
                                                target_hwnd,
                                                win32con.WM_KEYUP,
                                                page_vk,
                                                0,
                                            )

                                            # 额外只增加1次箭头键，减少之前的额外按键
                                            vk_code = (
                                                win32con.VK_UP
                                                if scroll_up
                                                else win32con.VK_DOWN
                                            )
                                            win32gui.PostMessage(
                                                target_hwnd,
                                                win32con.WM_KEYDOWN,
                                                vk_code,
                                                0,
                                            )
                                            win32gui.PostMessage(
                                                target_hwnd,
                                                win32con.WM_KEYUP,
                                                vk_code,
                                                0,
                                            )
                                        else:
                                            # 对于大幅度滚动，减少Page键系数
                                            page_count = min(
                                                int(abs_delta * 0.4), 2
                                            )  # 系数从0.6降到0.4，最多减少到2次
                                            page_vk = (
                                                win32con.VK_PRIOR
                                                if scroll_up
                                                else win32con.VK_NEXT
                                            )

                                            for _ in range(page_count):
                                                win32gui.PostMessage(
                                                    target_hwnd,
                                                    win32con.WM_KEYDOWN,
                                                    page_vk,
                                                    0,
                                                )
                                                win32gui.PostMessage(
                                                    target_hwnd,
                                                    win32con.WM_KEYUP,
                                                    page_vk,
                                                    0,
                                                )

                                            # 移除额外的箭头键调整

                                except Exception as e:
                                    print(f"处理滚轮事件失败: {str(e)}")
                                    continue

                            # 处理鼠标点击
                            elif isinstance(event, mouse.ButtonEvent):
                                if event.event_type == mouse.DOWN:
                                    if event.button == mouse.LEFT:
                                        win32gui.PostMessage(
                                            target_hwnd,
                                            win32con.WM_LBUTTONDOWN,
                                            win32con.MK_LBUTTON,
                                            lparam,
                                        )
                                    elif event.button == mouse.RIGHT:
                                        win32gui.PostMessage(
                                            target_hwnd,
                                            win32con.WM_RBUTTONDOWN,
                                            win32con.MK_RBUTTON,
                                            lparam,
                                        )
                                    elif event.button == mouse.MIDDLE:  # 添加中键支持
                                        win32gui.PostMessage(
                                            target_hwnd,
                                            win32con.WM_MBUTTONDOWN,
                                            win32con.MK_MBUTTON,
                                            lparam,
                                        )
                                elif event.event_type == mouse.UP:
                                    if event.button == mouse.LEFT:
                                        win32gui.PostMessage(
                                            target_hwnd,
                                            win32con.WM_LBUTTONUP,
                                            0,
                                            lparam,
                                        )
                                    elif event.button == mouse.RIGHT:
                                        win32gui.PostMessage(
                                            target_hwnd,
                                            win32con.WM_RBUTTONUP,
                                            0,
                                            lparam,
                                        )
                                    elif event.button == mouse.MIDDLE:  # 添加中键支持
                                        win32gui.PostMessage(
                                            target_hwnd,
                                            win32con.WM_MBUTTONUP,
                                            0,
                                            lparam,
                                        )

                            # 处理鼠标移动 - 减少移动事件传递，仅对实质性移动做处理
                            elif isinstance(event, mouse.MoveEvent):
                                win32gui.PostMessage(
                                    target_hwnd, win32con.WM_MOUSEMOVE, 0, lparam
                                )

                        except Exception as e:
                            error_msg = str(e)
                            # 减少错误日志输出频率
                            if (
                                not hasattr(self, "last_error_time")
                                or time.time() - self.last_error_time > 5
                            ):
                                print(f"同步到窗口 {hwnd} 失败: {error_msg}")
                                self.last_error_time = time.time()
        except Exception as e:
            print(f"鼠标事件处理总体错误: {str(e)}")

    def on_keyboard_event(self, event):
        # 优化版键盘事件处理
        try:
            if self.is_sync:
                current_window = win32gui.GetForegroundWindow()

                # 检查是否是主控窗口或其插件窗口
                is_master = current_window == self.master_window
                master_popups = self.get_chrome_popups(self.master_window)

                # 检查是否是主窗口的弹出窗口之一
                is_popup = False
                if not is_master and current_window in master_popups:
                    is_popup = True
                    # 确保这个弹出窗口在我们的同步列表中
                    if current_window not in self.popup_windows:
                        self.popup_windows.append(current_window)

                # 只有当当前窗口是主控窗口或其弹出窗口时才处理事件
                # 这样可以防止其他窗口控制同步
                if is_master or is_popup:
                    # 获取鼠标位置
                    x, y = mouse.get_position()

                    # 获取当前窗口的矩形区域
                    current_rect = win32gui.GetWindowRect(current_window)

                    # 检查鼠标是否在当前窗口范围内
                    mouse_in_window = (
                        x >= current_rect[0]
                        and x <= current_rect[2]
                        and y >= current_rect[1]
                        and y <= current_rect[3]
                    )

                    # 只有当鼠标在窗口范围内时才进行同步
                    if not mouse_in_window:
                        return

                    # 获取实际的输入目标窗口
                    input_hwnd = win32gui.GetFocus()

                    # 同步到其他窗口 - 键盘事件限流
                    current_time = time.time()
                    if (
                        not hasattr(self, "last_key_time")
                        or current_time - self.last_key_time > 0.01
                    ):
                        self.last_key_time = current_time
                    else:
                        # 对于连续的相同按键，适当限流，减少重复输入
                        if (
                            hasattr(self, "last_key")
                            and self.last_key == event.name
                            and event.event_type == keyboard.KEY_DOWN
                        ):
                            return

                    # 记录最后一个按键
                    self.last_key = event.name

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
                                min_diff = float("inf")
                                for popup in target_popups:
                                    popup_rect = win32gui.GetWindowRect(popup)
                                    master_rect = win32gui.GetWindowRect(current_window)
                                    # 计算相对位置差异
                                    master_rel_x = (
                                        master_rect[0]
                                        - win32gui.GetWindowRect(self.master_window)[0]
                                    )
                                    master_rel_y = (
                                        master_rect[1]
                                        - win32gui.GetWindowRect(self.master_window)[1]
                                    )
                                    popup_rel_x = (
                                        popup_rect[0] - win32gui.GetWindowRect(hwnd)[0]
                                    )
                                    popup_rel_y = (
                                        popup_rect[1] - win32gui.GetWindowRect(hwnd)[1]
                                    )

                                    diff = abs(master_rel_x - popup_rel_x) + abs(
                                        master_rel_y - popup_rel_y
                                    )
                                    if diff < min_diff:
                                        min_diff = diff
                                        best_match = popup
                                target_hwnd = best_match if best_match else hwnd

                            if not target_hwnd:
                                continue

                            # 检测组合键状态
                            modifiers = 0
                            modifier_keys = {
                                "ctrl": {
                                    "pressed": keyboard.is_pressed("ctrl"),
                                    "vk": win32con.VK_CONTROL,
                                    "flag": win32con.MOD_CONTROL,
                                },
                                "alt": {
                                    "pressed": keyboard.is_pressed("alt"),
                                    "vk": win32con.VK_MENU,
                                    "flag": win32con.MOD_ALT,
                                },
                                "shift": {
                                    "pressed": keyboard.is_pressed("shift"),
                                    "vk": win32con.VK_SHIFT,
                                    "flag": win32con.MOD_SHIFT,
                                },
                            }

                            # 处理修饰键和组合键
                            for mod_name, mod_info in modifier_keys.items():
                                if mod_info["pressed"]:
                                    # 按下修饰键
                                    if event.event_type == keyboard.KEY_DOWN:
                                        win32gui.PostMessage(
                                            target_hwnd,
                                            win32con.WM_KEYDOWN,
                                            mod_info["vk"],
                                            0,
                                        )

                                    modifiers |= mod_info["flag"]

                            # 处理 Ctrl+组合键的特殊情况
                            if modifier_keys["ctrl"]["pressed"] and event.name in [
                                "a",
                                "c",
                                "v",
                                "x",
                                "z",
                            ]:
                                vk_code = ord(event.name.upper())
                                if event.event_type == keyboard.KEY_DOWN:
                                    # 发送组合键序列
                                    win32gui.PostMessage(
                                        target_hwnd, win32con.WM_KEYDOWN, vk_code, 0
                                    )
                                    win32gui.PostMessage(
                                        target_hwnd, win32con.WM_KEYUP, vk_code, 0
                                    )

                                # 对于这些特殊组合键，直接处理完毕
                                continue

                            # 处理普通按键
                            if event.name in [
                                "enter",
                                "backspace",
                                "tab",
                                "esc",
                                "space",
                                "up",
                                "down",
                                "left",
                                "right",
                                "home",
                                "end",
                                "page up",
                                "page down",
                                "delete",
                                "f1",
                                "f2",
                                "f3",
                                "f4",
                                "f5",
                                "f6",
                                "f7",
                                "f8",
                                "f9",
                                "f10",
                                "f11",
                                "f12",
                            ]:
                                vk_map = {
                                    "enter": win32con.VK_RETURN,
                                    "backspace": win32con.VK_BACK,
                                    "tab": win32con.VK_TAB,
                                    "esc": win32con.VK_ESCAPE,
                                    "space": win32con.VK_SPACE,
                                    "up": win32con.VK_UP,
                                    "down": win32con.VK_DOWN,
                                    "left": win32con.VK_LEFT,
                                    "right": win32con.VK_RIGHT,
                                    "home": win32con.VK_HOME,
                                    "end": win32con.VK_END,
                                    "page up": win32con.VK_PRIOR,
                                    "page down": win32con.VK_NEXT,
                                    "delete": win32con.VK_DELETE,
                                    "f1": win32con.VK_F1,
                                    "f2": win32con.VK_F2,
                                    "f3": win32con.VK_F3,
                                    "f4": win32con.VK_F4,
                                    "f5": win32con.VK_F5,
                                    "f6": win32con.VK_F6,
                                    "f7": win32con.VK_F7,
                                    "f8": win32con.VK_F8,
                                    "f9": win32con.VK_F9,
                                    "f10": win32con.VK_F10,
                                    "f11": win32con.VK_F11,
                                    "f12": win32con.VK_F12,
                                }
                                vk_code = vk_map[event.name]

                                # 发送按键消息
                                if event.event_type == keyboard.KEY_DOWN:
                                    win32gui.PostMessage(
                                        target_hwnd, win32con.WM_KEYDOWN, vk_code, 0
                                    )
                                else:
                                    win32gui.PostMessage(
                                        target_hwnd, win32con.WM_KEYUP, vk_code, 0
                                    )
                            else:
                                # 处理普通字符
                                if len(event.name) == 1:
                                    vk_code = win32api.VkKeyScan(event.name[0]) & 0xFF
                                    if event.event_type == keyboard.KEY_DOWN:
                                        # 直接发送字符消息，更有效
                                        win32gui.PostMessage(
                                            target_hwnd,
                                            win32con.WM_CHAR,
                                            ord(event.name[0]),
                                            0,
                                        )
                                    continue
                                else:
                                    continue

                            # 释放修饰键 - 仅在按键弹起时释放
                            if event.event_type == keyboard.KEY_UP:
                                for mod_name, mod_info in modifier_keys.items():
                                    if mod_info["pressed"]:
                                        win32gui.PostMessage(
                                            target_hwnd,
                                            win32con.WM_KEYUP,
                                            mod_info["vk"],
                                            0,
                                        )

                        except Exception as e:
                            # 限制错误日志输出频率
                            if (
                                not hasattr(self, "last_key_error_time")
                                or time.time() - self.last_key_error_time > 5
                            ):
                                print(f"同步键盘事件到窗口 {hwnd} 失败: {str(e)}")
                                self.last_key_error_time = time.time()

        except Exception as e:
            # 限制错误日志输出频率
            if (
                not hasattr(self, "last_keyboard_error_time")
                or time.time() - self.last_keyboard_error_time > 5
            ):
                print(f"处理键盘事件失败: {str(e)}")
                self.last_keyboard_error_time = time.time()

    def stop_sync(self):
        """停止同步 - 优化版，确保资源清理"""
        try:
            # 标记同步状态为False
            self.is_sync = False

            # 卸载低级滚轮钩子
            self.unhook_wheel()

            # 保存当前快捷键设置，用于后续恢复
            current_shortcut = None
            if hasattr(self, "current_shortcut"):
                current_shortcut = self.current_shortcut

            # 保存当前快捷键钩子
            shortcut_hook = None
            if hasattr(self, "shortcut_hook"):
                shortcut_hook = self.shortcut_hook
                self.shortcut_hook = None  # 临时清除引用，避免被unhook_all移除

            # 移除同步相关的键盘钩子，但保留快捷键钩子
            try:
                # 不使用 keyboard.unhook_all()，而是有选择地移除
                # 暂时没有更好的方法来区分钩子，所以先重置然后恢复快捷键
                keyboard.unhook_all()
                print("已移除同步相关的键盘钩子")
            except Exception as e:
                print(f"移除键盘钩子失败: {str(e)}")

            # 移除鼠标钩子
            try:
                mouse.unhook_all()
                print("已移除鼠标钩子")
            except Exception as e:
                print(f"移除鼠标钩子失败: {str(e)}")

            # 等待线程结束
            if hasattr(self, "hook_thread") and self.hook_thread:
                try:
                    if self.hook_thread.is_alive():
                        self.hook_thread.join(timeout=0.5)
                except Exception as e:
                    print(f"等待消息循环线程结束失败: {str(e)}")
                self.hook_thread = None

            # 等待弹出窗口监控线程结束
            if hasattr(self, "popup_monitor_thread") and self.popup_monitor_thread:
                try:
                    if self.popup_monitor_thread.is_alive():
                        self.popup_monitor_thread.join(timeout=0.5)
                except Exception as e:
                    print(f"等待弹出窗口监控线程结束失败: {str(e)}")
                self.popup_monitor_thread = None

            # 重置关键数据结构
            self.popup_windows = []
            self.sync_popups = {}
            self.sync_windows = []

            # 更新按钮状态 - 需要检查按钮是否存在
            if hasattr(self, "sync_button") and self.sync_button:
                try:
                    self.sync_button.configure(
                        text="▶ 开始同步", style="Accent.TButton"
                    )
                except Exception as e:
                    print(f"更新按钮状态失败: {str(e)}")

            # 重新设置快捷键 - 确保快捷键在停止同步后仍然有效
            if current_shortcut:
                try:
                    self.set_shortcut(current_shortcut)
                    print(f"已恢复快捷键设置: {current_shortcut}")
                except Exception as e:
                    print(f"恢复快捷键失败: {str(e)}")

            # 提示用户
            print("同步已停止")

        except Exception as e:
            print(f"停止同步出错: {str(e)}")
            traceback.print_exc()
            # 确保按钮恢复正常状态
            try:
                if hasattr(self, "sync_button") and self.sync_button:
                    self.sync_button.configure(
                        text="▶ 开始同步", style="Accent.TButton"
                    )
            except:
                pass

    def on_closing(self):
        # 窗口关闭事件
        try:
            # 停止同步
            if hasattr(self, "is_sync") and self.is_sync:
                self.stop_sync()

            # 保存设置
            self.save_settings()
            # 保存窗口位置
            self.save_window_position()

            # 移除系统托盘图标
            if not self.is_win11 and hasattr(self, "notify_id"):
                try:
                    win32gui.Shell_NotifyIcon(win32gui.NIM_DELETE, self.notify_id)
                    print("已移除系统托盘图标")
                except Exception as e:
                    print(f"移除系统托盘图标失败: {str(e)}")

            # 关闭所有Chrome窗口
            if hasattr(self, "close_all_windows") and messagebox.askyesno(
                "确认", "关闭所有Chrome窗口?"
            ):
                self.close_all_windows()

            # 销毁主窗口
            self.root.destroy()

        except Exception as e:
            print(f"关闭程序时出错: {str(e)}")
            self.root.destroy()

    def auto_arrange_windows(self):
        # 自动排列窗口
        try:
            print("开始自动排列窗口...")
            # 先停止同步
            was_syncing = self.is_syncing
            if was_syncing:
                self.stop_sync()

            # 获取选中的窗口并按编号排序
            selected = []
            for item in self.window_list.get_children():
                if self.window_list.set(item, "select") == "√":
                    values = self.window_list.item(item)["values"]
                    if values and len(values) >= 5:
                        number = int(values[1])
                        hwnd = int(values[4])
                        selected.append((number, hwnd, item))

            if not selected:
                messagebox.showinfo("提示", "请先选择要排列的窗口！")
                return

            print(f"选中了 {len(selected)} 个窗口")

            # 按编号升序排序
            selected.sort(key=lambda x: x[0])
            print("窗口排序结果:")
            for num, hwnd, _ in selected:
                print(f"编号: {num}, 句柄: {hwnd}")

            # 获取选中的屏幕信息
            screen_selection = self.screen_selection
            print(f"当前选择的屏幕: {screen_selection}")

            # 更新屏幕列表
            screen_names = self.update_screen_list()

            # 找到选中的屏幕索引
            screen_index = 0  # 默认使用第一个屏幕
            for i, name in enumerate(screen_names):
                if name == screen_selection:
                    screen_index = i
                    break

            if screen_index >= len(self.screens):
                messagebox.showerror("错误", "请选择有效的屏幕！")
                return

            # 获取屏幕尺寸
            screen = self.screens[screen_index]
            screen_rect = screen["work_rect"]  # 使用工作区而不是完整显示区
            print(f"屏幕工作区: {screen_rect}")

            # 计算屏幕尺寸
            screen_width = screen_rect[2] - screen_rect[0]
            screen_height = screen_rect[3] - screen_rect[1]
            print(f"屏幕尺寸: {screen_width}x{screen_height}")

            # 计算最佳布局
            count = len(selected)
            cols = int(math.sqrt(count))
            if cols * cols < count:
                cols += 1
            rows = (count + cols - 1) // cols

            # 计算窗口大小
            width = screen_width // cols
            height = screen_height // rows
            print(f"窗口布局: {rows}行 x {cols}列, 窗口大小: {width}x{height}")

            # 创建位置映射（从左到右，从上到下）
            positions = []
            for i in range(count):
                row = i // cols
                col = i % cols
                x = screen_rect[0] + col * width
                y = screen_rect[1] + row * height
                positions.append((x, y))
                print(f"位置 {i}: ({x}, {y})")

            # 应用窗口位置
            for i, (number, hwnd, _) in enumerate(selected):
                try:
                    x, y = positions[i]
                    print(f"移动窗口 {number} (句柄: {hwnd}) 到位置 ({x}, {y})")

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
                    print(f"窗口 {number} 移动成功")

                except Exception as e:
                    print(f"移动窗口 {number} (句柄: {hwnd}) 失败: {str(e)}")
                    continue

            print("窗口排列完成")

            # 添加：将所有排列的窗口置顶
            for _, hwnd, _ in selected:
                try:
                    # 设置窗口为置顶
                    win32gui.SetWindowPos(
                        hwnd,
                        win32con.HWND_TOPMOST,
                        0,
                        0,
                        0,
                        0,
                        win32con.SWP_NOMOVE | win32con.SWP_NOSIZE,
                    )
                    # 取消置顶（但保持在所有窗口前面）
                    win32gui.SetWindowPos(
                        hwnd,
                        win32con.HWND_NOTOPMOST,
                        0,
                        0,
                        0,
                        0,
                        win32con.SWP_NOMOVE | win32con.SWP_NOSIZE,
                    )
                except Exception as e:
                    print(f"设置窗口 {hwnd} 置顶失败: {str(e)}")

            # 找到主窗口并激活
            master_hwnd = None
            for item in self.window_list.get_children():
                if self.window_list.set(item, "master") == "√":
                    values = self.window_list.item(item)["values"]
                    if values and len(values) >= 5:
                        master_hwnd = int(values[4])
                        break

            # 如果找到主窗口，将其设为活动窗口
            if master_hwnd:
                try:
                    # 确保窗口可见
                    win32gui.ShowWindow(master_hwnd, win32con.SW_RESTORE)
                    # 设置为前台窗口
                    win32gui.SetForegroundWindow(master_hwnd)
                    print(f"已激活主窗口: {master_hwnd}")
                except Exception as e:
                    print(f"激活主窗口失败: {str(e)}")

            # 如果之前在同步，重新开启同步
            if was_syncing:
                self.start_sync([item for _, _, item in selected])

        except Exception as e:
            print(f"自动排列失败: {str(e)}")
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
                    values = self.window_list.item(item)["values"]
                    if values and len(values) >= 5:
                        hwnd = int(values[4])
                        selected.append((item, hwnd))

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
                for i, (item, hwnd) in enumerate(selected):
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

            # 添加：将所有排列的窗口置顶
            for _, hwnd in selected:
                try:
                    # 设置窗口为置顶
                    win32gui.SetWindowPos(
                        hwnd,
                        win32con.HWND_TOPMOST,
                        0,
                        0,
                        0,
                        0,
                        win32con.SWP_NOMOVE | win32con.SWP_NOSIZE,
                    )
                    # 取消置顶（但保持在所有窗口前面）
                    win32gui.SetWindowPos(
                        hwnd,
                        win32con.HWND_NOTOPMOST,
                        0,
                        0,
                        0,
                        0,
                        win32con.SWP_NOMOVE | win32con.SWP_NOSIZE,
                    )
                except Exception as e:
                    print(f"设置窗口 {hwnd} 置顶失败: {str(e)}")

            # 找到主窗口并激活
            master_hwnd = None
            for item in self.window_list.get_children():
                if self.window_list.set(item, "master") == "√":
                    values = self.window_list.item(item)["values"]
                    if values and len(values) >= 5:
                        master_hwnd = int(values[4])
                        break

            # 如果找到主窗口，将其设为活动窗口
            if master_hwnd:
                try:
                    # 确保窗口可见
                    win32gui.ShowWindow(master_hwnd, win32con.SW_RESTORE)
                    # 设置为前台窗口
                    win32gui.SetForegroundWindow(master_hwnd)
                    print(f"已激活主窗口: {master_hwnd}")
                except Exception as e:
                    print(f"激活主窗口失败: {str(e)}")

            # 添加：如果之前在同步，重新开启同步
            if was_syncing:
                self.start_sync([item for item, _ in selected])

        except Exception as e:
            messagebox.showerror("错误", f"排列窗口失败: {str(e)}")

    def load_settings(self) -> dict:
        # 加载设置
        settings = {}
        try:
            if os.path.exists("settings.json"):
                with open("settings.json", "r", encoding="utf-8") as f:
                    settings = json.load(f)

                # 加载是否显示Chrome提示的设置
                if "show_chrome_tip" in settings:
                    self.show_chrome_tip = settings["show_chrome_tip"]
        except Exception as e:
            print(f"加载设置失败: {str(e)}")

        return settings

    def save_settings(self):
        # 保存设置
        try:
            # 确保信息是最新的
            self.settings["shortcut_path"] = self.shortcut_path
            self.settings["cache_dir"] = self.cache_dir
            self.settings["icon_dir"] = self.icon_dir
            if hasattr(self, "current_shortcut") and self.current_shortcut:
                self.settings["sync_shortcut"] = self.current_shortcut
            if hasattr(self, "screen_selection"):
                self.settings["screen_selection"] = self.screen_selection

            # 保存是否显示Chrome提示的设置
            self.settings["show_chrome_tip"] = self.show_chrome_tip

            # 保存排列参数
            self.settings.update(self.get_arrange_params())

            with open("settings.json", "w", encoding="utf-8") as f:
                json.dump(self.settings, f, ensure_ascii=False, indent=4)
            print(f"保存设置成功，包括 show_chrome_tip = {self.show_chrome_tip}")
        except Exception as e:
            print(f"保存设置失败: {str(e)}")

    def get_arrange_params(self):
        return {
            "start_x": self.start_x.get(),
            "start_y": self.start_y.get(),
            "window_width": self.window_width.get(),
            "window_height": self.window_height.get(),
            "h_spacing": self.h_spacing.get(),
            "v_spacing": self.v_spacing.get(),
            "windows_per_row": self.windows_per_row.get(),
        }

    def load_arrange_params(self):
        # 加载排列参数
        settings = self.load_settings()
        if "arrange_params" in settings:
            params = settings["arrange_params"]
            self.start_x.delete(0, tk.END)
            self.start_x.insert(0, params.get("start_x", "0"))
            self.start_y.delete(0, tk.END)
            self.start_y.insert(0, params.get("start_y", "0"))
            self.window_width.delete(0, tk.END)
            self.window_width.insert(0, params.get("window_width", "500"))
            self.window_height.delete(0, tk.END)
            self.window_height.insert(0, params.get("window_height", "400"))
            self.h_spacing.delete(0, tk.END)
            self.h_spacing.insert(0, params.get("h_spacing", "0"))
            self.v_spacing.delete(0, tk.END)
            self.v_spacing.insert(0, params.get("v_spacing", "0"))
            self.windows_per_row.delete(0, tk.END)
            self.windows_per_row.insert(0, params.get("windows_per_row", "5"))

    def parse_window_numbers(self, numbers_str: str) -> List[int]:
        # 解析窗口编号字符串
        if not numbers_str.strip():
            return list(range(1, 49))  # 如果为空，返回所有编号

        result = []
        # 分割逗号分隔的部分
        parts = numbers_str.split(",")
        for part in parts:
            part = part.strip()
            if "-" in part:
                # 处理范围，如 "1-5"
                start, end = map(int, part.split("-"))
                result.extend(range(start, end + 1))
            else:
                # 处理单个数字
                result.append(int(part))
        return sorted(list(set(result)))  # 去重并排序

    def open_windows(self):
        """打开Chrome窗口，依次打开但速度更快"""
        # 获取快捷方式目录
        shortcut_dir = self.shortcut_path

        if not shortcut_dir:
            messagebox.showinfo("提示", "请先在设置中设置快捷方式目录！")
            return

        if not os.path.exists(shortcut_dir):
            messagebox.showerror("错误", "快捷方式目录不存在！")
            return

        # 获取用户设置的路径
        abs_path = os.path.abspath(os.path.normpath(shortcut_dir))
        if not os.path.isdir(abs_path):
            messagebox.showerror("路径错误", "指定的路径不是一个有效目录")
            return

        # 快速验证路径可访问性
        if not os.access(abs_path, os.R_OK):
            messagebox.showerror("权限不足", "程序没有该目录的读取权限")
            return

        # 打开窗口逻辑
        numbers = self.numbers_entry.get()

        if not numbers:
            messagebox.showwarning("警告", "请输入窗口编号！")
            return

        try:
            window_numbers = self.parse_window_numbers(numbers)

            # 清空现有调试端口映射
            self.debug_ports.clear()

            # 临时文件列表，用于最后清理
            temp_files = []

            for num in window_numbers:
                shortcut = os.path.join(abs_path, f"{num}.lnk")
                if not os.path.exists(shortcut):
                    print(f"警告: 快捷方式不存在: {shortcut}")
                    continue

                # 如果启用了CDP，添加远程调试参数
                if self.enable_cdp:
                    # 获取快捷方式信息
                    shortcut_obj = self.shell.CreateShortCut(shortcut)
                    target = shortcut_obj.TargetPath
                    args = shortcut_obj.Arguments
                    working_dir = shortcut_obj.WorkingDirectory

                    # 为每个窗口分配一个唯一的调试端口
                    debug_port = 9222 + int(num)

                    # 将窗口号和调试端口的映射保存到字典中
                    self.debug_ports[num] = debug_port

                    # 设置调试端口参数
                    if "--remote-debugging-port=" in args:
                        # 替换已有的调试端口参数
                        new_args = re.sub(
                            r"--remote-debugging-port=\d+",
                            f"--remote-debugging-port={debug_port}",
                            args,
                        )
                    else:
                        # 添加新的调试端口参数
                        new_args = f"{args} --remote-debugging-port={debug_port}"

                    # 创建临时快捷方式
                    temp_shortcut = os.path.join(abs_path, f"temp_{num}.lnk")
                    temp_obj = self.shell.CreateShortCut(temp_shortcut)
                    temp_obj.TargetPath = target
                    temp_obj.Arguments = new_args
                    temp_obj.WorkingDirectory = working_dir
                    temp_obj.IconLocation = shortcut_obj.IconLocation
                    temp_obj.Save()

                    # 记录临时文件
                    temp_files.append(temp_shortcut)

                    # 启动临时快捷方式
                    print(f"启动窗口 {num}，调试端口: {debug_port}")
                    subprocess.Popen(["start", "", temp_shortcut], shell=True)

                    # 只等待极短时间，让进程开始启动
                    time.sleep(0.05)  # 只等待50毫秒
                else:
                    # 不启用CDP，直接打开
                    subprocess.Popen(["start", "", shortcut], shell=True)
                    time.sleep(0.05)  # 只等待50毫秒

            # 在所有窗口启动后，在后台清理临时文件
            def cleanup_temp_files():
                # 等待一小段时间再清理，确保所有窗口都已经启动
                time.sleep(1)
                for temp_file in temp_files:
                    try:
                        if os.path.exists(temp_file):
                            os.remove(temp_file)
                    except:
                        pass  # 忽略删除失败

            # 启动清理线程，不阻塞主线程
            cleanup_thread = threading.Thread(target=cleanup_temp_files)
            cleanup_thread.daemon = True  # 设为守护线程，程序退出时自动结束
            cleanup_thread.start()

            # 调试输出当前所有的端口映射，方便排查
            print("窗口号到调试端口的映射:")
            for window_num, port in self.debug_ports.items():
                print(f"窗口 {window_num} -> 端口 {port}")

            # 保存当前使用的窗口编号到设置
            try:
                # 重新加载设置，确保获取最新的设置
                settings = self.load_settings()
                settings["last_window_numbers"] = numbers
                self.settings = settings  # 更新当前实例中的设置

                # 保存设置到文件
                with open("settings.json", "w", encoding="utf-8") as f:
                    json.dump(settings, f, ensure_ascii=False, indent=4)
                print(f"成功保存窗口编号: {numbers}")
            except Exception as e:
                print(f"保存窗口编号设置失败: {str(e)}")

        except Exception as e:
            messagebox.showerror("错误", f"打开窗口失败: {str(e)}")

    def get_shortcut_number(self, shortcut_path):
        # 从快捷方式文件名中获取窗口编号
        try:
            # 首先从快捷方式文件名提取编号
            # 例如 "D:/chrome duo/1.lnk" -> "1"
            file_name = os.path.basename(shortcut_path)
            name_without_ext = os.path.splitext(file_name)[0]
            if name_without_ext.isdigit():
                return name_without_ext

            # 如果文件名不是纯数字，则尝试从参数中提取数据目录
            shortcut = self.shell.CreateShortCut(shortcut_path)
            cmd_line = shortcut.Arguments

            if "--user-data-dir=" in cmd_line:
                data_dir = cmd_line.split("--user-data-dir=")[1].strip('"')
                # 注意：这里不再假设数据目录名就是数字
                # 但为了向后兼容性，我们仍然检查是否为数字
                base_name = os.path.basename(data_dir)
                if base_name.isdigit():
                    return base_name

            return None

        except Exception as e:
            print(f"获取快捷方式编号失败: {str(e)}")
            return None

    def import_windows(self):
        """导入当前打开的Chrome窗口，并显示进度对话框"""
        try:
            print("开始导入窗口...")
            # 清空列表
            for item in self.window_list.get_children():
                self.window_list.delete(item)

            # 创建进度对话框
            progress_dialog = tk.Toplevel(self.root)
            progress_dialog.title("加载窗口")  # 修改标题
            progress_dialog.geometry("300x120")  # 减小高度，因为不需要显示那么多文字
            progress_dialog.resizable(False, False)
            progress_dialog.transient(self.root)  # 设置为主窗口的临时窗口
            progress_dialog.grab_set()  # 模态对话框

            # 设置图标
            try:
                if os.path.exists("app.ico"):
                    progress_dialog.iconbitmap("app.ico")
            except Exception as e:
                print(f"设置图标失败: {str(e)}")

            # 保持对话框在顶层
            progress_dialog.attributes("-topmost", True)

            # 居中对话框
            self.center_window(progress_dialog)

            # 添加进度标签 - 只保留一个简单的说明
            progress_label = ttk.Label(
                progress_dialog, text="正在加载窗口...", font=("微软雅黑", 10)
            )
            progress_label.pack(pady=(15, 10))

            # 不再显示状态标签 (删除status_label)

            # 添加进度条
            progress_bar = ttk.Progressbar(
                progress_dialog, mode="indeterminate", length=250
            )
            progress_bar.pack(pady=10)
            progress_bar.start(10)  # 开始动画

            # 添加取消按钮
            cancel_btn = ttk.Button(
                progress_dialog, text="取消", command=progress_dialog.destroy
            )
            cancel_btn.pack(pady=5)

            # 在后台线程中进行窗口导入操作
            import_thread_active = [True]  # 使用列表作为可变引用

            def import_thread():
                try:
                    # 初始化COM环境，必须在线程中使用WMI之前调用
                    pythoncom.CoInitialize()

                    windows = []
                    hwnd_map = {}

                    # 使用WMI搜索Chrome进程
                    def search_chrome_processes():
                        c = wmi.WMI()
                        chrome_processes = []
                        # 不再更新进度文字

                        for process in c.Win32_Process():
                            if not import_thread_active[0]:
                                return []  # 如果取消了，立即返回

                            # 检查ExecutablePath是否为None
                            if (
                                process.ExecutablePath is not None
                                and "chrome.exe" in process.ExecutablePath.lower()
                            ):
                                cmd_line = process.CommandLine
                                if cmd_line and "--user-data-dir=" in cmd_line:
                                    chrome_processes.append(process)

                        return chrome_processes

                    # 获取Chrome进程
                    chrome_processes = search_chrome_processes()
                    total_processes = len(chrome_processes)

                    if not import_thread_active[0]:
                        return  # 如果已取消，不继续处理

                    # 不再更新进度文字

                    # 处理每个Chrome进程
                    for index, process in enumerate(chrome_processes):
                        if not import_thread_active[0]:
                            return  # 如果已取消，不继续处理

                        try:
                            pid = process.ProcessId
                            cmd_line = process.CommandLine

                            # 不再更新进度文字

                            if "--user-data-dir=" in cmd_line:
                                # 先检查这个进程是否有可见的Chrome窗口
                                def find_window_for_process(pid):
                                    result = []

                                    def enum_callback(hwnd, process_windows):
                                        if win32gui.IsWindowVisible(hwnd):
                                            _, win_pid = (
                                                win32process.GetWindowThreadProcessId(
                                                    hwnd
                                                )
                                            )
                                            if win_pid == pid:
                                                title = win32gui.GetWindowText(hwnd)
                                                if title and not title.startswith(
                                                    "Chrome 传递"
                                                ):
                                                    process_windows.append(hwnd)

                                    process_windows = []
                                    win32gui.EnumWindows(enum_callback, process_windows)
                                    return process_windows

                                # 获取该进程的窗口列表
                                chrome_windows = find_window_for_process(pid)

                                # 如果没有可见窗口，跳过这个进程
                                # 这有助于避免处理后台或扩展进程
                                if not chrome_windows:
                                    continue

                                # 从命令行中提取用户数据目录路径
                                data_dir = re.search(
                                    r'--user-data-dir="?([^"]+)"?', cmd_line
                                )
                                if data_dir:
                                    data_path = data_dir.group(1)

                                    # 尝试找到对应的快捷方式和编号
                                    window_num = None

                                    # 1. 首先尝试从快捷方式目录查找与此用户数据目录匹配的快捷方式
                                    shortcut_dir = self.shortcut_path
                                    if shortcut_dir and os.path.exists(shortcut_dir):
                                        for shortcut_file in os.listdir(shortcut_dir):
                                            if shortcut_file.endswith(".lnk"):
                                                shortcut_path = os.path.join(
                                                    shortcut_dir, shortcut_file
                                                )
                                                try:
                                                    shortcut_obj = (
                                                        self.shell.CreateShortCut(
                                                            shortcut_path
                                                        )
                                                    )
                                                    shortcut_args = (
                                                        shortcut_obj.Arguments
                                                    )

                                                    # 检查是否为同一数据目录
                                                    if (
                                                        "--user-data-dir="
                                                        in shortcut_args
                                                    ):
                                                        shortcut_data_dir = re.search(
                                                            r'--user-data-dir="?([^"]+)"?',
                                                            shortcut_args,
                                                        )
                                                        if (
                                                            shortcut_data_dir
                                                            and self.normalize_path(
                                                                shortcut_data_dir.group(
                                                                    1
                                                                )
                                                            )
                                                            == self.normalize_path(
                                                                data_path
                                                            )
                                                        ):
                                                            # 找到匹配的快捷方式，从文件名提取编号
                                                            shortcut_name = (
                                                                os.path.splitext(
                                                                    shortcut_file
                                                                )[0]
                                                            )
                                                            if shortcut_name.isdigit():
                                                                window_num = int(
                                                                    shortcut_name
                                                                )
                                                                break
                                                except Exception as e:
                                                    print(f"读取快捷方式失败: {str(e)}")

                                    # 2. 如果未找到匹配的快捷方式，则尝试从数据目录名称中提取（向后兼容）
                                    if window_num is None:
                                        try:
                                            base_name = os.path.basename(data_path)
                                            if base_name.isdigit():
                                                window_num = int(base_name)
                                        except:
                                            pass

                                    # 3. 如果仍未找到编号，则创建一个临时编号
                                    if window_num is None:
                                        # 生成一个大于1001的临时编号，避免与用户自定义编号冲突
                                        window_num = 1001 + len(windows)
                                        print(
                                            f"未能确定窗口编号，使用临时编号: {window_num}，用户数据目录: {data_path}"
                                        )

                                    # 注意：这里不再需要重复查找窗口，因为我们已经在前面找到了窗口
                                    # 使用第一个窗口
                                    hwnd = chrome_windows[0]
                                    title = win32gui.GetWindowText(hwnd)
                                    windows.append(
                                        {
                                            "hwnd": hwnd,
                                            "title": title,
                                            "number": window_num,
                                        }
                                    )
                                    hwnd_map[window_num] = hwnd
                                    print(f"添加窗口: 编号={window_num}, 标题={title}")
                        except:
                            continue

                    # 按窗口编号排序（升序）
                    windows.sort(key=lambda w: w["number"])

                    # 导入完成，更新UI
                    def update_ui():
                        if not import_thread_active[0]:
                            return  # 如果已取消，不更新UI

                        # 填充列表
                        for window in windows:
                            self.window_list.insert(
                                "",
                                "end",
                                values=(
                                    "",
                                    f"{window['number']}",
                                    window["title"],
                                    "",
                                    window["hwnd"],
                                ),
                            )

                        # 更新端口映射
                        self.debug_ports = {
                            w["number"]: 9222 + w["number"] for w in windows
                        }

                        # 关闭进度对话框 - 不显示完成文字，直接变进度条状态
                        progress_bar.stop()
                        progress_bar.config(mode="determinate", value=100)

                        # 0.3秒后关闭对话框 - 减少等待时间，但还是给用户一点完成的视觉反馈
                        progress_dialog.after(300, progress_dialog.destroy)

                        # 显示导入结果 - 只在没有找到窗口时显示提示
                        if not windows:
                            # 延迟显示消息框，确保进度对话框已关闭
                            self.root.after(
                                400,
                                lambda: messagebox.showinfo(
                                    "导入结果", "未找到任何Chrome窗口"
                                ),
                            )
                        else:
                            # 只在控制台打印结果，不再向用户显示
                            print(f"成功导入 {len(windows)} 个窗口")

                    # 在主线程中更新UI
                    if import_thread_active[0]:
                        progress_dialog.after(0, update_ui)

                    # 自动为导入的窗口设置图标
                    self.apply_icons_to_chrome_windows(hwnd_map)

                except Exception as import_error:
                    # 修复变量作用域问题 - 将异常保存到局部变量
                    error_message = str(import_error)
                    print(f"导入窗口线程内部错误: {error_message}")

                    # 在主线程中关闭对话框并显示错误
                    def show_error_message():
                        if progress_dialog.winfo_exists():
                            progress_dialog.destroy()
                        messagebox.showerror("错误", f"导入窗口失败: {error_message}")

                    progress_dialog.after(0, show_error_message)

                finally:
                    # 清理COM环境
                    try:
                        pythoncom.CoUninitialize()
                    except:
                        pass

            # 取消按钮的事件处理
            def on_cancel():
                import_thread_active[0] = False
                progress_dialog.destroy()

            cancel_btn.config(command=on_cancel)

            # 启动导入线程
            threading.Thread(target=import_thread, daemon=True).start()

        except Exception as e:
            print(f"导入窗口失败: {str(e)}")
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
                    handle = win32api.OpenProcess(
                        win32con.PROCESS_QUERY_INFORMATION | win32con.PROCESS_VM_READ,
                        False,
                        pid,
                    )
                    if handle:
                        cmd_line = win32process.GetModuleFileNameEx(handle, 0)
                        win32api.CloseHandle(handle)

                        # 从路径中提取编号
                        if "\\Data\\" in cmd_line:
                            number = int(cmd_line.split("\\Data\\")[-1].split("\\")[0])
                except:
                    pass

                if number is not None:
                    windows.append({"hwnd": hwnd, "title": title, "number": number})

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
                hwnd = int(self.window_list.item(item)["values"][4])
                try:
                    # 检查窗口是否还存在
                    if win32gui.IsWindow(hwnd):
                        win32gui.PostMessage(hwnd, win32con.WM_CLOSE, 0, 0)
                except:
                    pass  # 忽略已关闭窗口的错误

            # 移除自动导入，改为手动从列表中删除项目
            for item in selected:
                self.window_list.delete(item)

            # 重置全选按钮状态为"全部选择"
            self.select_all_var.set("全部选择")

            # 显示Chrome后台运行提示（如果启用）
            if self.show_chrome_tip:
                self.show_chrome_settings_tip()

        except Exception as e:
            print(f"关闭窗口失败: {str(e)}")  # 只打印错误，不显示错误对话框

    def set_taskbar_icons(self):
        # 设置独立任务栏图标
        # 从设置中获取目录信息
        settings = self.load_settings()
        shortcut_dir = self.shortcut_path
        icon_dir = settings.get("icon_dir", "")

        if not shortcut_dir:
            messagebox.showinfo("提示", "请先在设置中设置快捷方式目录！")
            return

        if not os.path.exists(shortcut_dir):
            messagebox.showerror("错误", "快捷方式目录不存在！")
            return

        if not icon_dir:
            messagebox.showinfo("提示", "请先在设置中设置图标目录！")
            return

        if not os.path.exists(icon_dir):
            messagebox.showerror("错误", "图标目录不存在！")
            return

        # 确认操作
        choice = messagebox.askyesnocancel(
            "选择操作",
            "选择要执行的操作：\n是 - 设置自定义图标\n否 - 恢复原始设置\n取消 - 不执行任何操作",
        )
        if choice is None:  # 用户点击取消
            return

        try:
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
                    shortcut_path = os.path.join(shortcut_dir, f"{i}.lnk")
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

                messagebox.showinfo(
                    "成功", f"已成功修改 {modified_count} 个快捷方式的图标！"
                )
            else:  # 恢复原始设置
                chrome_path = r"C:\Program Files\Google\Chrome\Application\chrome.exe"
                if not os.path.exists(chrome_path):
                    chrome_path = (
                        r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe"
                    )

                # 获取Chrome数据目录
                chrome_data_dir = settings.get("cache_dir", "D:\\chrom duo\\Data")

                # 恢复指定的快捷方式
                for i in window_numbers:
                    shortcut_path = os.path.join(shortcut_dir, f"{i}.lnk")
                    if not os.path.exists(shortcut_path):
                        continue

                    # 修改快捷方式
                    shortcut = shell.CreateShortCut(shortcut_path)

                    # 恢复默认图标
                    shortcut.IconLocation = f"{chrome_path},0"

                    # 恢复原始启动参数
                    original_args = f'--user-data-dir="{chrome_data_dir}\\{i}"'
                    shortcut.TargetPath = chrome_path
                    shortcut.Arguments = original_args

                    # 保存修改
                    shortcut.save()
                    modified_count += 1

                messagebox.showinfo(
                    "成功", f"已成功恢复 {modified_count} 个快捷方式的原始设置！"
                )

        except Exception as e:
            messagebox.showerror("错误", f"操作失败: {str(e)}")

    def batch_open_urls(self):
        """批量打开网页，使用直接的命令行方式打开URL"""
        try:
            # 获取输入的网址
            url = self.url_entry.get()
            if not url:
                messagebox.showwarning("警告", "请输入要打开的网址！")
                return

            # 确保 URL 格式正确
            if not url.startswith(("http://", "https://")):
                url = "https://" + url

            # 获取选中的窗口
            selected_windows = []
            for item in self.window_list.get_children():
                if self.window_list.set(item, "select") == "√":
                    try:
                        # 获取窗口编号和标题
                        # values列表: ["", "编号", "标题", "", hwnd]
                        window_values = self.window_list.item(item)["values"]
                        window_num = int(window_values[1])  # 获取窗口编号
                        window_title = (
                            str(window_values[2]) if len(window_values) > 2 else ""
                        )
                        hwnd = int(window_values[-1]) if len(window_values) > 4 else 0

                        # 调试输出
                        print(
                            f"选择了窗口: {window_title} (编号: {window_num}, 句柄: {hwnd})"
                        )
                        selected_windows.append(window_num)
                    except (ValueError, IndexError) as e:
                        print(f"解析窗口信息出错: {str(e)}")
                        # 忽略无法识别编号的窗口

            if not selected_windows:
                messagebox.showwarning("警告", "请先选择要操作的窗口！")
                return

            # 调试输出
            print(f"选择的窗口编号: {selected_windows}")

            # 验证快捷方式目录是否存在
            shortcut_dir = self.shortcut_path
            if not shortcut_dir or not os.path.exists(shortcut_dir):
                messagebox.showerror("错误", "快捷方式目录不存在，请在设置中配置！")
                return

            # 查找Chrome路径
            chrome_path = self.find_chrome_path()
            if not chrome_path:
                messagebox.showerror("错误", "未找到Chrome安装路径！")
                return

            # 创建WScript.Shell对象（如果尚未创建）
            if not hasattr(self, "shell") or self.shell is None:
                self.shell = win32com.client.Dispatch("WScript.Shell")

            # 为每个选中的窗口直接启动Chrome并打开指定URL
            success_count = 0
            for window_num in selected_windows:
                try:
                    # 通过快捷方式获取用户数据目录路径
                    shortcut_path = os.path.join(shortcut_dir, f"{window_num}.lnk")

                    # 检查快捷方式是否存在
                    if not os.path.exists(shortcut_path):
                        print(
                            f"警告: 窗口 {window_num} 的快捷方式不存在: {shortcut_path}"
                        )
                        continue

                    # 从快捷方式中获取用户数据目录
                    try:
                        shortcut_obj = self.shell.CreateShortCut(shortcut_path)
                        cmd_line = shortcut_obj.Arguments

                        # 提取user-data-dir参数
                        if "--user-data-dir=" in cmd_line:
                            user_data_dir = re.search(
                                r'--user-data-dir="?([^"]+)"?', cmd_line
                            )
                            if user_data_dir:
                                user_data_dir = user_data_dir.group(1)
                            else:
                                print(
                                    f"警告: 无法从快捷方式提取用户数据目录: {shortcut_path}"
                                )
                                continue
                        else:
                            # 尝试使用旧的方式（向后兼容）
                            user_data_dir = os.path.join(
                                self.cache_dir, str(window_num)
                            )
                            if not os.path.exists(user_data_dir):
                                print(
                                    f"警告: 窗口 {window_num} 的用户数据目录不存在: {user_data_dir}"
                                )
                                continue
                    except Exception as e:
                        print(f"警告: 读取快捷方式失败: {str(e)}")
                        continue

                    # 使用subprocess.list形式构建命令，避免路径引号问题
                    cmd_list = [
                        chrome_path,
                        f"--user-data-dir={user_data_dir}",
                    ]

                    # 如果启用了CDP，添加调试端口参数
                    if self.enable_cdp:
                        debug_port = 9222 + window_num
                        cmd_list.insert(1, f"--remote-debugging-port={debug_port}")

                    # 添加URL
                    cmd_list.append(url)

                    # 打印命令以便调试
                    print(f"执行命令: {' '.join(cmd_list)}")

                    # 使用不带shell的方式启动进程，避免命令行解析问题
                    subprocess.Popen(cmd_list)

                    success_count += 1
                    print(f"成功在窗口 {window_num} 打开URL: {url}")

                    # 短暂延迟，避免同时打开太多窗口导致系统过载
                    time.sleep(0.1)

                except Exception as e:
                    print(f"打开URL失败 (窗口 {window_num}): {str(e)}")

            # 移除通知提示，操作成功或失败都不再提示
            # if success_count > 0:
            #     self.show_notification("成功", f"成功为 {success_count} 个窗口打开了网页！")
            # else:
            #     messagebox.showerror("失败", "批量打开网页失败！")

        except Exception as e:
            messagebox.showerror("错误", f"批量打开网页失败: {str(e)}")

    def find_chrome_path(self):
        """查找Chrome可执行文件路径"""
        common_paths = [
            r"C:\Program Files\Google\Chrome\Application\chrome.exe",
            r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
            r"C:\Users\%USERNAME%\AppData\Local\Google\Chrome\Application\chrome.exe",
        ]

        # 替换用户名
        username = os.environ.get("USERNAME", "")
        common_paths = [p.replace("%USERNAME%", username) for p in common_paths]

        # 检查常见路径
        for path in common_paths:
            if os.path.exists(path):
                return path

        # 如果找不到，尝试从注册表获取
        try:
            import winreg

            key = winreg.OpenKey(
                winreg.HKEY_LOCAL_MACHINE,
                r"SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\chrome.exe",
            )
            chrome_path, _ = winreg.QueryValueEx(key, None)
            if os.path.exists(chrome_path):
                return chrome_path
        except:
            pass

        # 如果以上方法都失败，返回None
        return None

    def run(self):
        """运行程序"""
        try:
            # 确保窗口快速显示
            print(f"[{time.time() - self.start_time:.3f}s] 开始显示窗口...")
            self.root.deiconify()  # 显示窗口
            self.root.attributes("-topmost", True)  # 先设置为置顶
            self.root.update()  # 强制刷新UI
            self.root.attributes("-topmost", False)  # 取消置顶
            print(f"[{time.time() - self.start_time:.3f}s] 窗口显示完成")

            # 启动主循环
            self.root.mainloop()
        except Exception as e:
            print(f"运行程序时出错: {str(e)}")
            # 确保错误被显示出来
            error_message = f"程序出现错误:\n{str(e)}\n\n{traceback.format_exc()}"
            print(error_message)
            try:
                messagebox.showerror("程序错误", error_message)
            except:
                pass

    def delayed_initialization(self):
        """延迟执行可能耗时的初始化操作"""
        try:
            print(f"[{time.time() - self.start_time:.3f}s] 开始执行延迟初始化")

            # 检查管理员权限(延迟检查)
            if not is_admin():
                print(
                    f"[{time.time() - self.start_time:.3f}s] 检测到非管理员权限，准备提示"
                )

                # 将管理员权限请求延迟显示，确保主窗口已完全显示
                def show_admin_prompt():
                    result = messagebox.askquestion(
                        "权限提示",
                        "没有管理员权限可能无法正常访问某些窗口，是否以管理员身份重新启动？",
                    )
                    if result == "yes":
                        run_as_admin()
                        self.root.destroy()

                # 延迟更长时间显示，避免干扰用户
                self.root.after(1500, show_admin_prompt)
            else:
                print(f"[{time.time() - self.start_time:.3f}s] 已是管理员权限")

            # 预热窗口枚举 (这个操作可能比较慢)
            print(f"[{time.time() - self.start_time:.3f}s] 开始预热窗口枚举...")
            try:
                # 注意：这里不实际填充列表，只做枚举测试
                windows = []
                win32gui.EnumWindows(self.enum_window_callback, windows)
                print(f"[{time.time() - self.start_time:.3f}s] 预热窗口枚举完成")
            except Exception as e:
                print(
                    f"[{time.time() - self.start_time:.3f}s] 预热窗口枚举失败: {str(e)}"
                )

            # 其他可能耗时的初始化可以放在这里

            print(f"[{time.time() - self.start_time:.3f}s] 所有延迟初始化任务完成")
        except Exception as e:
            print(f"[{time.time() - self.start_time:.3f}s] 延迟初始化出错: {str(e)}")

    def load_window_position(self):
        # 从 settings.json 加载窗口位置
        try:
            position = self.settings.get("window_position")
            if position:
                # 检查是否只包含位置信息（以+开头）
                if position.startswith("+"):
                    return position  # 直接返回位置信息

                # 处理包含尺寸的旧格式（"widthxheight+x+y"）
                if "x" in position and "+" in position:
                    parts = position.split("+")
                    if len(parts) >= 3:
                        return f"+{parts[1]}+{parts[2]}"  # 只返回位置部分
            return None
        except Exception as e:
            print(f"加载窗口位置失败: {str(e)}")
            return None

    def save_window_position(self):
        # 保存窗口位置到 settings.json（只保存位置，不保存尺寸）
        try:
            # 获取窗口当前位置
            geometry = self.root.geometry()

            # 提取位置信息 (x和y坐标)
            position_parts = geometry.split("+")
            if len(position_parts) >= 3:
                x_pos = position_parts[1]
                y_pos = position_parts[2]
                position = f"+{x_pos}+{y_pos}"  # 只保存位置信息

                # 保存到设置
                self.settings["window_position"] = position

                # 写入文件
                with open("settings.json", "w", encoding="utf-8") as f:
                    json.dump(self.settings, f, ensure_ascii=False, indent=4)
        except Exception as e:
            print(f"保存窗口位置失败: {str(e)}")

    def get_chrome_popups(self, chrome_hwnd):
        """改进的插件窗口检测，支持网页触发的钱包插件和网页浮动层"""
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
                    if "Chrome_WidgetWin" in class_name:  # 放宽类名匹配条件
                        # 检查是否是扩展程序相关窗口，放宽检测条件
                        style = win32gui.GetWindowLong(hwnd, win32con.GWL_STYLE)
                        ex_style = win32gui.GetWindowLong(hwnd, win32con.GWL_EXSTYLE)

                        # 扩展窗口的特征
                        is_popup = (
                            "扩展程序" in title
                            or "插件" in title
                            or "OKX" in title  # 常见钱包名称
                            or "MetaMask" in title  # 常见钱包名称
                            or "钱包" in title
                            or "Wallet" in title
                            or win32gui.GetParent(hwnd) == chrome_hwnd
                            or (style & win32con.WS_POPUP) != 0
                            or (style & win32con.WS_CHILD) != 0
                            or (ex_style & win32con.WS_EX_TOOLWINDOW) != 0
                            or (ex_style & win32con.WS_EX_DLGMODALFRAME)
                            != 0  # 对话框样式窗口
                        )

                        # 获取窗口位置和大小，钱包插件通常较小
                        rect = win32gui.GetWindowRect(hwnd)
                        width = rect[2] - rect[0]
                        height = rect[3] - rect[1]

                        # 钱包插件窗口通常不会特别大
                        is_wallet_size = (
                            width < 800
                            and height < 800
                            and width > 200
                            and height > 200
                        )

                        # 网页浮动层通常是较小的弹窗
                        is_floating_layer = (
                            (style & win32con.WS_POPUP) != 0
                            and (width < 600 and height < 600)
                            and hwnd != chrome_hwnd
                        )

                        if is_popup or is_wallet_size or is_floating_layer:
                            # 增加额外判断，如果窗口很像钱包弹窗，即使不满足其他条件也捕获
                            if hwnd != chrome_hwnd and hwnd not in popups:
                                if (
                                    self.is_likely_wallet_popup(hwnd, chrome_hwnd)
                                    or is_floating_layer
                                ):
                                    popups.append(hwnd)
                                    print(
                                        f"识别到可能的钱包插件窗口或网页浮动层: {title} (句柄: {hwnd})"
                                    )
                                elif is_popup:
                                    popups.append(hwnd)

            except Exception as e:
                print(f"枚举窗口失败: {str(e)}")

        win32gui.EnumWindows(enum_windows_callback, None)
        return popups

    def is_likely_wallet_popup(self, hwnd, parent_hwnd):
        """检查窗口是否可能是钱包弹出窗口或网页浮动层"""
        try:
            # 常见钱包和浮层关键词
            keywords = [
                "钱包",
                "okx",
                "metamask",
                "token",
                "connect",
                "wallet",
                "sign",
                "signature",
                "transaction",
                "登录",
                "connect",
                "eth",
                "web3",
                "链接",
                "连接",
                "确认",
                "confirm",
                "cancel",
                "取消",
                "dialog",
                "弹出层",
                "浮层",
                "modal",
                "popup",
                "alert",
                "提示",
                "通知",
                "message",
                "消息",
            ]

            # 检查窗口标题
            title = win32gui.GetWindowText(hwnd).lower()
            for keyword in keywords:
                if keyword.lower() in title:
                    return True

            # 尝试获取窗口内部的文本 (使用WM_GETTEXT消息)
            buffer_size = 1024
            buffer = ctypes.create_unicode_buffer(buffer_size)
            try:
                ctypes.windll.user32.SendMessageW(
                    hwnd, win32con.WM_GETTEXT, buffer_size, ctypes.byref(buffer)
                )
                text = buffer.value.lower()
                for keyword in keywords:
                    if keyword.lower() in text:
                        return True
            except:
                pass

            # 检查窗口尺寸和样式特征
            rect = win32gui.GetWindowRect(hwnd)
            width = rect[2] - rect[0]
            height = rect[3] - rect[1]

            # 获取Chrome主窗口位置
            parent_rect = win32gui.GetWindowRect(parent_hwnd)

            # 检查窗口是否在Chrome窗口内或附近
            is_near_chrome = (
                rect[0] >= parent_rect[0] - 100
                and rect[1] >= parent_rect[1] - 100
                and rect[2] <= parent_rect[2] + 100
                and rect[3] <= parent_rect[3] + 100
            )

            # 检查窗口样式
            style = win32gui.GetWindowLong(hwnd, win32con.GWL_STYLE)
            ex_style = win32gui.GetWindowLong(hwnd, win32con.GWL_EXSTYLE)

            # 弹出窗口特征
            has_popup_style = (
                (style & win32con.WS_POPUP) != 0
                or (ex_style & win32con.WS_EX_TOPMOST) != 0
                or (ex_style & win32con.WS_EX_TOOLWINDOW) != 0
            )

            # 检测是否为网页浮动层 (往往会有z-index较高，且有特定样式)
            is_floating_layer = (
                has_popup_style
                and is_near_chrome
                and (200 <= width <= 600 and 100 <= height <= 600)
            )

            # 综合判断
            return (
                (300 <= width <= 600 and 300 <= height <= 800)  # 典型钱包窗口尺寸
                and has_popup_style
                and is_near_chrome
            ) or is_floating_layer

        except Exception as e:
            print(f"判断钱包窗口或浮动层失败: {str(e)}")
            return False

    def monitor_popups(self):
        """监控Chrome弹出窗口，改进以更好地支持钱包插件窗口"""
        last_check_time = time.time()
        last_error_time = 0
        error_count = 0

        # 钱包插件窗口同步历史
        wallet_popup_history = {}

        print("启动弹窗监控线程...")

        while self.is_sync:
            try:
                # 优化CPU使用率
                time.sleep(0.1)  # 更快速的检查以捕获快速弹出的钱包窗口

                # 每500毫秒执行一次完整检查
                current_time = time.time()
                if current_time - last_check_time < 0.5:
                    continue

                last_check_time = current_time

                # 检查主窗口是否有效
                if not self.master_window or not win32gui.IsWindow(self.master_window):
                    if current_time - last_error_time > 10:
                        print("主窗口无效，停止同步")
                        last_error_time = current_time
                    self.stop_sync()
                    break

                # 获取主窗口的弹出窗口
                current_popups = self.get_chrome_popups(self.master_window)

                # 检查是否有新增弹出窗口或关闭的弹出窗口
                new_popups = [
                    popup for popup in current_popups if popup not in self.popup_windows
                ]
                closed_popups = [
                    popup for popup in self.popup_windows if popup not in current_popups
                ]

                has_changes = False

                # 处理新的弹出窗口，特别注意钱包插件窗口
                for popup in new_popups:
                    try:
                        if self.is_sync and win32gui.IsWindow(popup):
                            # 获取窗口标题
                            title = win32gui.GetWindowText(popup)

                            # 检查是否是钱包插件窗口
                            is_wallet = self.is_likely_wallet_popup(
                                popup, self.master_window
                            )

                            if is_wallet:
                                print(f"发现钱包插件窗口: {title}")
                                # 记录钱包窗口信息用于后续处理
                                wallet_popup_history[popup] = {
                                    "detected_time": time.time(),
                                    "title": title,
                                    "synced": False,
                                }

                            # 将弹出窗口添加到同步列表
                            if popup not in self.popup_windows:
                                self.popup_windows.append(popup)
                                has_changes = True
                    except Exception as e:
                        if current_time - last_error_time > 10:
                            print(f"处理新弹窗时出错: {str(e)}")
                            last_error_time = current_time

                # 清理已关闭的弹出窗口
                for popup in closed_popups:
                    if popup in self.popup_windows:
                        self.popup_windows.remove(popup)
                        if popup in wallet_popup_history:
                            del wallet_popup_history[popup]
                        has_changes = True

                # 同步处理钱包窗口和其他弹出窗口
                if has_changes:
                    self.sync_popups()

                # 定期尝试同步钱包插件窗口，即使没有检测到变化
                # 这有助于处理某些难以检测的网页触发钱包窗口
                for hwnd, info in list(wallet_popup_history.items()):
                    if (
                        not info.get("synced")
                        and current_time - info.get("detected_time", 0) > 0.5
                        and win32gui.IsWindow(hwnd)
                    ):
                        # 尝试强制同步钱包窗口
                        try:
                            self.sync_specific_popup(hwnd)
                            info["synced"] = True
                            print(f"强制同步钱包窗口: {info['title']}")
                        except Exception as e:
                            if current_time - last_error_time > 10:
                                print(f"强制同步钱包窗口失败: {str(e)}")
                                last_error_time = current_time

                # 清理无效的历史记录
                for hwnd in list(wallet_popup_history.keys()):
                    if (
                        not win32gui.IsWindow(hwnd)
                        or current_time - wallet_popup_history[hwnd]["detected_time"]
                        > 60
                    ):
                        del wallet_popup_history[hwnd]

            except Exception as e:
                error_count += 1

                # 限制错误日志频率
                if current_time - last_error_time > 10:
                    print(f"弹出窗口监控异常: {str(e)}")
                    last_error_time = current_time

                # 防止过多错误导致CPU占用过高
                if error_count > 100:
                    print("错误次数过多，停止弹窗监控")
                    break

                time.sleep(1)  # 出错后等待一段时间

        print("弹窗监控线程已结束")

    def sync_specific_popup(self, popup_hwnd):
        """单独同步特定的弹出窗口（特别是钱包插件窗口）"""
        try:
            if not win32gui.IsWindow(popup_hwnd):
                return

            # 获取窗口位置
            popup_rect = win32gui.GetWindowRect(popup_hwnd)
            popup_x = popup_rect[0]
            popup_y = popup_rect[1]
            popup_width = popup_rect[2] - popup_rect[0]
            popup_height = popup_rect[3] - popup_rect[1]

            # 获取主窗口位置
            master_rect = win32gui.GetWindowRect(self.master_window)
            master_x = master_rect[0]
            master_y = master_rect[1]

            # 计算相对位置（相对于主窗口左上角）
            relative_x = popup_x - master_x
            relative_y = popup_y - master_y

            # 确保在其他浏览器窗口中也能看到弹出窗口
            for hwnd in self.sync_windows:
                try:
                    if hwnd != self.master_window and win32gui.IsWindow(hwnd):
                        # 获取同步窗口位置
                        sync_rect = win32gui.GetWindowRect(hwnd)
                        sync_x = sync_rect[0]
                        sync_y = sync_rect[1]

                        # 计算新位置（相对于同步窗口）
                        new_x = sync_x + relative_x
                        new_y = sync_y + relative_y

                        # 检查同步窗口的弹出窗口
                        sync_popups = self.get_chrome_popups(hwnd)

                        # 查找匹配的弹出窗口，使用标题和大小作为匹配依据
                        target_title = win32gui.GetWindowText(popup_hwnd)
                        matching_popup = None

                        for sync_popup in sync_popups:
                            if win32gui.IsWindow(sync_popup):
                                sync_popup_title = win32gui.GetWindowText(sync_popup)
                                sync_popup_rect = win32gui.GetWindowRect(sync_popup)
                                sync_popup_width = (
                                    sync_popup_rect[2] - sync_popup_rect[0]
                                )
                                sync_popup_height = (
                                    sync_popup_rect[3] - sync_popup_rect[1]
                                )

                                # 如果标题相似且尺寸相近，认为是匹配的窗口
                                title_similarity = self.title_similarity(
                                    target_title, sync_popup_title
                                )
                                size_match = (
                                    abs(sync_popup_width - popup_width) < 50
                                    and abs(sync_popup_height - popup_height) < 50
                                )

                                if title_similarity > 0.5 or size_match:
                                    matching_popup = sync_popup
                                    break

                        # 移动匹配的弹出窗口
                        if matching_popup:
                            win32gui.SetWindowPos(
                                matching_popup,
                                win32con.HWND_TOP,
                                new_x,
                                new_y,
                                popup_width,
                                popup_height,
                                win32con.SWP_NOACTIVATE,
                            )

                except Exception as e:
                    print(f"同步特定弹窗失败: {str(e)}")

        except Exception as e:
            print(f"同步特定弹窗出错: {str(e)}")

    def title_similarity(self, title1, title2):
        """计算两个窗口标题之间的相似度

        Args:
            title1: 第一个窗口标题
            title2: 第二个窗口标题

        Returns:
            float: 0到1之间的相似度分数，1表示完全匹配
        """
        # 处理空标题的情况
        if not title1 and not title2:
            return 1.0
        if not title1 or not title2:
            return 0.0

        # 转换为小写以进行不区分大小写的比较
        title1 = title1.lower()
        title2 = title2.lower()

        # 计算Jaccard相似度
        set1 = set(title1)
        set2 = set(title2)

        # 计算交集和并集的大小
        intersection_size = len(set1.intersection(set2))
        union_size = len(set1.union(set2))

        # 避免除以零
        if union_size == 0:
            return 1.0

        return intersection_size / union_size

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

        # 设置图标
        try:
            icon_path = os.path.join(os.path.dirname(__file__), "app.ico")
            if os.path.exists(icon_path):
                dialog.iconbitmap(icon_path)
        except Exception as e:
            print(f"设置图标失败: {str(e)}")

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
        on_key_event = None  # 在外部声明，方便后续引用

        def start_recording():
            # 开始录制快捷键
            nonlocal recording, on_key_event
            recording = True
            keys_pressed.clear()
            shortcut_var.set("请按下快捷键组合...")
            record_btn.configure(state="disabled")

            # 定义按键事件处理函数
            def on_key_event_handler(e):
                if not recording:
                    return
                if e.event_type == keyboard.KEY_DOWN:
                    keys_pressed.add(e.name)
                    shortcut_var.set("+".join(sorted(keys_pressed)))
                elif e.event_type == keyboard.KEY_UP:
                    if e.name in keys_pressed:
                        keys_pressed.remove(e.name)
                    if not keys_pressed:
                        stop_recording()

            # 保存引用以便后续取消钩子
            on_key_event = on_key_event_handler

            # 只为录制添加临时钩子
            keyboard.hook(on_key_event)

        def stop_recording():
            # 停止录制快捷键
            nonlocal recording
            recording = False

            # 移除录制时添加的临时钩子，而不是所有钩子
            keyboard.unhook(on_key_event)

            # 不再需要重新设置当前快捷键，保持原状
            record_btn.configure(state="normal")

        # 录制按钮
        record_btn = ttk.Button(dialog, text="开始录制", command=start_recording)
        record_btn.pack(pady=10)

        def save_shortcut():
            # 保存快捷键设置
            new_shortcut = shortcut_var.get()
            if (
                new_shortcut
                and new_shortcut != "点击下方按钮开始录制快捷键..."
                and new_shortcut != "请按下快捷键组合..."
            ):
                try:
                    # 设置新快捷键
                    self.set_shortcut(new_shortcut)

                    # 保存到设置文件
                    settings = self.load_settings()
                    settings["sync_shortcut"] = new_shortcut
                    with open("settings.json", "w", encoding="utf-8") as f:
                        json.dump(settings, f, ensure_ascii=False, indent=4)

                    messagebox.showinfo("成功", f"快捷键已设置为: {new_shortcut}")
                    dialog.destroy()
                except Exception as e:
                    messagebox.showerror("错误", f"设置快捷键失败: {str(e)}")
            else:
                messagebox.showwarning("警告", "请先录制快捷键！")

        # 保存按钮
        ttk.Button(
            dialog, text="保存", style="Accent.TButton", command=save_shortcut
        ).pack(pady=5)

        # 确保关闭对话框时停止录制
        dialog.protocol(
            "WM_DELETE_WINDOW", lambda: [stop_recording(), dialog.destroy()]
        )

        # 居中显示对话框
        dialog.update_idletasks()
        width = dialog.winfo_width()
        height = dialog.winfo_height()
        x = (dialog.winfo_screenwidth() // 2) - (width // 2)
        y = (dialog.winfo_screenheight() // 2) - (height // 2)
        dialog.geometry(f"{width}x{height}+{x}+{y}")

    def set_shortcut(self, shortcut):
        # 设置快捷键
        try:
            # 只清除之前的快捷键钩子，而不是所有钩子
            if hasattr(self, "shortcut_hook") and self.shortcut_hook:
                keyboard.remove_hotkey(self.shortcut_hook)
                self.shortcut_hook = None

            # 设置新的快捷键
            if shortcut:
                # 保存当前快捷键字符串，即使添加热键失败也能保留
                self.current_shortcut = shortcut

                # 添加新的热键钩子
                self.shortcut_hook = keyboard.add_hotkey(
                    shortcut, self.toggle_sync, suppress=True, trigger_on_release=True
                )
                print(f"快捷键 {shortcut} 设置成功")

                # 保存到设置文件
                settings = self.load_settings()
                settings["sync_shortcut"] = shortcut
                with open("settings.json", "w", encoding="utf-8") as f:
                    json.dump(settings, f, ensure_ascii=False, indent=4)

                return True

        except Exception as e:
            print(f"设置快捷键失败: {str(e)}")
            # 不重置current_shortcut，即使失败也保留当前值
            return False

    def update_screen_list(self):
        """更新屏幕列表，返回屏幕名称列表"""
        try:
            screens = []

            def callback(hmonitor, hdc, lprect, lparam):
                try:
                    # 获取显示器信息
                    monitor_info = win32api.GetMonitorInfo(hmonitor)
                    screen_name = f"屏幕 {len(screens) + 1}"
                    if monitor_info["Flags"] & 1:  # MONITORINFOF_PRIMARY
                        screen_name += " (主)"
                    screens.append(
                        {
                            "name": screen_name,
                            "rect": monitor_info["Monitor"],
                            "work_rect": monitor_info["Work"],
                            "monitor": hmonitor,
                        }
                    )
                except Exception as e:
                    print(f"处理显示器信息失败: {str(e)}")
                return True

            # 定义回调函数类型
            MONITORENUMPROC = ctypes.WINFUNCTYPE(
                ctypes.c_bool,
                ctypes.c_ulong,
                ctypes.c_ulong,
                ctypes.POINTER(wintypes.RECT),
                ctypes.c_longlong,
            )

            # 创建回调函数
            callback_function = MONITORENUMPROC(callback)

            # 枚举显示器
            if (
                ctypes.windll.user32.EnumDisplayMonitors(0, 0, callback_function, 0)
                == 0
            ):
                # EnumDisplayMonitors 失败，尝试使用备用方法
                try:
                    # 获取虚拟屏幕范围
                    virtual_width = win32api.GetSystemMetrics(
                        win32con.SM_CXVIRTUALSCREEN
                    )
                    virtual_height = win32api.GetSystemMetrics(
                        win32con.SM_CYVIRTUALSCREEN
                    )
                    virtual_left = win32api.GetSystemMetrics(win32con.SM_XVIRTUALSCREEN)
                    virtual_top = win32api.GetSystemMetrics(win32con.SM_YVIRTUALSCREEN)

                    # 获取主屏幕信息
                    primary_monitor = win32api.MonitorFromPoint(
                        (0, 0), win32con.MONITOR_DEFAULTTOPRIMARY
                    )
                    primary_info = win32api.GetMonitorInfo(primary_monitor)

                    # 添加主屏幕
                    screens.append(
                        {
                            "name": "屏幕 1 (主)",
                            "rect": primary_info["Monitor"],
                            "work_rect": primary_info["Work"],
                            "monitor": primary_monitor,
                        }
                    )

                    # 尝试获取第二个屏幕
                    try:
                        second_monitor = win32api.MonitorFromPoint(
                            (
                                virtual_left + virtual_width - 1,
                                virtual_top + virtual_height // 2,
                            ),
                            win32con.MONITOR_DEFAULTTONULL,
                        )
                        if second_monitor and second_monitor != primary_monitor:
                            second_info = win32api.GetMonitorInfo(second_monitor)
                            screens.append(
                                {
                                    "name": "屏幕 2",
                                    "rect": second_info["Monitor"],
                                    "work_rect": second_info["Work"],
                                    "monitor": second_monitor,
                                }
                            )
                    except:
                        pass

                except Exception as e:
                    print(f"备用方法失败: {str(e)}")

            if not screens:
                # 如果仍然没有找到屏幕，使用基本方案
                screen_width = win32api.GetSystemMetrics(win32con.SM_CXSCREEN)
                screen_height = win32api.GetSystemMetrics(win32con.SM_CYSCREEN)
                screens.append(
                    {
                        "name": "屏幕 1 (主)",
                        "rect": (0, 0, screen_width, screen_height),
                        "work_rect": (0, 0, screen_width, screen_height),
                        "monitor": None,
                    }
                )

            # 按照屏幕位置排序（从左到右）
            screens.sort(key=lambda x: x["rect"][0])

            # 保存屏幕信息
            self.screens = screens

            # 返回屏幕名称列表
            screen_names = [screen["name"] for screen in screens]
            return screen_names

        except Exception as e:
            print(f"获取屏幕列表失败: {str(e)}")
            return ["主屏幕"]  # 返回默认值

    def create_environments(self):
        """批量创建Chrome环境"""
        try:
            # 从设置中获取目录信息
            settings = self.load_settings()
            cache_dir = settings.get("cache_dir", "")
            shortcut_dir = self.shortcut_path
            numbers = self.env_numbers.get().strip()

            if not all([cache_dir, shortcut_dir, numbers]):
                messagebox.showwarning(
                    "警告", "请先在设置中填写缓存目录和快捷方式目录!"
                )
                return

            # 确保目录存在
            os.makedirs(cache_dir, exist_ok=True)
            os.makedirs(shortcut_dir, exist_ok=True)

            # 查找chrome可执行文件
            chrome_path = r"C:\Program Files\Google\Chrome\Application\chrome.exe"
            if not os.path.exists(chrome_path):
                chrome_path = (
                    r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe"
                )

            if not os.path.exists(chrome_path):
                messagebox.showerror("错误", "未找到Chrome安装路径！")
                return

            # 创建WScript.Shell对象
            shell = win32com.client.Dispatch("WScript.Shell")

            # 解析窗口编号
            window_numbers = self.parse_window_numbers(numbers)

            # 批量创建环境
            for i in window_numbers:
                # 创建数据目录 - 使用纯数字命名
                data_dir_name = str(i)  # 改回纯数字命名

                # 使用os.path.join创建路径，然后统一转换为正斜杠格式
                data_dir = os.path.join(cache_dir, data_dir_name)
                data_dir = data_dir.replace("\\", "/")  # 统一使用正斜杠

                os.makedirs(data_dir, exist_ok=True)

                # 创建快捷方式 - 仍然使用数字命名以便识别和分配端口
                shortcut_path = os.path.join(shortcut_dir, f"{i}.lnk")
                shortcut = shell.CreateShortCut(shortcut_path)
                shortcut.TargetPath = chrome_path
                shortcut.Arguments = (
                    f'--user-data-dir="{data_dir}"'  # 使用统一的正斜杠格式
                )
                shortcut.WorkingDirectory = os.path.dirname(chrome_path)
                shortcut.WindowStyle = 1  # 正常窗口
                shortcut.IconLocation = f"{chrome_path},0"
                shortcut.save()

            messagebox.showinfo(
                "成功", f"已成功创建 {len(window_numbers)} 个Chrome环境！"
            )

        except Exception as e:
            messagebox.showerror("错误", f"创建环境失败: {str(e)}")

    def setup_hotkey_message_handler(self):
        """设置热键消息处理"""
        try:
            # 获取窗口句柄
            hwnd = int(self.root.winfo_id())

            # 在这里我们添加额外的保障，确保快捷键设置有效
            if hasattr(self, "current_shortcut") and self.current_shortcut:
                # 重新确认快捷键有效性
                if hasattr(self, "shortcut_hook") and not self.shortcut_hook:
                    # 如果快捷键被清除，重新设置
                    self.set_shortcut(self.current_shortcut)
                    print(f"已重新设置快捷键: {self.current_shortcut}")

            # 使用定时器检查热键状态
            def check_hotkey():
                try:
                    if self.current_shortcut and keyboard.is_pressed(
                        self.current_shortcut
                    ):
                        # 确保不会重复触发
                        keyboard.release(self.current_shortcut)
                        # 在主线程中执行toggle_sync
                        self.root.after(0, self.toggle_sync)

                        # 额外打印调试信息
                        print(f"检测到快捷键 {self.current_shortcut} 被按下")
                except Exception as e:
                    print(f"检查热键状态失败: {str(e)}")

                    # 尝试恢复快捷键设置
                    if hasattr(self, "current_shortcut") and self.current_shortcut:
                        try:
                            self.set_shortcut(self.current_shortcut)
                            print(f"已尝试恢复快捷键: {self.current_shortcut}")
                        except:
                            pass
                finally:
                    # 继续检查
                    if not self.root.winfo_exists():
                        return
                    self.root.after(100, check_hotkey)

            # 启动检查
            check_hotkey()

        except Exception as e:
            print(f"设置热键消息处理失败: {str(e)}")

    def show_settings_dialog(self):
        """显示设置对话框"""
        # 创建设置对话框
        settings_dialog = tk.Toplevel(self.root)
        settings_dialog.title("设置")
        settings_dialog.geometry("500x370")  # 增加窗口高度
        settings_dialog.resizable(False, False)
        settings_dialog.transient(self.root)
        settings_dialog.grab_set()

        # 设置图标
        try:
            icon_path = os.path.join(os.path.dirname(__file__), "app.ico")
            if os.path.exists(icon_path):
                settings_dialog.iconbitmap(icon_path)
        except Exception as e:
            print(f"设置图标失败: {str(e)}")

        # 创建内容和按钮的主框架
        main_frame = ttk.Frame(settings_dialog)
        main_frame.pack(fill=tk.BOTH, expand=True)

        # 创建内容框架
        content_frame = ttk.Frame(main_frame)
        content_frame.pack(fill=tk.BOTH, expand=True, padx=10, pady=10)

        # 目录设置框架
        settings_frame = ttk.LabelFrame(content_frame, text="目录设置", padding=10)
        settings_frame.pack(fill=tk.X, pady=5)

        # 加载当前设置
        settings = self.load_settings()

        # 快捷方式目录
        shortcut_frame = ttk.Frame(settings_frame)
        shortcut_frame.pack(fill=tk.X, pady=5)
        ttk.Label(shortcut_frame, text="谷歌多开快捷方式目录:").pack(side=tk.LEFT)
        shortcut_path_var = tk.StringVar(
            value=self.shortcut_path or settings.get("shortcut_path", "")
        )
        shortcut_path_entry = ttk.Entry(shortcut_frame, textvariable=shortcut_path_var)
        shortcut_path_entry.pack(side=tk.LEFT, fill=tk.X, expand=True, padx=5)
        self.setup_right_click_menu(shortcut_path_entry)
        ttk.Button(
            shortcut_frame,
            text="浏览",
            command=lambda: shortcut_path_var.set(
                filedialog.askdirectory(
                    initialdir=shortcut_path_var.get() or os.getcwd()
                )
            ),
        ).pack(side=tk.LEFT)

        # 缓存存放目录
        cache_frame = ttk.Frame(settings_frame)
        cache_frame.pack(fill=tk.X, pady=5)
        ttk.Label(cache_frame, text="谷歌多开缓存存放目录:").pack(side=tk.LEFT)
        cache_dir_var = tk.StringVar(
            value=self.cache_dir or settings.get("cache_dir", "")
        )
        cache_dir_entry = ttk.Entry(cache_frame, textvariable=cache_dir_var)
        cache_dir_entry.pack(side=tk.LEFT, fill=tk.X, expand=True, padx=5)
        self.setup_right_click_menu(cache_dir_entry)
        ttk.Button(
            cache_frame,
            text="浏览",
            command=lambda: cache_dir_var.set(
                filedialog.askdirectory(initialdir=cache_dir_var.get() or os.getcwd())
            ),
        ).pack(side=tk.LEFT)

        # 快捷方式图标资源目录
        icon_frame = ttk.Frame(settings_frame)
        icon_frame.pack(fill=tk.X, pady=5)
        ttk.Label(icon_frame, text="快捷方式图标资源目录:").pack(side=tk.LEFT)
        icon_dir_var = tk.StringVar(value=self.icon_dir or settings.get("icon_dir", ""))
        icon_dir_entry = ttk.Entry(icon_frame, textvariable=icon_dir_var)
        icon_dir_entry.pack(side=tk.LEFT, fill=tk.X, expand=True, padx=5)
        self.setup_right_click_menu(icon_dir_entry)
        ttk.Button(
            icon_frame,
            text="浏览",
            command=lambda: icon_dir_var.set(
                filedialog.askdirectory(initialdir=icon_dir_var.get() or os.getcwd())
            ),
        ).pack(side=tk.LEFT)

        # 功能设置
        function_frame = ttk.LabelFrame(content_frame, text="功能设置", padding=10)
        function_frame.pack(fill=tk.X, pady=5)

        # 屏幕选择
        screen_frame = ttk.Frame(function_frame)
        screen_frame.pack(fill=tk.X, pady=5)
        ttk.Label(screen_frame, text="屏幕选择:").pack(side=tk.LEFT)

        # 更新屏幕列表
        screen_options = self.update_screen_list()
        if not screen_options:
            screen_options = ["主屏幕"]

        screen_var = tk.StringVar(value=settings.get("screen_selection", ""))
        screen_combo = ttk.Combobox(
            screen_frame, textvariable=screen_var, width=15, state="readonly"
        )
        screen_combo.pack(side=tk.LEFT, padx=5)
        screen_combo["values"] = screen_options

        # 如果之前选过屏幕且还在列表中，则选中它
        if screen_var.get() and screen_var.get() in screen_options:
            screen_combo.set(screen_var.get())
        # 否则默认选择第一个屏幕
        elif screen_options:
            screen_combo.current(0)

        # 快捷键设置
        shortcut_frame = ttk.Frame(function_frame)
        shortcut_frame.pack(fill=tk.X, pady=5)
        ttk.Label(shortcut_frame, text="快捷键设置:").pack(side=tk.LEFT)
        shortcut_button = ttk.Button(
            shortcut_frame, text="设置快捷键", command=self.show_shortcut_dialog
        )
        shortcut_button.pack(side=tk.LEFT, padx=5)

        # 底部按钮框架
        button_frame = ttk.Frame(settings_dialog)
        button_frame.pack(side=tk.BOTTOM, fill=tk.X, padx=10, pady=10)

        cancel_button = ttk.Button(
            button_frame, text="取消", command=settings_dialog.destroy
        )
        cancel_button.pack(side=tk.RIGHT, padx=5)

        save_button = ttk.Button(
            button_frame,
            text="保存",
            style="Accent.TButton",
            command=lambda: self.save_settings_dialog(
                settings_dialog,
                shortcut_path_var.get(),
                cache_dir_var.get(),
                icon_dir_var.get(),
                screen_var.get(),
            ),
        )
        save_button.pack(side=tk.RIGHT, padx=5)

        # 居中显示
        self.center_window(settings_dialog)

        # 为对话框中所有文本框添加右键菜单支持
        def add_right_click_to_all_entries(parent):
            """为所有文本框添加右键菜单"""
            for child in parent.winfo_children():
                if isinstance(child, (tk.Entry, ttk.Entry, tk.Text, ttk.Combobox)):
                    self.setup_right_click_menu(child)
                elif child.winfo_children():
                    add_right_click_to_all_entries(child)

        # 在对话框创建完成后应用右键菜单
        settings_dialog.after(
            100, lambda: add_right_click_to_all_entries(settings_dialog)
        )

    def save_settings_dialog(self, dialog, shortcut_path, cache_dir, icon_dir, screen):
        """保存设置对话框中的设置"""
        try:
            print("保存前设置:", self.load_settings())  # 调试输出

            # 更新当前实例变量，确保在本次会话中立即生效
            self.shortcut_path = shortcut_path
            self.cache_dir = cache_dir
            self.icon_dir = icon_dir
            self.screen_selection = screen
            self.enable_cdp = True  # 始终开启CDP

            # 准备新设置
            new_settings = {
                "shortcut_path": shortcut_path,
                "cache_dir": cache_dir,
                "icon_dir": icon_dir,
                "screen_selection": screen,
                "enable_cdp": True,  # 始终开启CDP
            }

            # 加载现有设置(不覆盖窗口等其他设置)
            settings = self.load_settings()
            settings.update(new_settings)  # 更新设置

            # 直接写入文件
            with open("settings.json", "w", encoding="utf-8") as f:
                json.dump(settings, f, ensure_ascii=False, indent=4)

            # 如果页面上有路径输入框，更新它
            if hasattr(self, "path_entry") and self.path_entry is not None:
                self.path_entry.delete(0, tk.END)
                self.path_entry.insert(0, shortcut_path)

            print("保存后设置:", settings)  # 调试输出

            # 显示成功消息
            messagebox.showinfo("成功", "设置已保存！")
            dialog.destroy()

        except Exception as e:
            messagebox.showerror("错误", f"保存设置失败: {e}")
            print(f"保存设置失败: {e}")

    def center_window(self, window):
        """将窗口居中显示在屏幕上"""
        # 先隐藏窗口，以便计算尺寸
        window.withdraw()

        # 更新窗口尺寸
        window.update_idletasks()

        # 获取屏幕尺寸
        screen_width = window.winfo_screenwidth()
        screen_height = window.winfo_screenheight()

        # 获取窗口尺寸
        window_width = window.winfo_width()
        window_height = window.winfo_height()

        # 确保窗口尺寸正确
        if window_width < 100 or window_height < 100:
            # 使用窗口请求的尺寸
            geometry = window.geometry()
            if "x" in geometry and "+" in geometry:
                size_part = geometry.split("+")[0]
                if "x" in size_part:
                    parts = size_part.split("x")
                    if len(parts) == 2 and parts[0].isdigit() and parts[1].isdigit():
                        window_width = int(parts[0])
                        window_height = int(parts[1])

        # 计算居中位置
        x = (screen_width - window_width) // 2
        y = (screen_height - window_height) // 2

        # 设置窗口位置
        window.geometry(f"{window_width}x{window_height}+{x}+{y}")

        # 显示窗口
        window.deiconify()

    def keep_only_current_tab(self):
        """仅保留当前标签页，关闭所有选中窗口的其它标签页（高性能版）"""
        # 立即显示视觉反馈
        self.root.config(cursor="wait")  # 修改光标为等待状态

        # 获取选中的窗口
        selected = []
        try:
            for item in self.window_list.get_children():
                if self.window_list.set(item, "select") == "√":
                    values = self.window_list.item(item)["values"]
                    if len(values) >= 5:
                        hwnd = int(values[4])
                        window_num = int(values[1])
                        selected.append((window_num, hwnd))
        except Exception as e:
            print(f"获取选中窗口失败: {str(e)}")
            self.root.config(cursor="")  # 恢复光标
            messagebox.showerror("错误", f"获取选中窗口失败: {str(e)}")
            return

        if not selected:
            self.root.config(cursor="")  # 恢复光标
            messagebox.showinfo("提示", "请先选择要操作的窗口！")
            return

        # 如果debug_ports为空，尝试重建
        if not hasattr(self, "debug_ports") or not self.debug_ports:
            print("未找到调试端口映射，尝试重建...")
            self.debug_ports = {
                window_num: 9222 + window_num for window_num, _ in selected
            }

        # 使用ThreadPoolExecutor在后台处理所有标签页操作
        # 不再暂停同步功能，两者可以同时运行
        def process_tabs():
            try:
                # 并行获取所有窗口的标签信息
                port_to_tabs = {}

                def get_tabs(window_data):
                    window_num, _ = window_data
                    if window_num in self.debug_ports:
                        port = self.debug_ports[window_num]
                        try:
                            # 使用更短的超时时间提高响应速度
                            response = requests.get(
                                f"http://localhost:{port}/json", timeout=0.5
                            )
                            if response.status_code == 200:
                                tabs = response.json()
                                page_tabs = [
                                    tab for tab in tabs if tab.get("type") == "page"
                                ]
                                if len(page_tabs) > 1:  # 如果只有一个标签页则不处理
                                    return port, page_tabs, window_num
                        except Exception as e:
                            print(f"获取窗口{window_num}的标签页失败: {str(e)}")
                    return None

                # 并行获取所有窗口的标签页
                with concurrent.futures.ThreadPoolExecutor(max_workers=20) as executor:
                    futures = []
                    for window_data in selected:
                        futures.append(executor.submit(get_tabs, window_data))

                    # 立即处理结果，不等待所有任务完成
                    for future in concurrent.futures.as_completed(futures):
                        result = future.result()
                        if result:
                            port, tabs, window_num = result
                            port_to_tabs[port] = (tabs, window_num)

                # 如果没有可操作的标签页，立即结束并恢复光标
                if not port_to_tabs:
                    self.root.after(0, lambda: self.root.config(cursor=""))
                    return

                # 准备并行关闭请求
                close_requests = []

                for port, (tabs, window_num) in port_to_tabs.items():
                    keep_tab = tabs[0]  # 始终保留第一个标签
                    to_close = []
                    for tab in tabs:
                        if tab.get("id") != keep_tab.get("id"):
                            to_close.append((port, tab.get("id")))
                    close_requests.extend(to_close)

                # 并行执行所有关闭请求
                def close_tab(request):
                    port, tab_id = request
                    try:
                        requests.get(
                            f"http://localhost:{port}/json/close/{tab_id}", timeout=0.5
                        )
                        return True
                    except Exception as e:
                        print(f"关闭标签页失败: {str(e)}")
                        return False

                # 使用更大的线程池来加速处理
                with concurrent.futures.ThreadPoolExecutor(max_workers=30) as executor:
                    futures = [
                        executor.submit(close_tab, req) for req in close_requests
                    ]
                    for future in concurrent.futures.as_completed(futures):
                        future.result()  # 仅为了确保所有请求完成

                # 操作完成后立即恢复光标
                self.root.after(0, lambda: self.root.config(cursor=""))

            except Exception as e:
                print(f"处理标签页时出错: {str(e)}")
                traceback.print_exc()
                # 确保UI状态恢复
                self.root.after(0, lambda: self.root.config(cursor=""))
                self.root.after(
                    0,
                    lambda: messagebox.showerror("错误", f"处理标签页时出错: {str(e)}"),
                )

        # 启动后台线程处理，不阻塞UI
        threading.Thread(target=process_tabs, daemon=True).start()

    def keep_only_new_tab(self):
        """仅保留新标签页，关闭所有选中窗口的其它标签页（高性能版）"""
        # 立即显示视觉反馈
        self.root.config(cursor="wait")  # 修改光标为等待状态

        # 获取选中的窗口
        selected = []
        try:
            for item in self.window_list.get_children():
                if self.window_list.set(item, "select") == "√":
                    values = self.window_list.item(item)["values"]
                    if len(values) >= 5:
                        hwnd = int(values[4])
                        window_num = int(values[1])
                        selected.append((window_num, hwnd))
        except Exception as e:
            print(f"获取选中窗口失败: {str(e)}")
            self.root.config(cursor="")  # 恢复光标
            messagebox.showerror("错误", f"获取选中窗口失败: {str(e)}")
            return

        if not selected:
            self.root.config(cursor="")  # 恢复光标
            messagebox.showinfo("提示", "请先选择要操作的窗口！")
            return

        # 如果debug_ports为空，尝试重建
        if not hasattr(self, "debug_ports") or not self.debug_ports:
            print("未找到调试端口映射，尝试重建...")
            self.debug_ports = {
                window_num: 9222 + window_num for window_num, _ in selected
            }

        # 使用ThreadPoolExecutor在后台处理所有标签页操作
        # 不再暂停同步功能，两者可以同时运行
        def process_tabs():
            try:
                # 并行获取所有窗口的标签信息
                window_tabs = {}

                def get_tabs(window_data):
                    window_num, _ = window_data
                    if window_num in self.debug_ports:
                        port = self.debug_ports[window_num]
                        try:
                            # 使用更短的超时时间提高响应速度
                            response = requests.get(
                                f"http://localhost:{port}/json", timeout=0.5
                            )
                            if response.status_code == 200:
                                tabs = response.json()
                                page_tabs = [
                                    tab.get("id")
                                    for tab in tabs
                                    if tab.get("type") == "page"
                                ]
                                if page_tabs:
                                    return port, page_tabs, window_num
                        except Exception as e:
                            print(f"获取窗口{window_num}的标签页失败: {str(e)}")
                    return None

                # 并行获取所有窗口的标签页
                valid_ports = []
                with concurrent.futures.ThreadPoolExecutor(max_workers=20) as executor:
                    futures = []
                    for window_data in selected:
                        futures.append(executor.submit(get_tabs, window_data))

                    # 立即处理结果，不等待所有任务完成
                    for future in concurrent.futures.as_completed(futures):
                        result = future.result()
                        if result:
                            port, tabs, window_num = result
                            window_tabs[port] = (tabs, window_num)
                            valid_ports.append(port)

                # 如果没有可操作的标签页，立即结束并恢复光标
                if not valid_ports:
                    self.root.after(0, lambda: self.root.config(cursor=""))
                    return

                # 并行为所有窗口创建新标签页
                created_tabs = {}

                def create_new_tab(port_data):
                    port, window_num = port_data
                    try:
                        requests.put(
                            f"http://localhost:{port}/json/new?chrome://newtab/",
                            timeout=0.5,
                        )
                        return port, window_num, True
                    except Exception as e:
                        print(f"为窗口 {window_num} 创建新标签页失败: {str(e)}")
                        return port, window_num, False

                # 并行创建新标签页
                port_to_window = {
                    port: window_num for port, (_, window_num) in window_tabs.items()
                }
                with concurrent.futures.ThreadPoolExecutor(max_workers=20) as executor:
                    futures = [
                        executor.submit(create_new_tab, (port, port_to_window[port]))
                        for port in valid_ports
                    ]
                    for future in concurrent.futures.as_completed(futures):
                        port, window_num, success = future.result()
                        if success:
                            created_tabs[window_num] = port

                # 并行关闭原有标签页
                def close_old_tabs(port_data):
                    port, tabs, window_num = port_data
                    for tab_id in tabs:
                        try:
                            requests.get(
                                f"http://localhost:{port}/json/close/{tab_id}",
                                timeout=0.5,
                            )
                        except Exception as e:
                            print(f"关闭窗口 {window_num} 的标签页失败: {str(e)}")

                # 只有在成功创建了新标签页的窗口才关闭旧标签页
                with concurrent.futures.ThreadPoolExecutor(max_workers=20) as executor:
                    futures = []
                    for window_num, port in created_tabs.items():
                        tabs, _ = window_tabs[port]
                        futures.append(
                            executor.submit(close_old_tabs, (port, tabs, window_num))
                        )

                    # 等待所有关闭操作完成
                    for future in concurrent.futures.as_completed(futures):
                        future.result()

                # 操作完成后立即恢复光标
                self.root.after(0, lambda: self.root.config(cursor=""))

            except Exception as e:
                print(f"处理标签页时出错: {str(e)}")
                traceback.print_exc()
                # 确保UI状态恢复
                self.root.after(0, lambda: self.root.config(cursor=""))
                self.root.after(
                    0,
                    lambda: messagebox.showerror("错误", f"处理标签页时出错: {str(e)}"),
                )

        # 启动后台线程处理，不阻塞UI
        threading.Thread(target=process_tabs, daemon=True).start()

    def set_quick_url(self, url_template):
        """设置快捷网址模板到URL输入框"""
        # 清空现有内容
        self.url_entry.delete(0, tk.END)

        # 根据不同的模板设置不同的URL组合
        if url_template == "x.com" or url_template == "https://twitter.com":
            self.url_entry.insert(0, "x.com")
        elif (
            url_template == "discord.com/app"
            or url_template == "https://discord.com/channels/@me"
        ):
            self.url_entry.insert(0, "discord.com/app")
        elif (
            url_template == "mail.google.com"
            or url_template == "https://mail.google.com"
        ):
            self.url_entry.insert(0, "mail.google.com")
        else:
            # 对于其他URL，直接使用传入的值
            self.url_entry.insert(0, url_template)

        # 自动触发批量打开网页
        self.batch_open_urls()

    # 添加右键菜单相关函数
    def show_context_menu(self, event):
        """显示右键菜单"""
        widget = event.widget
        if isinstance(widget, (tk.Entry, ttk.Entry, tk.Text, ttk.Combobox)):
            # 保存当前文本框引用
            self.current_text_widget = widget
            # 显示菜单
            try:
                self.context_menu.tk_popup(event.x_root, event.y_root)
            finally:
                self.context_menu.grab_release()

    def cut_text(self):
        """剪切文本"""
        if self.current_text_widget:
            try:
                self.current_text_widget.event_generate("<<Cut>>")
            except:
                pass

    def copy_text(self):
        """复制文本"""
        if self.current_text_widget:
            try:
                self.current_text_widget.event_generate("<<Copy>>")
            except:
                pass

    def paste_text(self):
        """粘贴文本"""
        if self.current_text_widget:
            try:
                self.current_text_widget.event_generate("<<Paste>>")
            except:
                pass

    def select_all_text(self):
        """全选文本"""
        if self.current_text_widget:
            try:
                if isinstance(
                    self.current_text_widget, (tk.Entry, ttk.Entry, ttk.Combobox)
                ):
                    self.current_text_widget.select_range(0, tk.END)
                    self.current_text_widget.icursor(tk.END)
                elif isinstance(self.current_text_widget, tk.Text):
                    self.current_text_widget.tag_add(tk.SEL, "1.0", tk.END)
                    self.current_text_widget.mark_set(tk.INSERT, tk.END)
            except:
                pass

    def setup_right_click_menu(self, widget):
        """为文本框设置右键菜单"""
        widget.bind("<Button-3>", self.show_context_menu)

    def show_window_list_menu(self, event):
        """显示窗口列表的右键菜单"""
        try:
            # 获取点击的行
            item = self.window_list.identify_row(event.y)
            if item:
                # 保存当前右键点击的项
                self.right_clicked_item = item
                # 在点击位置显示菜单
                self.window_list_menu.post(event.x_root, event.y_root)
        except Exception as e:
            print(f"显示右键菜单失败: {str(e)}")

    def close_selected_window(self):
        """关闭右键菜单选中的窗口"""
        try:
            if hasattr(self, "right_clicked_item") and self.right_clicked_item:
                item = self.right_clicked_item
                # 从values中获取hwnd
                values = self.window_list.item(item)["values"]
                if values and len(values) > 4:
                    hwnd = int(values[4])
                    # 检查窗口是否存在
                    if win32gui.IsWindow(hwnd):
                        # 关闭窗口
                        win32gui.PostMessage(hwnd, win32con.WM_CLOSE, 0, 0)
                        # 从列表中删除
                        self.window_list.delete(item)
                        # 更新UI
                        self.update_select_all_status()
        except Exception as e:
            print(f"关闭窗口失败: {str(e)}")

    def sync_popups(self):
        """同步主窗口的弹出窗口到所有同步窗口，改进对网页浮动层的处理"""
        try:
            if (
                not self.is_sync
                or not self.master_window
                or not win32gui.IsWindow(self.master_window)
            ):
                return

            # 获取主窗口的所有弹出窗口
            master_popups = self.get_chrome_popups(self.master_window)
            if not master_popups:
                return

            # 获取主窗口位置
            master_rect = win32gui.GetWindowRect(self.master_window)
            master_x = master_rect[0]
            master_y = master_rect[1]

            # 针对每个主窗口的弹出窗口进行同步
            for popup in master_popups:
                try:
                    if not win32gui.IsWindow(popup):
                        continue

                    # 获取弹出窗口位置和大小
                    popup_rect = win32gui.GetWindowRect(popup)
                    popup_width = popup_rect[2] - popup_rect[0]
                    popup_height = popup_rect[3] - popup_rect[1]

                    # 检查窗口样式，确定是否为网页浮动层
                    style = win32gui.GetWindowLong(popup, win32con.GWL_STYLE)
                    ex_style = win32gui.GetWindowLong(popup, win32con.GWL_EXSTYLE)

                    is_floating_layer = (style & win32con.WS_POPUP) != 0 and (
                        popup_width < 600 and popup_height < 600
                    )

                    # 计算相对于主窗口的位置
                    rel_x = popup_rect[0] - master_x
                    rel_y = popup_rect[1] - master_y

                    # 同步到所有其他窗口
                    for hwnd in self.sync_windows:
                        if hwnd != self.master_window and win32gui.IsWindow(hwnd):
                            # 获取同步窗口位置
                            sync_rect = win32gui.GetWindowRect(hwnd)
                            sync_x = sync_rect[0]
                            sync_y = sync_rect[1]

                            # 获取该窗口的所有弹出窗口
                            sync_popups = self.get_chrome_popups(hwnd)

                            # 寻找可能匹配的弹出窗口
                            target_title = win32gui.GetWindowText(popup)
                            best_match = None
                            best_score = 0

                            # 对网页浮动层和其他弹出窗口应用不同的匹配策略
                            if is_floating_layer:
                                # 为浮动层寻找相似的大小和位置
                                for sync_popup in sync_popups:
                                    if not win32gui.IsWindow(sync_popup):
                                        continue

                                    sync_style = win32gui.GetWindowLong(
                                        sync_popup, win32con.GWL_STYLE
                                    )

                                    # 检查是否同样是弹出样式
                                    if (sync_style & win32con.WS_POPUP) == 0:
                                        continue

                                    # 对于网页浮动层，主要基于尺寸和位置相似度匹配
                                    sync_rect = win32gui.GetWindowRect(sync_popup)
                                    sync_width = sync_rect[2] - sync_rect[0]
                                    sync_height = sync_rect[3] - sync_rect[1]

                                    # 尺寸相似度
                                    size_match = 1.0 - min(
                                        1.0,
                                        (
                                            abs(sync_width - popup_width)
                                            / max(popup_width, 1)
                                            + abs(sync_height - popup_height)
                                            / max(popup_height, 1)
                                        )
                                        / 2,
                                    )

                                    # 相对位置相似度
                                    sync_rel_x = sync_rect[0] - sync_x
                                    sync_rel_y = sync_rect[1] - sync_y
                                    pos_match = 1.0 - min(
                                        1.0,
                                        (
                                            abs(sync_rel_x - rel_x)
                                            + abs(sync_rel_y - rel_y)
                                        )
                                        / max(
                                            sync_rect[2]
                                            - sync_rect[0]
                                            + sync_rect[3]
                                            - sync_rect[1],
                                            1,
                                        ),
                                    )

                                    # 综合得分，对于浮动层位置更重要
                                    score = size_match * 0.4 + pos_match * 0.6

                                    if (
                                        score > best_score and score > 0.6
                                    ):  # 提高匹配阈值
                                        best_score = score
                                        best_match = sync_popup
                            else:
                                # 对于普通弹出窗口，使用标题和尺寸综合匹配
                                for sync_popup in sync_popups:
                                    if not win32gui.IsWindow(sync_popup):
                                        continue

                                    sync_title = win32gui.GetWindowText(sync_popup)
                                    # 计算标题相似度
                                    similarity = self.title_similarity(
                                        target_title, sync_title
                                    )

                                    # 获取窗口大小相似度
                                    sync_rect = win32gui.GetWindowRect(sync_popup)
                                    sync_width = sync_rect[2] - sync_rect[0]
                                    sync_height = sync_rect[3] - sync_rect[1]
                                    size_match = min(
                                        1.0,
                                        1.0
                                        - (
                                            abs(sync_width - popup_width)
                                            + abs(sync_height - popup_height)
                                        )
                                        / max(popup_width + popup_height, 1),
                                    )

                                    # 计算总匹配分数
                                    score = similarity * 0.7 + size_match * 0.3
                                    if score > best_score and score > 0.5:
                                        best_score = score
                                        best_match = sync_popup

                            # 如果找到匹配的弹出窗口，调整其位置
                            if best_match:
                                # 计算新位置
                                new_x = sync_x + rel_x
                                new_y = sync_y + rel_y

                                # 设置窗口位置
                                win32gui.SetWindowPos(
                                    best_match,
                                    win32con.HWND_TOP,
                                    new_x,
                                    new_y,
                                    popup_width,
                                    popup_height,
                                    win32con.SWP_NOACTIVATE,
                                )
                            elif is_floating_layer:
                                # 如果是浮动层但没找到匹配项，尝试通过模拟点击关闭和重新打开的方式同步
                                print(f"找不到匹配的浮动层，尝试通过其他方式同步")
                                # 这里可以实现其他同步策略，如向目标窗口发送模拟点击
                                # 由于模拟点击可能较复杂，这里只记录日志
                except Exception as e:
                    print(f"同步单个弹窗出错: {str(e)}")

        except Exception as e:
            print(f"同步弹窗过程出错: {str(e)}")

    def setup_wheel_hook(self):
        """设置全局滚轮钩子"""
        if self.wheel_hook_id:
            # 如果已经有钩子，先卸载
            self.unhook_wheel()

        # 定义钩子回调函数
        def wheel_proc(nCode, wParam, lParam):
            try:
                # 检查是否为滚轮消息
                if wParam == win32con.WM_MOUSEWHEEL and self.is_sync:
                    # 获取当前窗口
                    current_window = win32gui.GetForegroundWindow()

                    # 检查是否为主控窗口
                    is_master_window = current_window == self.master_window

                    # 获取主窗口的弹出窗口
                    master_popups = self.get_chrome_popups(self.master_window)

                    # 判断是否为主窗口的插件
                    is_master_plugin = current_window in master_popups

                    # 如果不是主控窗口也不是主窗口插件，直接放行事件
                    if not is_master_window and not is_master_plugin:
                        return ctypes.windll.user32.CallNextHookEx(
                            self.wheel_hook_id,
                            nCode,
                            wParam,
                            ctypes.cast(lParam, ctypes.c_void_p),
                        )

                    # 获取窗口层次结构信息
                    try:
                        # 获取窗口类名和标题
                        window_class = win32gui.GetClassName(current_window)
                        window_title = win32gui.GetWindowText(current_window)

                        # 获取窗口样式
                        style = win32gui.GetWindowLong(
                            current_window, win32con.GWL_STYLE
                        )
                        ex_style = win32gui.GetWindowLong(
                            current_window, win32con.GWL_EXSTYLE
                        )

                        # 获取窗口进程ID
                        _, process_id = win32process.GetWindowThreadProcessId(
                            current_window
                        )
                        _, master_process_id = win32process.GetWindowThreadProcessId(
                            self.master_window
                        )

                        # 获取位置和尺寸
                        rect = win32gui.GetWindowRect(current_window)
                        width = rect[2] - rect[0]
                        height = rect[3] - rect[1]
                    except:
                        window_class = ""
                        window_title = ""
                        style = 0
                        ex_style = 0
                        process_id = 0
                        master_process_id = 0
                        width = 0
                        height = 0

                    # 检查是否为无法用键盘控制滚动的特殊窗口
                    is_uncontrollable_window = False
                    if "Chrome_RenderWidgetHostHWND" in window_class:
                        is_uncontrollable_window = True

                    # 检查是否与Chrome相关
                    is_chrome_window = (
                        "Chrome_" in window_class or "Chromium_" in window_class
                    )

                    # 检查是否是插件窗口
                    is_plugin_window = is_master_plugin

                    if is_master_plugin:
                        print(
                            f"识别到主窗口插件: {window_title}, 句柄: {current_window}"
                        )

                    # 检查Ctrl键状态 - 如果按下Ctrl则不拦截事件（保留缩放功能）
                    ctrl_pressed = (
                        ctypes.windll.user32.GetKeyState(win32con.VK_CONTROL) & 0x8000
                        != 0
                    )
                    if ctrl_pressed and is_chrome_window:
                        print("检测到Ctrl键按下，不拦截滚轮事件(保留缩放功能)")
                        # 不拦截，让事件继续传递给Chrome处理缩放
                        return ctypes.windll.user32.CallNextHookEx(
                            self.wheel_hook_id,
                            nCode,
                            wParam,
                            ctypes.cast(lParam, ctypes.c_void_p),
                        )

                    # 只处理Chrome相关窗口且不是无法控制的特殊窗口
                    if is_chrome_window and not is_uncontrollable_window:
                        # 防止过于频繁触发
                        current_time = time.time()
                        if current_time - self.last_wheel_time < self.wheel_threshold:
                            # 阻止事件继续传递（返回1）
                            return 1

                        self.last_wheel_time = current_time

                        # 从MSLLHOOKSTRUCT结构体中获取滚轮增量
                        wheel_delta = ctypes.c_short(
                            lParam.contents.mouseData >> 16
                        ).value

                        # 标准化滚轮增量
                        normalized_delta = self.normalize_wheel_delta(wheel_delta)

                        # 只同步到其他同步窗口，不包括主窗口自身
                        windows_to_sync = self.sync_windows

                        # 获取鼠标位置
                        mouse_x, mouse_y = lParam.contents.pt.x, lParam.contents.pt.y
                        print(
                            f"拦截滚轮事件: 窗口={current_window}, 类型={'主窗口' if is_master_window else '主窗口插件' if is_master_plugin else '其他'}, wheel_delta={wheel_delta}"
                        )

                        # 如果是插件窗口，同步到其他窗口，但允许原始事件继续传递
                        if is_plugin_window:
                            # 向同步窗口发送模拟滚动
                            if windows_to_sync:
                                print(
                                    f"主窗口插件滚轮事件，同步到其他{len(windows_to_sync)}个窗口"
                                )
                                self.sync_specified_windows_scroll(
                                    normalized_delta, windows_to_sync
                                )

                            # 允许原始事件继续传递，这样插件窗口本身可以正常滚动
                            print("允许插件窗口原始滚轮事件继续传递")
                            return ctypes.windll.user32.CallNextHookEx(
                                self.wheel_hook_id,
                                nCode,
                                wParam,
                                ctypes.cast(lParam, ctypes.c_void_p),
                            )

                        # 主窗口：拦截原始事件，向同步窗口发送模拟滚动
                        else:
                            # 包括主窗口在内的所有窗口
                            all_windows = [self.master_window] + self.sync_windows
                            print(f"主窗口滚轮事件，同步到所有{len(all_windows)}个窗口")
                            self.sync_specified_windows_scroll(
                                normalized_delta, all_windows
                            )
                            # 拦截原始滚轮事件
                            return 1

                # 其他消息或非Chrome窗口，继续传递事件
                return ctypes.windll.user32.CallNextHookEx(
                    self.wheel_hook_id,
                    nCode,
                    wParam,
                    ctypes.cast(lParam, ctypes.c_void_p),
                )

            except Exception as e:
                print(f"滚轮钩子处理出错: {str(e)}")
                # 异常情况下继续传递事件
                return ctypes.windll.user32.CallNextHookEx(
                    self.wheel_hook_id,
                    nCode,
                    wParam,
                    ctypes.cast(lParam, ctypes.c_void_p),
                )

        # 创建钩子回调函数
        self.wheel_hook_proc = ctypes.WINFUNCTYPE(
            ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.POINTER(MSLLHOOKSTRUCT)
        )(wheel_proc)

        try:
            # 安装钩子 - 修复整数溢出错误，直接使用0而不是GetModuleHandle(None)
            self.wheel_hook_id = ctypes.windll.user32.SetWindowsHookExW(
                win32con.WH_MOUSE_LL,
                self.wheel_hook_proc,
                0,  # 直接使用0替代win32api.GetModuleHandle(None)
                0,
            )

            if not self.wheel_hook_id:
                error = ctypes.windll.kernel32.GetLastError()
                raise Exception(f"安装滚轮钩子失败，错误码: {error}")

        except Exception as e:
            print(f"安装滚轮钩子时出错: {str(e)}")
            # 确保标记为None，以便其他部分代码知道钩子未成功安装
            self.wheel_hook_id = None
            raise

    def unhook_wheel(self):
        """卸载滚轮钩子"""
        if self.wheel_hook_id:
            try:
                if ctypes.windll.user32.UnhookWindowsHookEx(self.wheel_hook_id):
                    print("已卸载滚轮钩子")
                else:
                    error = ctypes.windll.kernel32.GetLastError()
                    print(f"卸载滚轮钩子失败，错误码: {error}")
            except Exception as e:
                print(f"卸载滚轮钩子时出错: {str(e)}")
            finally:
                self.wheel_hook_id = None
                self.wheel_hook_proc = None

    def normalize_wheel_delta(self, delta, is_plugin=False):
        """标准化滚轮增量值 - 使用适中的缩放系数"""
        # 检查是否可能来自触控板（通常有小数或不规则值）
        abs_delta = abs(delta)

        # 使用适中的缩放系数，不区分窗口类型
        if abs_delta < 40:  # 很小的值，可能是精确触控板
            normalized = delta * 0.20  # 适中系数
        elif abs_delta < 80:  # 中等值
            normalized = delta * 0.25  # 适中系数
        else:  # 标准鼠标滚轮
            normalized = delta * 0.30  # 适中系数

        # 保持方向一致，但标准化大小
        direction = 1 if delta > 0 else -1
        # 标准增量设为中等值，从120降至50
        reduced_wheel_delta = int(self.standard_wheel_delta * 0.42)
        return direction * reduced_wheel_delta

    def sync_specified_windows_scroll(self, normalized_delta, window_list):
        """同步指定窗口列表的滚动 - 使用键盘事件模拟"""
        try:
            # 确定滚动方向和大小
            is_scroll_up = normalized_delta > 0
            abs_delta = abs(normalized_delta)

            # 遍历所有需要同步的窗口
            for hwnd in window_list:
                try:
                    if not win32gui.IsWindow(hwnd):
                        continue

                    # 根据滚动大小决定使用不同的按键组合
                    if abs_delta < 40:  # 小幅度滚动
                        key = win32con.VK_UP if is_scroll_up else win32con.VK_DOWN
                        repeat = max(1, min(int(abs_delta / 20), 2))

                        for _ in range(repeat):
                            win32gui.PostMessage(hwnd, win32con.WM_KEYDOWN, key, 0)
                            win32gui.PostMessage(hwnd, win32con.WM_KEYUP, key, 0)

                    elif abs_delta < 80:  # 中等幅度滚动
                        # 使用Page键
                        key = win32con.VK_PRIOR if is_scroll_up else win32con.VK_NEXT
                        win32gui.PostMessage(hwnd, win32con.WM_KEYDOWN, key, 0)
                        win32gui.PostMessage(hwnd, win32con.WM_KEYUP, key, 0)

                    else:  # 大幅度滚动
                        # 使用多个Page键
                        key = win32con.VK_PRIOR if is_scroll_up else win32con.VK_NEXT
                        repeat = min(int(abs_delta / 100) + 1, 2)

                        for _ in range(repeat):
                            win32gui.PostMessage(hwnd, win32con.WM_KEYDOWN, key, 0)
                            win32gui.PostMessage(hwnd, win32con.WM_KEYUP, key, 0)

                except Exception as e:
                    print(f"向窗口 {hwnd} 发送滚动事件失败: {str(e)}")

        except Exception as e:
            print(f"同步滚动出错: {str(e)}")

    def sync_all_windows_scroll(self, normalized_delta):
        """同步所有窗口的滚动 - 设置适中的滚动幅度"""
        # 遍历所有窗口，包括主窗口
        all_windows = [self.master_window] + self.sync_windows

        # 调用指定窗口滚动函数
        self.sync_specified_windows_scroll(normalized_delta, all_windows)

    def normalize_path(self, path):
        """标准化路径格式，统一使用正斜杠，便于比较"""
        if not path:
            return ""
        return os.path.normpath(path).lower().replace("\\", "/")

    def input_random_number(self):
        """在选中的窗口中输入随机数字"""
        try:
            # 获取选中的窗口
            selected_windows = []
            for item in self.window_list.get_children():
                if self.window_list.set(item, "select") == "√":
                    hwnd = int(self.window_list.item(item)["values"][-1])
                    selected_windows.append(hwnd)

            if not selected_windows:
                messagebox.showwarning("警告", "请先选择要操作的窗口！")
                return

            # 获取范围值
            min_str = self.random_min_value.get().strip()
            max_str = self.random_max_value.get().strip()

            if not min_str or not max_str:
                messagebox.showwarning("警告", "请输入有效的范围值！")
                return

            # 确定是整数还是小数
            is_float = "." in min_str or "." in max_str

            try:
                if is_float:
                    min_val = float(min_str)
                    max_val = float(max_str)
                    # 获取小数位数
                    decimal_places = max(
                        len(min_str.split(".")[-1]) if "." in min_str else 0,
                        len(max_str.split(".")[-1]) if "." in max_str else 0,
                    )
                    decimal_places = min(decimal_places, 10)  # 最多10位小数
                else:
                    min_val = int(min_str)
                    max_val = int(max_str)
            except ValueError:
                messagebox.showerror("错误", "请输入有效的数字范围！")
                return

            # 获取选项
            overwrite = self.random_overwrite.get()
            delayed = self.random_delayed.get()

            print(
                f"准备为{len(selected_windows)}个窗口生成随机数 (范围: {min_val}-{max_val}, 覆盖: {overwrite}, 延迟: {delayed})"
            )

            # 为每个选中的窗口输入随机数
            for hwnd in selected_windows:
                # 为每个窗口单独生成一个随机数
                if is_float:
                    # 生成随机小数，最多10位小数
                    random_number = round(
                        random.uniform(min_val, max_val), decimal_places
                    )
                    # 转为字符串，保留指定小数位
                    random_text = f"{random_number:.{decimal_places}f}"
                    # 去除尾部多余的0
                    if "." in random_text:
                        random_text = (
                            random_text.rstrip("0").rstrip(".")
                            if "." in random_text
                            else random_text
                        )
                else:
                    random_number = random.randint(min_val, max_val)
                    random_text = str(random_number)

                print(f"窗口 {hwnd} 的随机数: {random_text}")

                # 激活窗口
                try:
                    win32gui.SetForegroundWindow(hwnd)
                    time.sleep(0.1)  # 等待窗口获得焦点

                    # 如果选择覆盖原有内容，先全选文本
                    if overwrite:
                        keyboard.press_and_release("ctrl+a")
                        time.sleep(0.05)

                    # 输入随机数
                    if delayed:
                        # 模拟真人输入，逐字输入
                        for char in random_text:
                            keyboard.write(char)
                            # 随机延迟50-150毫秒
                            time.sleep(random.uniform(0.05, 0.15))
                    else:
                        # 直接输入整个字符串
                        keyboard.write(random_text)

                    time.sleep(0.2)  # 等待短暂时间再处理下一个窗口
                except Exception as e:
                    print(f"向窗口 {hwnd} 输入随机数时出错: {str(e)}")

        except Exception as e:
            messagebox.showerror("错误", f"输入随机数时出错: {str(e)}")

    def show_random_number_dialog(self):
        """显示随机数字输入对话框"""
        dialog = tk.Toplevel(self.root)
        dialog.title("随机数字输入")
        dialog.geometry("400x300")  # 增加高度从250到320
        dialog.transient(self.root)
        dialog.grab_set()
        dialog.resizable(False, False)

        # 设置图标
        try:
            icon_path = os.path.join(os.path.dirname(__file__), "app.ico")
            if os.path.exists(icon_path):
                dialog.iconbitmap(icon_path)
        except Exception as e:
            print(f"设置对话框图标失败: {str(e)}")

        # 居中显示
        self.center_window(dialog)

        # 主框架
        main_frame = ttk.Frame(dialog, padding=10)
        main_frame.pack(fill=tk.BOTH, expand=True)

        # 范围输入区域
        range_frame = ttk.LabelFrame(main_frame, text="数字范围", padding=10)
        range_frame.pack(fill=tk.X, pady=(0, 10))

        range_inner_frame = ttk.Frame(range_frame)
        range_inner_frame.pack(fill=tk.X)

        ttk.Label(range_inner_frame, text="最小值:").pack(side=tk.LEFT)
        min_entry = ttk.Entry(
            range_inner_frame, width=10, textvariable=self.random_min_value
        )
        min_entry.pack(side=tk.LEFT, padx=(5, 15))
        self.setup_right_click_menu(min_entry)

        ttk.Label(range_inner_frame, text="最大值:").pack(side=tk.LEFT)
        max_entry = ttk.Entry(
            range_inner_frame, width=10, textvariable=self.random_max_value
        )
        max_entry.pack(side=tk.LEFT, padx=5)
        self.setup_right_click_menu(max_entry)

        # 选项区域
        options_frame = ttk.LabelFrame(main_frame, text="输入选项", padding=10)
        options_frame.pack(fill=tk.X, pady=(0, 15))

        options_inner_frame = ttk.Frame(options_frame)
        options_inner_frame.pack(fill=tk.X)

        overwrite_var = tk.BooleanVar(value=True)

        overwrite_check = ttk.Checkbutton(
            options_inner_frame, text="覆盖原有内容", variable=self.random_overwrite
        )
        overwrite_check.pack(anchor=tk.W, pady=5)

        delayed_check = ttk.Checkbutton(
            options_inner_frame,
            text="模拟人工输入（逐字输入并添加延迟）",
            variable=self.random_delayed,
        )
        delayed_check.pack(anchor=tk.W)

        # 按钮区域
        buttons_frame = ttk.Frame(main_frame)
        buttons_frame.pack(fill=tk.X)

        ttk.Button(buttons_frame, text="取消", command=dialog.destroy, width=10).pack(
            side=tk.RIGHT, padx=5
        )

        ttk.Button(
            buttons_frame,
            text="开始输入",
            command=lambda: self.run_random_input(dialog),
            style="Accent.TButton",
            width=10,
        ).pack(side=tk.RIGHT, padx=5)

    def run_random_input(self, dialog):
        """执行随机数输入操作并关闭对话框"""
        dialog.destroy()
        self.input_random_number()

    def show_text_input_dialog(self):
        """显示指定文本输入对话框"""
        dialog = tk.Toplevel(self.root)
        dialog.title("指定文本输入")
        dialog.geometry("500x400")  # 增加高度从300到400
        dialog.transient(self.root)
        dialog.grab_set()
        dialog.resizable(False, False)

        # 设置图标
        try:
            icon_path = os.path.join(os.path.dirname(__file__), "app.ico")
            if os.path.exists(icon_path):
                dialog.iconbitmap(icon_path)
        except Exception as e:
            print(f"设置对话框图标失败: {str(e)}")

        # 居中显示
        self.center_window(dialog)

        # 主框架
        main_frame = ttk.Frame(dialog, padding=10)
        main_frame.pack(fill=tk.BOTH, expand=True)

        # 文本文件选择区域
        file_frame = ttk.LabelFrame(main_frame, text="文本文件", padding=10)
        file_frame.pack(fill=tk.X, pady=(0, 10))

        file_path_var = tk.StringVar()
        file_path_entry = ttk.Entry(file_frame, textvariable=file_path_var, width=40)
        file_path_entry.pack(side=tk.LEFT, fill=tk.X, expand=True, padx=(0, 5))
        self.setup_right_click_menu(file_path_entry)

        def browse_file():
            filepath = filedialog.askopenfilename(
                title="选择文本文件",
                filetypes=[("文本文件", "*.txt"), ("所有文件", "*.*")],
            )
            if filepath:
                file_path_var.set(filepath)
                # 预览文本文件内容
                try:
                    with open(filepath, "r", encoding="utf-8") as f:
                        preview_text = "\n".join(f.read().splitlines()[:10])
                        if len(f.read().splitlines()) > 10:
                            preview_text += "\n..."
                        preview.delete(1.0, tk.END)
                        preview.insert(tk.END, preview_text)
                except Exception as e:
                    messagebox.showerror("错误", f"读取文件失败: {str(e)}")

        ttk.Button(file_frame, text="浏览...", command=browse_file).pack(side=tk.RIGHT)

        # 文本预览区域
        preview_frame = ttk.LabelFrame(main_frame, text="文件内容预览", padding=10)
        preview_frame.pack(fill=tk.BOTH, expand=True, pady=(0, 10))

        preview = tk.Text(preview_frame, height=6, width=50, wrap=tk.WORD)
        preview.pack(side=tk.LEFT, fill=tk.BOTH, expand=True)

        preview_scrollbar = ttk.Scrollbar(
            preview_frame, orient=tk.VERTICAL, command=preview.yview
        )
        preview_scrollbar.pack(side=tk.RIGHT, fill=tk.Y)
        preview.configure(yscrollcommand=preview_scrollbar.set)

        # 输入方式选择
        input_method_frame = ttk.Frame(main_frame)
        input_method_frame.pack(fill=tk.X, pady=(0, 10))

        input_method = tk.StringVar(value="sequential")

        ttk.Radiobutton(
            input_method_frame,
            text="顺序输入",
            variable=input_method,
            value="sequential",
        ).pack(side=tk.LEFT, padx=(0, 15))

        ttk.Radiobutton(
            input_method_frame, text="随机输入", variable=input_method, value="random"
        ).pack(side=tk.LEFT)

        # 选项区域
        options_frame = ttk.Frame(main_frame)
        options_frame.pack(fill=tk.X, pady=(0, 10))

        overwrite_var = tk.BooleanVar(value=True)

        overwrite_check = ttk.Checkbutton(
            options_frame, text="覆盖原有内容", variable=overwrite_var
        )
        overwrite_check.pack(side=tk.LEFT, padx=(0, 10))

        # 按钮区域
        buttons_frame = ttk.Frame(main_frame)
        buttons_frame.pack(fill=tk.X)

        ttk.Button(buttons_frame, text="取消", command=dialog.destroy, width=10).pack(
            side=tk.RIGHT, padx=5
        )

        ttk.Button(
            buttons_frame,
            text="开始输入",
            command=lambda: self.execute_text_input(
                dialog,
                file_path_var.get(),
                input_method.get(),
                overwrite_var.get(),
                False,  # 永远不使用延迟输入
            ),
            style="Accent.TButton",
            width=10,
        ).pack(side=tk.RIGHT, padx=5)

    def execute_text_input(self, dialog, file_path, input_method, overwrite, delayed):
        """执行文本输入操作"""
        if not file_path:
            messagebox.showwarning("警告", "请选择文本文件！")
            return

        if not os.path.exists(file_path):
            messagebox.showerror("错误", "文件不存在！")
            return

        # 关闭对话框
        dialog.destroy()

        # 调用文本输入功能
        self.input_text_from_file(file_path, input_method, overwrite, delayed)

    def input_text_from_file(self, file_path, input_method, overwrite, delayed):
        """从文件输入文本到选中的窗口"""
        try:
            # 获取选中的窗口
            selected_windows = []
            for item in self.window_list.get_children():
                if self.window_list.set(item, "select") == "√":
                    hwnd = int(self.window_list.item(item)["values"][-1])
                    selected_windows.append(hwnd)

            if not selected_windows:
                messagebox.showwarning("警告", "请先选择要操作的窗口！")
                return

            # 读取文本文件
            try:
                with open(file_path, "r", encoding="utf-8") as f:
                    lines = [line.strip() for line in f.readlines() if line.strip()]
            except UnicodeDecodeError:
                # 尝试其它编码
                try:
                    with open(file_path, "r", encoding="gbk") as f:
                        lines = [line.strip() for line in f.readlines() if line.strip()]
                except Exception as e:
                    messagebox.showerror("错误", f"读取文件失败: {str(e)}")
                    return

            if not lines:
                messagebox.showwarning("警告", "文本文件为空！")
                return

            # 准备文本行
            if input_method == "random":
                # 为每个窗口随机选择一行
                random.shuffle(lines)
                # 如果窗口数量大于文本行数，循环使用
                if len(selected_windows) > len(lines):
                    lines = lines * (len(selected_windows) // len(lines) + 1)

            # 确保文本行至少与窗口数量一样多
            while len(lines) < len(selected_windows):
                lines.extend(lines)

            # 输入进度窗口
            progress_dialog = tk.Toplevel(self.root)
            progress_dialog.title("文本输入")
            progress_dialog.geometry("400x100")
            progress_dialog.transient(self.root)
            progress_dialog.grab_set()
            progress_dialog.resizable(False, False)

            # 设置图标
            try:
                icon_path = os.path.join(os.path.dirname(__file__), "app.ico")
                if os.path.exists(icon_path):
                    progress_dialog.iconbitmap(icon_path)
            except Exception as e:
                print(f"设置进度对话框图标失败: {str(e)}")

            self.center_window(progress_dialog)

            progress_label = ttk.Label(progress_dialog, text="正在准备输入...")
            progress_label.pack(pady=(20, 10))

            progress_bar = ttk.Progressbar(
                progress_dialog, mode="determinate", length=350
            )
            progress_bar.pack(pady=(0, 20))

            progress_dialog.update()

            try:
                # 为每个窗口输入文本
                for i, hwnd in enumerate(selected_windows):
                    # 更新进度
                    progress = int((i / len(selected_windows)) * 100)
                    progress_bar["value"] = progress
                    text_line = lines[i % len(lines)]
                    progress_label.config(
                        text=f"正在输入 ({i + 1}/{len(selected_windows)}): {text_line[:30]}..."
                    )
                    progress_dialog.update()

                    try:
                        # 激活窗口
                        win32gui.SetForegroundWindow(hwnd)
                        time.sleep(0.1)  # 等待窗口获得焦点

                        # 如果选择覆盖原有内容，先全选文本
                        if overwrite:
                            keyboard.press_and_release("ctrl+a")
                            time.sleep(0.05)

                        # 输入文本 - 直接输入整个字符串
                        keyboard.write(text_line)

                        time.sleep(0.2)  # 等待短暂时间再处理下一个窗口
                    except Exception as e:
                        print(f"向窗口 {hwnd} 输入文本时出错: {str(e)}")
                        continue

                # 完成后更新进度
                progress_bar["value"] = 100
                progress_label.config(text="输入完成！")
                progress_dialog.update()

                # 短暂延迟后关闭进度窗口
                self.root.after(1000, progress_dialog.destroy)

            except Exception as e:
                progress_dialog.destroy()
                messagebox.showerror("错误", f"输入文本时出错: {str(e)}")

        except Exception as e:
            messagebox.showerror("错误", f"操作失败: {str(e)}")

    def show_chrome_settings_tip(self):
        """显示Chrome后台运行设置提示"""
        tip_dialog = tk.Toplevel(self.root)
        tip_dialog.title("Chrome后台运行提示")
        tip_dialog.geometry("420x255")
        tip_dialog.transient(self.root)
        tip_dialog.grab_set()

        # 设置为模态对话框
        tip_dialog.focus_set()

        # 提示信息
        tip_text = '如果窗口关闭后，Chrome仍在后台运行（右下角系统托盘区域里有多个chrome图标），请批量在浏览器设置页面取消后台运行：\n\n1. 批量打开Chrome浏览器\n2. 在地址栏输入：chrome://settings/system，或者进入设置-系统\n3. 找到"关闭 Google Chrome 后继续运行后台应用"选项\n4. 关闭该选项'

        tip_label = ttk.Label(
            tip_dialog, text=tip_text, justify=tk.LEFT, wraplength=380
        )
        tip_label.pack(pady=20, padx=20)

        # 不再显示的选项
        dont_show_var = tk.BooleanVar(value=False)
        dont_show_check = ttk.Checkbutton(
            tip_dialog, text="下次不再显示", variable=dont_show_var
        )
        dont_show_check.pack(pady=10)

        # 确定按钮
        def on_ok():
            if dont_show_var.get():
                self.show_chrome_tip = False
                self.save_tip_settings()
            tip_dialog.destroy()

        ok_button = ttk.Button(
            tip_dialog, text="确定", command=on_ok, style="Accent.TButton"
        )
        ok_button.pack(pady=10)

        # 居中显示
        self.center_window(tip_dialog)

    def save_tip_settings(self):
        """保存提示设置到设置文件"""
        try:
            # 强制设置为False - 确保选择"下次不再显示"后永远不再显示
            self.show_chrome_tip = False

            # 直接设置当前实例的设置
            self.settings["show_chrome_tip"] = False

            # 立即保存到settings.json
            with open("settings.json", "w", encoding="utf-8") as f:
                json.dump(self.settings, f, ensure_ascii=False, indent=4)

            print(f"成功保存Chrome提示设置: show_chrome_tip = {self.show_chrome_tip}")

        except Exception as e:
            print(f"保存提示设置失败: {str(e)}")
            messagebox.showerror("设置保存失败", f"无法保存提示设置: {str(e)}")

    def load_settings(self) -> dict:
        # 加载设置
        settings = {}
        try:
            if os.path.exists("settings.json"):
                with open("settings.json", "r", encoding="utf-8") as f:
                    settings = json.load(f)
        except Exception as e:
            print(f"加载设置失败: {str(e)}")

        return settings

    def generate_color_icon(self, number, size=48):
        """生成带数字的彩色图标"""
        try:
            # 再次确保icon_dir存在
            if not self.icon_dir:
                self.icon_dir = os.path.join(
                    os.path.dirname(os.path.abspath(__file__)), "icons"
                )
            if not os.path.exists(self.icon_dir):
                try:
                    os.makedirs(self.icon_dir, exist_ok=True)
                except Exception as e:
                    print(f"创建图标目录失败: {str(e)}")
            random.seed(number)
            r = random.randint(30, 220)
            g = random.randint(30, 220)
            b = random.randint(30, 220)
            img = Image.new("RGBA", (size, size), color=(0, 0, 0, 0))
            draw = ImageDraw.Draw(img)
            bg_image_path = "./chrome.png"
            if os.path.exists(bg_image_path):
                bg_image = Image.open(bg_image_path).resize((size, size))
                img.paste(bg_image, (0, 0))
            ellipse_width = size * 0.85
            ellipse_height = size * 0.5
            ellipse_left = (size - ellipse_width) / 2
            ellipse_top = (size - ellipse_height) / 2 + 12
            ellipse_right = ellipse_left + ellipse_width
            ellipse_bottom = ellipse_top + ellipse_height
            draw.ellipse(
                (ellipse_left, ellipse_top, ellipse_right, ellipse_bottom),
                fill=(r, g, b, 255),
            )
            try:
                font_size = 24
                font_path = os.path.join(os.environ["WINDIR"], "Fonts", "Arial.ttf")
                if os.path.exists(font_path):
                    font = ImageFont.truetype(font_path, font_size)
                else:
                    font = ImageFont.truetype("Arial", font_size)
            except Exception as font_error:
                print(f"加载字体失败: {str(font_error)}")
                font = ImageFont.load_default()
                font_size = 24
            text = str(number)
            try:
                if hasattr(font, "getbbox"):
                    bbox = font.getbbox(text)
                    text_width = bbox[2] - bbox[0]
                    text_height = bbox[3] - bbox[1]
                elif hasattr(draw, "textsize"):
                    text_width, text_height = draw.textsize(text, font=font)
                else:
                    text_width = font_size * len(text) * 0.6
                    text_height = font_size
                x = (size - text_width) / 2
                y = (size - text_height) / 2 + 10
                text_color = (255, 255, 255, 255)
                draw.text((x, y), text, fill=text_color, font=font)
            except Exception as text_error:
                print(f"绘制文本失败: {str(text_error)}")
                draw.text(
                    (size // 4, size // 4), text, fill=(255, 255, 255, 255), font=font
                )
            icon_path = os.path.join(self.icon_dir, f"chrome_icon_{number}.ico")
            os.makedirs(os.path.dirname(icon_path), exist_ok=True)
            try:
                img.save(icon_path, format="ICO")
            except Exception as save_error:
                print(f"保存图标失败: {str(save_error)}")
                png_path = os.path.join(self.icon_dir, f"chrome_icon_{number}.png")
                img.save(png_path, format="PNG")
                icon_path = png_path
            return icon_path
        except Exception as e:
            print(f"生成图标失败: {str(e)}")
            return None

    def set_chrome_icon(self, hwnd, icon_path):
        """为Chrome窗口设置自定义图标"""
        try:
            big_icon = win32gui.LoadImage(
                0, icon_path, win32con.IMAGE_ICON, 32, 32, win32con.LR_LOADFROMFILE
            )
            small_icon = win32gui.LoadImage(
                0, icon_path, win32con.IMAGE_ICON, 16, 16, win32con.LR_LOADFROMFILE
            )
            win32gui.SendMessage(hwnd, win32con.WM_SETICON, win32con.ICON_BIG, big_icon)
            win32gui.SendMessage(
                hwnd, win32con.WM_SETICON, win32con.ICON_SMALL, small_icon
            )
            return True
        except Exception as e:
            print(f"设置图标失败: {str(e)}")
            return False

    def apply_icons_to_chrome_windows(self, hwnd_map):
        """为所有打开的Chrome窗口应用自定义图标"""
        for number, hwnd in hwnd_map.items():
            icon_path = self.generate_color_icon(number)
            if icon_path:
                self.set_chrome_icon(hwnd, icon_path)


if __name__ == "__main__":
    try:
        app = ChromeManager()
        app.run()
    except Exception as e:
        # 确保错误被显示出来
        import traceback

        error_message = f"程序出现错误:\n{str(e)}\n\n{traceback.format_exc()}"
        print(error_message)
        try:
            # 尝试使用tkinter显示错误
            from tkinter import messagebox

            messagebox.showerror("程序错误", error_message)
        except:
            # 如果tkinter也失败了，尝试命令行保持窗口
            print("\n按任意键退出...")
            input()
