import os
import sys
import shutil
import subprocess
import json
from typing import Dict


def check_and_install_packages(packages_with_versions: Dict[str, str]):
    """检查并安装指定版本的包"""
    print("正在检查并安装必要的依赖包...")

    for package, version in packages_with_versions.items():
        try:
            # 尝试导入包来检查是否已安装
            __import__(package.replace("-", "_").replace(".", "_"))
            print(f"✓ {package} 已安装")
        except ImportError:
            print(f"正在安装 {package}=={version}...")
            try:
                # 对于win11toast包特殊处理，忽略安装失败
                if package == "win11toast":
                    try:
                        subprocess.run(
                            [
                                sys.executable,
                                "-m",
                                "pip",
                                "install",
                                f"{package}=={version}",
                            ],
                            check=False,
                        )
                        print(f"✓ {package}=={version} 安装成功")
                    except:
                        print(
                            f"! {package} 安装失败，但这不会影响程序核心功能，继续打包"
                        )
                else:
                    # 其他包正常安装
                    subprocess.run(
                        [
                            sys.executable,
                            "-m",
                            "pip",
                            "install",
                            f"{package}=={version}",
                        ],
                        check=True,
                    )
                    print(f"✓ {package}=={version} 安装成功")
            except subprocess.CalledProcessError as e:
                print(f"! 无法安装 {package}=={version}: {str(e)}")
                print(f"  尝试安装不指定版本的 {package}...")
                try:
                    subprocess.run(
                        [sys.executable, "-m", "pip", "install", package], check=True
                    )
                    print(f"✓ {package} 安装成功")
                except subprocess.CalledProcessError as e2:
                    print(f"! 安装 {package} 失败: {str(e2)}")
                    if package == "win11toast":
                        print(f"  {package} 安装失败，但这不会影响程序核心功能")
                    else:
                        return False
    return True


def create_notification_alternative():
    """创建替代win11toast的通知实现文件"""
    print("正在创建通知替代实现...")

    # 在dist目录下创建一个替代的通知模块
    notification_dir = os.path.join("dist", "win11toast")
    if not os.path.exists(notification_dir):
        os.makedirs(notification_dir)

    # 创建__init__.py
    with open(
        os.path.join(notification_dir, "__init__.py"), "w", encoding="utf-8"
    ) as f:
        f.write('''
# 替代win11toast的简易实现
import ctypes
import threading
import time

def toast(title, message, **kwargs):
    """显示一个Windows通知，使用MessageBox替代"""
    def show():
        ctypes.windll.user32.MessageBoxW(0, message, title, 0x40)
    threading.Thread(target=show, daemon=True).start()
    
def notify(title, message, **kwargs):
    """显示一个Windows通知，使用MessageBox替代"""
    toast(title, message, **kwargs)
''')

    # 创建空的__pycache__目录以避免警告
    pycache_dir = os.path.join(notification_dir, "__pycache__")
    if not os.path.exists(pycache_dir):
        os.makedirs(pycache_dir)

    print("通知替代实现创建完成")


def get_installed_packages() -> Dict[str, str]:
    """获取当前已安装的包版本信息"""
    result = {}
    try:
        output = subprocess.check_output(
            [sys.executable, "-m", "pip", "freeze"]
        ).decode("utf-8")
        for line in output.split("\n"):
            if "==" in line:
                package, version = line.strip().split("==", 1)
                result[package] = version
    except Exception as e:
        print(f"获取已安装包信息时出错: {str(e)}")
    return result


def write_requirements_file(packages_with_versions: Dict[str, str]):
    """生成requirements.txt文件"""
    print("正在生成requirements.txt文件...")
    with open("requirements.txt", "w", encoding="utf-8") as f:
        for package, version in packages_with_versions.items():
            f.write(f"{package}=={version}\n")
    print("requirements.txt文件已生成")


def create_manifest_file():
    """创建应用程序清单文件，请求管理员权限"""
    print("正在创建应用程序清单文件...")
    manifest_content = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
    <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
        <security>
            <requestedPrivileges>
                <requestedExecutionLevel level="requireAdministrator" uiAccess="false"/>
            </requestedPrivileges>
        </security>
    </trustInfo>
