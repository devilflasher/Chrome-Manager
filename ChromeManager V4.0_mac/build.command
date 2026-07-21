#!/bin/bash

# 获取脚本所在目录并切换过去
cd "$(dirname "$0")"

# 给 Go 单独的本地缓存目录，避免系统缓存权限或路径变化影响构建
mkdir -p ".build-cache/go"
export GOCACHE="$(pwd)/.build-cache/go"

# 设置 Terminal 标题
printf '\033]0;ChromeManager V4.0 macOS 构建工具\007'

# ANSI Colors
CYAN='\033[0;36m'
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m'

print_header() {
    echo -e "${CYAN}========================================================${NC}"
    echo -e "${CYAN}       ChromeManager V4.0 macOS 快捷构建工具${NC}"
    echo -e "${CYAN}========================================================${NC}"
    echo ""
}

info() {
    echo -e "${CYAN}[信息]${NC} $1"
}

success() {
    echo -e "${GREEN}[成功]${NC} $1"
}

warn() {
    echo -e "${YELLOW}[提示]${NC} $1"
}

error() {
    echo -e "${RED}[错误]${NC} $1"
}

pause_and_exit() {
    local code="$1"
    echo ""
    read -r -n 1 -p "按任意键退出..."
    echo ""
    exit "$code"
}

command_exists() {
    command -v "$1" >/dev/null 2>&1
}

is_admin_user() {
    id -Gn 2>/dev/null | tr ' ' '\n' | grep -qx "admin"
}

ensure_sudo_ready() {
    if ! command_exists sudo; then
        error "未检测到 sudo，无法安装 Homebrew。"
        return 1
    fi

    if ! is_admin_user; then
        error "当前用户不在 admin 管理员组，无法安装 Homebrew。"
        warn "请使用管理员账号登录后重试。"
        return 1
    fi

    if [ ! -t 0 ]; then
        error "当前会话不可交互，无法输入管理员密码。"
        warn "请在终端中运行本脚本，或先执行 sudo -v 后再重试。"
        return 1
    fi

    warn "即将请求管理员密码以安装 Homebrew..."
    if sudo -v; then
        success "管理员权限校验通过"
        return 0
    fi

    error "管理员权限校验失败，请确认密码后重试。"
    return 1
}

setup_homebrew_env() {
    if [ -x "/opt/homebrew/bin/brew" ]; then
        eval "$(/opt/homebrew/bin/brew shellenv)"
    elif [ -x "/usr/local/bin/brew" ]; then
        eval "$(/usr/local/bin/brew shellenv)"
    fi
}

set_ustc_brew_mirrors() {
    export HOMEBREW_BREW_GIT_REMOTE="https://mirrors.ustc.edu.cn/brew.git"
    export HOMEBREW_CORE_GIT_REMOTE="https://mirrors.ustc.edu.cn/homebrew-core.git"
    export HOMEBREW_BOTTLE_DOMAIN="https://mirrors.ustc.edu.cn/homebrew-bottles"
    export HOMEBREW_API_DOMAIN="https://mirrors.ustc.edu.cn/homebrew-bottles/api"
}

try_install_homebrew_official() {
    info "尝试通过 Homebrew 官方脚本安装..."
    /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
}

try_install_homebrew_ustc() {
    info "官方安装失败，尝试 USTC 镜像安装..."
    set_ustc_brew_mirrors
    /bin/bash -c "$(curl -fsSL https://mirrors.ustc.edu.cn/misc/brew-install.sh)"
}

print_homebrew_manual_guide() {
    echo ""
    warn "你可以复制下面命令手动重试（推荐逐行执行）："
    echo 'xcode-select --install'
    echo 'export HOMEBREW_BREW_GIT_REMOTE="https://mirrors.ustc.edu.cn/brew.git"'
    echo 'export HOMEBREW_CORE_GIT_REMOTE="https://mirrors.ustc.edu.cn/homebrew-core.git"'
    echo 'export HOMEBREW_BOTTLE_DOMAIN="https://mirrors.ustc.edu.cn/homebrew-bottles"'
    echo 'export HOMEBREW_API_DOMAIN="https://mirrors.ustc.edu.cn/homebrew-bottles/api"'
    echo '/bin/bash -c "$(curl -fsSL https://mirrors.ustc.edu.cn/misc/brew-install.sh)"'
    echo ""
    warn "镜像帮助文档: https://mirrors.ustc.edu.cn/help/brew.git.html"
    warn "官方安装文档: https://docs.brew.sh/Installation"
}