</assembly>"""

    with open("app.manifest", "w", encoding="utf-8") as f:
        f.write(manifest_content)
    print("应用程序清单文件已创建")


def create_spec_file(sv_ttk_path: str):
    """创建PyInstaller spec文件"""
    print("正在创建PyInstaller spec文件...")

    # 这里列出所有需要的隐藏导入
    hidden_imports = [
        "sv_ttk",
        "keyboard",
        "mouse",
        "win32gui",
        "win32process",
        "win32con",
        "win32api",
        "win32com.client",
        "json",
        "requests",
        "math",
        "ctypes",
        "threading",
        "time",
        "webbrowser",
        "re",
        "traceback",
        "wmi",
        "pythoncom",
        "concurrent.futures",
        "winreg",
        "win11toast",  # 总是包含win11toast，即使安装失败也不影响
    ]

    # 创建spec文件内容
    spec_content = f"""# -*- mode: python ; coding: utf-8 -*-

a = Analysis(
    ['chrome_manager.py'],
    pathex=[],
    binaries=[],
    datas=[
        ('app.ico', '.'),
        (r'{sv_ttk_path}', 'sv_ttk'),
        ('README.md', '.'),
        ('chrome.png', '.'),
        ('settings.json', '.') if os.path.exists('settings.json') else None,
    ],
    hiddenimports={hidden_imports},
    hookspath=[],
    hooksconfig={{}},
    runtime_hooks=[],
    excludes=[],
    noarchive=False,
)

# 移除None值
a.datas = [x for x in a.datas if x is not None]

pyz = PYZ(a.pure)

exe = EXE(
    pyz,
    a.scripts,
    a.binaries,
    a.zipfiles,
    a.datas,
    [('app.manifest', 'app.manifest', 'DATA')],
    name='chrome_manager',
    debug=False,
    bootloader_ignore_signals=False,
    strip=False,
    upx=False,  # 禁用UPX压缩，防止被杀毒软件误报
    console=False,
    icon=['app.ico'],
    manifest="app.manifest",
    uac_admin=True,  # 添加UAC管理员请求
    uac_uiaccess=False,
    disable_windowed_traceback=False,
)
"""

    with open("chrome_manager.spec", "w", encoding="utf-8") as f:
        f.write(spec_content)
    print("PyInstaller spec文件已创建")


def find_sv_ttk_path():
    try:
        import sv_ttk

        return os.path.dirname(sv_ttk.__file__)
    except ImportError:
        print("未找到sv_ttk模块，请先安装")
        return None


def ensure_icon_exists():
    if not os.path.exists("app.ico"):
        print("警告: 未找到app.ico文件，将使用默认图标")
        # 可以考虑生成一个简单的图标或从网络下载一个
        try:
            # 尝试从Windows系统中复制一个默认图标
            shutil.copy(
                os.path.join(os.environ["SystemRoot"], "System32", "shell32.dll"),
                "temp_icon.dll",
            )
            subprocess.run(
                [
                    "powershell",
                    "-Command",
                    "(New-Object -ComObject Shell.Application).NameSpace(0).ParseName('temp_icon.dll').GetLink.GetIconLocation() | Out-File -FilePath 'app.ico'",
                ],
                check=True,
            )
            os.remove("temp_icon.dll")
        except Exception as e:
            print(f"生成默认图标失败: {str(e)}")
            print("将使用PyInstaller默认图标")


def ensure_settings_exists():
    """确保settings.json文件存在"""
    if not os.path.exists("settings.json"):
        print("正在创建默认settings.json文件...")
        default_settings = {
            "shortcut_path": "",
            "cache_dir": "",
            "icon_dir": "",
            "screen_selection": "",
            "sync_shortcut": None,
            "window_position": {"x": 100, "y": 100},
        }
        with open("settings.json", "w", encoding="utf-8") as f:
            json.dump(default_settings, f, ensure_ascii=False, indent=4)
        print("默认settings.json文件已创建")


def modify_chrome_manager_for_win11toast():
    """修改chrome_manager.py中的通知相关代码，添加简单的try-except处理"""
    print("检查chrome_manager.py是否需要修改通知实现...")

    try:
        with open("chrome_manager.py", "r", encoding="utf-8") as f:
            content = f.read()

        # 如果已经有错误处理，则不需要修改
        if (
            "try:" in content
            and "from win11toast import notify, toast" in content
            and "except ImportError:" in content
        ):
            print("chrome_manager.py已包含通知错误处理")
            return

        # 查找win11toast导入行
        if "from win11toast import notify, toast" in content:
            # 替换成带错误处理的版本
            original = "from win11toast import notify, toast"
            replacement = """# 添加通知错误处理
try:
    from win11toast import notify, toast
except ImportError:
    # 简单的空函数替代
    def toast(title, message, **kwargs):
        pass
    def notify(title, message, **kwargs):
        pass"""

            modified_content = content.replace(original, replacement)

            with open("chrome_manager.py", "w", encoding="utf-8") as f:
                f.write(modified_content)

            print("成功添加通知错误处理到chrome_manager.py")
        else:
            print("未找到win11toast导入行，跳过修改")
    except Exception as e:
        print(f"修改chrome_manager.py失败: {str(e)}")
        print("继续打包过程...")


def show_success_message():
    print("\n")
    print("─────────────────────────────────────────────────────")
    print("                                                     ")
    print("       ✨  Chrome多窗口管理器 V2.0 打包成功  ✨       ")
    print("                                                     ")
    print("  📦 可执行文件已生成到dist文件夹                      ")
    print("  🚀 双击chrome_manager.exe即可运行                   ")
    print("  🔑 首次打开程序时间会稍长，请耐心等待                 ")
    print("  🌐 使用中遇到问题，请查阅Github中的说明               ")
    print("                                                     ")
    print("─────────────────────────────────────────────────────")
    print("\n")


def show_failure_message(error_msg="未知错误"):
    print("\n")
    print("─────────────────────────────────────────────────────")
    print("                                                     ")
    print("       ❌  Chrome多窗口管理器 V2.0 打包失败  ❌       ")
    print("                                                     ")
    print("  ❗ 打包过程中出现错误                              ")
    print(f"  📋 错误信息: {error_msg[:35]}{'...' if len(error_msg) > 35 else ''}")
    print("  🔄 请查阅Github中的说明                              ")
    print("  💡 然后尝试重新运行打包程序                             ")
    print("                                                     ")
    print("─────────────────────────────────────────────────────")
    print("\n")


def build():
    """打包程序"""
    print("\n===== 开始打包Chrome多窗口管理器 V2.0 =====\n")

    # 修改chrome_manager.py添加简单的错误处理
    modify_chrome_manager_for_win11toast()

    # 需要的包和版本列表
    required_packages = {
        "pyinstaller": "6.12.0",
        "sv-ttk": "2.6.0",
        "keyboard": "0.13.5",
        "mouse": "0.7.1",
        "pywin32": "309",
        "wmi": "1.5.1",
        "requests": "2.32.3",
        "pillow": "11.1.0",
        "win11toast": "0.32",  # 包含win11toast但允许安装失败
    }

    # 获取当前已安装的包
    installed_packages = get_installed_packages()

    # 更新为实际安装的版本
    for package in required_packages:
        if package in installed_packages:
            required_packages[package] = installed_packages[package]

    # 检查并安装必要的包
    if not check_and_install_packages(required_packages):
        print("安装必要的包失败，尝试继续打包...")

    # 创建requirements.txt文件
    write_requirements_file(required_packages)

    # 确保其他必要文件存在
    ensure_icon_exists()
    ensure_settings_exists()

    # 创建清单文件
    create_manifest_file()

    # 查找sv_ttk路径
    sv_ttk_path = find_sv_ttk_path()
    if not sv_ttk_path:
        print("无法找到sv_ttk模块，打包终止")
        show_failure_message("未找到sv_ttk模块")
        return False

    # 创建spec文件
    create_spec_file(sv_ttk_path)

    # 清理旧的构建文件
    print("正在清理旧的构建文件...")
    if os.path.exists("build"):
        shutil.rmtree("build")
    if os.path.exists("dist"):
        shutil.rmtree("dist")

    # 运行PyInstaller
    print("\n正在打包程序...")
    try:
        # 当使用 .spec 文件时，不应在命令行传递 --clean 或 --noupx 等选项
        # 这些选项应在 spec 文件内配置，或由脚本本身处理（如清理目录）
        subprocess.run(["pyinstaller", "chrome_manager.spec"], check=True)
        print("\n打包完成！程序文件在dist文件夹中。")

        # 复制额外文件到dist目录
        if not os.path.exists(os.path.join("dist", "settings.json")) and os.path.exists(
            "settings.json"
        ):
            shutil.copy("settings.json", os.path.join("dist", "settings.json"))

        show_success_message()
        return True
    except subprocess.CalledProcessError as e:
        error_msg = str(e)
        show_failure_message(error_msg)
        return False


if __name__ == "__main__":
    try:
        success = build()
    except Exception as e:
        show_failure_message(str(e))
    finally:
        input("\n按回车键退出...")