ensure_homebrew() {
    if command_exists brew; then
        success "Homebrew 已安装"
        return 0
    fi

    warn "未检测到 Homebrew，准备自动安装（可能需要输入管理员密码）..."
    warn "首次安装可能会弹出 Xcode Command Line Tools 安装提示，请按系统引导完成。"

    if ! ensure_sudo_ready; then
        print_homebrew_manual_guide
        pause_and_exit 1
    fi

    if try_install_homebrew_official; then
        setup_homebrew_env
    fi

    if command_exists brew; then
        success "Homebrew 安装完成（官方源）"
        return 0
    fi

    if try_install_homebrew_ustc; then
        setup_homebrew_env
    fi

    if command_exists brew; then
        success "Homebrew 安装完成（USTC 镜像）"
        return 0
    fi

    error "Homebrew 自动安装失败。"
    print_homebrew_manual_guide
    pause_and_exit 1
}

install_with_brew() {
    local cmd_name="$1"
    local formula="$2"
    local display_name="$3"

    if command_exists "$cmd_name"; then
        success "${display_name} 已安装"
        return 0
    fi

    info "正在安装 ${display_name}..."
    if brew install "$formula"; then
        setup_homebrew_env
        if command_exists "$cmd_name"; then
            success "${display_name} 安装完成"
            return 0
        fi
    fi

    error "${display_name} 安装失败，请稍后重试"
    pause_and_exit 1
}

install_task() {
    if command_exists task; then
        success "Task 已安装"
        return 0
    fi

    info "正在安装 Task..."
    if brew install go-task; then
        setup_homebrew_env
    else
        warn "尝试备用安装源 go-task/tap/go-task..."
        brew install go-task/tap/go-task
        setup_homebrew_env
    fi

    if command_exists task; then
        success "Task 安装完成"
    else
        error "Task 安装失败，请稍后重试"
        pause_and_exit 1
    fi
}

ensure_xcode_clt() {
    if xcode-select -p >/dev/null 2>&1; then
        success "Xcode Command Line Tools 已安装"
        return 0
    fi

    warn "未检测到 Xcode Command Line Tools。macOS CGO/打包需要它。"
    warn "即将打开系统安装器；安装完成后请重新运行本脚本。"
    if xcode-select --install >/dev/null 2>&1; then
        pause_and_exit 1
    fi

    error "无法启动 Xcode Command Line Tools 安装器。"
    warn "请手动执行：xcode-select --install"
    pause_and_exit 1
}

ensure_npm() {
    if command_exists npm; then
        success "npm 已安装"
        return 0
    fi

    error "未检测到 npm。npm 通常随 Node.js 一起安装，请检查 Node.js 安装和 PATH。"
    pause_and_exit 1
}

print_header

if ! command_exists curl; then
    error "未检测到 curl，无法自动安装依赖。请先安装 curl 后重试。"
    pause_and_exit 1
fi

ensure_xcode_clt
setup_homebrew_env
ensure_homebrew

install_with_brew go go "Go"
install_with_brew node node "Node.js"
ensure_npm
install_with_brew git git "Git"
install_task

if command_exists go; then
    GO_BIN_DIR="$(go env GOPATH 2>/dev/null)/bin"
    if [ -d "$GO_BIN_DIR" ]; then
        export PATH="$GO_BIN_DIR:$PATH"
    fi
fi

echo ""
success "基础构建环境已就绪，正在启动构建菜单..."
echo ""

# 运行构建工具
go run ./cmd/build-tool
EXIT_CODE=$?
if [ "$EXIT_CODE" -ne 0 ]; then
    error "构建工具运行失败，退出代码：$EXIT_CODE"
    pause_and_exit "$EXIT_CODE"
fi

exit 0
