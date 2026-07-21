$ErrorActionPreference = "Continue"

try {
    [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
    $OutputEncoding = [System.Text.Encoding]::UTF8
} catch {
}

Set-Location -LiteralPath $PSScriptRoot

$goCacheDir = Join-Path $PSScriptRoot ".build-cache\go"
New-Item -ItemType Directory -Force -Path $goCacheDir | Out-Null
$env:GOCACHE = $goCacheDir

function Write-Header {
    Clear-Host
    Write-Host "========================================================" -ForegroundColor Cyan
    Write-Host "       ChromeManager V4.0 启动引导程序 (Windows)" -ForegroundColor Cyan
    Write-Host "========================================================" -ForegroundColor Cyan
    Write-Host ""
}

function Pause-AndExit([int]$Code) {
    Write-Host ""
    Read-Host "按 Enter 键退出"
    exit $Code
}

function Test-CommandRun([string]$Command, [string[]]$Arguments) {
    try {
        & $Command @Arguments *> $null
        return $LASTEXITCODE -eq 0
    } catch {
        return $false
    }
}

function Get-NodeVersion {
    try {
        $version = (& node -v 2>$null).Trim()
        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($version)) {
            return $null
        }
        return $version
    } catch {
        return $null
    }
}

function Test-NodeVersion([string]$Version) {
    if ([string]::IsNullOrWhiteSpace($Version)) {
        return $false
    }

    $clean = $Version.TrimStart("v")
    $parts = $clean.Split(".")
    if ($parts.Count -lt 2) {
        return $false
    }

    $major = 0
    $minor = 0
    if (-not [int]::TryParse($parts[0], [ref]$major)) {
        return $false
    }
    if (-not [int]::TryParse($parts[1], [ref]$minor)) {
        return $false
    }

    return (($major -eq 20 -and $minor -ge 19) -or ($major -eq 22 -and $minor -ge 12) -or $major -gt 22)
}

function Test-WebView2Runtime {
    $guid = "{F3017226-FE2A-4295-8BDF-00C3A9C6A7}"
    $registryPaths = @(
        "HKCU:\SOFTWARE\Microsoft\EdgeUpdate\Clients\$guid",
        "HKLM:\SOFTWARE\Microsoft\EdgeUpdate\Clients\$guid",
        "HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\$guid"
    )

    foreach ($path in $registryPaths) {
        try {
            $pv = (Get-ItemProperty -LiteralPath $path -Name "pv" -ErrorAction Stop).pv
            if (-not [string]::IsNullOrWhiteSpace($pv) -and $pv -ne "0.0.0.0") {
                return $true
            }
        } catch {
        }
    }

    $bases = @($env:ProgramFiles, ${env:ProgramFiles(x86)}, $env:LocalAppData)
    foreach ($base in $bases) {
        if ([string]::IsNullOrWhiteSpace($base)) {
            continue
        }
        $patterns = @(
            (Join-Path $base "Microsoft\EdgeWebView\Application\*\msedgewebview2.exe"),
            (Join-Path $base "Programs\Microsoft\EdgeWebView\Application\*\msedgewebview2.exe")
        )
        foreach ($pattern in $patterns) {
            if (Get-ChildItem -Path $pattern -ErrorAction SilentlyContinue) {
                return $true
            }
        }
    }

    return $false
}

function Invoke-Winget([string]$Name, [string[]]$Arguments, [string]$FailHint) {
    Write-Host $Name -ForegroundColor Yellow
    & winget @Arguments
    if ($LASTEXITCODE -ne 0) {
        Write-Host "[ERROR] $Name 失败，错误代码：$LASTEXITCODE" -ForegroundColor Red
        Write-Host $FailHint -ForegroundColor Red
        return $false
    }
    Write-Host "[INFO] $Name 完成。" -ForegroundColor Green
    return $true
}

function Add-GitToPath {
    $candidates = @(
        "$env:ProgramFiles\Git\cmd",
        "${env:ProgramFiles(x86)}\Git\cmd",
        "$env:LocalAppData\Programs\Git\cmd"
    )

    foreach ($path in $candidates) {
        if ((Test-Path -LiteralPath (Join-Path $path "git.exe")) -and ($env:Path -notlike "*$path*")) {
            $env:Path = "$path;$env:Path"
        }
    }
}

function Show-ManualInstall([bool]$MissingGo, [bool]$MissingNode, [bool]$InvalidNode, [bool]$MissingNpm, [bool]$MissingGit, [bool]$MissingWebView2) {
    Write-Host ""
    Write-Host "[WARN] 已取消自动安装或安装失败。" -ForegroundColor Yellow
    Write-Host "请手动安装缺失的环境："
    if ($MissingGo) {
        Write-Host "  - Go: https://golang.google.cn/dl"
    }
    if ($MissingNode) {
        Write-Host "  - Node.js: https://nodejs.org/"
    }
    if ($InvalidNode) {
        Write-Host "  - Node.js: 当前版本过低，请安装 20.19+ 或 22.12+：https://nodejs.org/"
    }
    if ($MissingNpm) {
        Write-Host "  - npm: 通常随 Node.js 一起安装，请检查 Node.js 安装和 PATH"
    }
    if ($MissingGit) {
        Write-Host "  - Git: https://git-scm.com/download/win"
    }
    if ($MissingWebView2) {
        Write-Host "  - Microsoft Edge WebView2 Runtime: https://developer.microsoft.com/microsoft-edge/webview2/"
        Write-Host "    说明：这是运行 ChromeManager.exe 的界面依赖。如果把 exe 拷贝到其他干净电脑，也需要在那台电脑安装。"
    }
}

function Start-BuildTool {
    Write-Host "[INFO] 环境检测通过。正在启动构建工具..." -ForegroundColor Green
    Write-Host "--------------------------------------------------------"

    if (-not (Test-Path -LiteralPath "cmd\build-tool\main.go")) {
        Write-Host "[ERROR] 找不到构建工具源码 (cmd\build-tool\main.go)" -ForegroundColor Red
        Pause-AndExit 1
    }

    & go run .\cmd\build-tool
    if ($LASTEXITCODE -ne 0) {
        Write-Host "[ERROR] 构建工具运行出错，错误代码：$LASTEXITCODE" -ForegroundColor Red
        Pause-AndExit 1
    }
    exit 0
}

try {
    Write-Header

    $missingGo = -not (Test-CommandRun "go" @("version"))
    $nodeVersion = Get-NodeVersion
    $missingNode = [string]::IsNullOrWhiteSpace($nodeVersion)
    $invalidNode = (-not $missingNode) -and (-not (Test-NodeVersion $nodeVersion))
    $missingNpm = -not (Test-CommandRun "npm.cmd" @("-v"))
    $missingGit = -not (Test-CommandRun "git" @("--version"))
    $missingWebView2 = -not (Test-WebView2Runtime)

    if (-not $missingGo -and -not $missingNode -and -not $invalidNode -and -not $missingNpm -and -not $missingGit -and -not $missingWebView2) {
        Start-BuildTool
    }

    Write-Host "[WARN] 检测到部分开发环境缺失：" -ForegroundColor Yellow
    if ($missingGo) {
        Write-Host "   - Go 语言环境"
    }
    if ($missingNode) {
        Write-Host "   - Node.js 环境"
    }
    if ($invalidNode) {
        Write-Host "   - Node.js 版本过低（当前：$nodeVersion，需要 20.19+ 或 22.12+）"
    }
    if ($missingNpm) {
        Write-Host "   - npm 包管理器"
    }
    if ($missingGit) {
        Write-Host "   - Git 版本控制工具（安装 Wails CLI 需要）"
    }
    if ($missingWebView2) {
        Write-Host "   - Microsoft Edge WebView2 Runtime（运行程序界面需要）"
    }
    Write-Host ""

    Write-Host "是否尝试使用 Winget 自动安装缺失的依赖？"
    Write-Host "  Y - 是，自动安装"
    Write-Host "  N - 否，手动安装或退出"
    $answer = Read-Host "请选择 [Y/N]"

    if ($answer -notmatch "^[Yy]$") {
        Show-ManualInstall $missingGo $missingNode $invalidNode $missingNpm $missingGit $missingWebView2
        Pause-AndExit 1
    }

    if (-not (Get-Command winget -ErrorAction SilentlyContinue)) {
        Write-Host "[ERROR] 未检测到 Winget，无法自动安装环境。" -ForegroundColor Red
        Show-ManualInstall $missingGo $missingNode $invalidNode $missingNpm $missingGit $missingWebView2
        Pause-AndExit 1
    }

    $wingetHint = "[ERROR] 请查看上方 Winget 输出。常见原因：网络不可用、Winget 源不可用或权限不足。"
    $nodeUpdateHint = "[ERROR] 请查看上方 Winget 输出。常见原因：网络不可用、Winget 源不可用、Node.js 不是通过 Winget 安装或权限不足。"

    if ($missingGo) {
        if (-not (Invoke-Winget "[Step] 安装 Go" @("install", "GoLang.Go", "--accept-source-agreements", "--accept-package-agreements") $wingetHint)) {
            Show-ManualInstall $missingGo $missingNode $invalidNode $missingNpm $missingGit $missingWebView2
            Pause-AndExit 1
        }
    }

    if ($missingGit) {
        if (-not (Invoke-Winget "[Step] 安装 Git" @("install", "Git.Git", "--accept-source-agreements", "--accept-package-agreements") $wingetHint)) {
            Show-ManualInstall $missingGo $missingNode $invalidNode $missingNpm $missingGit $missingWebView2
            Pause-AndExit 1
        }
        Add-GitToPath
    }

    if ($missingNode) {
        if (-not (Invoke-Winget "[Step] 安装 Node.js" @("install", "OpenJS.NodeJS.LTS", "--accept-source-agreements", "--accept-package-agreements") $wingetHint)) {
            Show-ManualInstall $missingGo $missingNode $invalidNode $missingNpm $missingGit $missingWebView2
            Pause-AndExit 1
        }
    }

    if ($invalidNode) {
        if (-not (Invoke-Winget "[Step] 更新 Node.js LTS" @("upgrade", "OpenJS.NodeJS.LTS", "--accept-source-agreements", "--accept-package-agreements") $nodeUpdateHint)) {
            Show-ManualInstall $missingGo $missingNode $invalidNode $missingNpm $missingGit $missingWebView2
            Pause-AndExit 1
        }
    }

    if ($missingNpm -and -not $missingNode) {
        Write-Host "[WARN] 已检测到 Node.js，但未检测到 npm。请重新安装 Node.js LTS 或检查 PATH。" -ForegroundColor Yellow
        Show-ManualInstall $missingGo $missingNode $invalidNode $missingNpm $missingGit $missingWebView2
        Pause-AndExit 1
    }

    if ($missingWebView2) {
        if (-not (Invoke-Winget "[Step] 安装 Microsoft Edge WebView2 Runtime" @("install", "Microsoft.EdgeWebView2Runtime", "--accept-source-agreements", "--accept-package-agreements") $wingetHint)) {
            Show-ManualInstall $missingGo $missingNode $invalidNode $missingNpm $missingGit $missingWebView2
            Pause-AndExit 1
        }
    }

    if ($missingWebView2 -and -not (Test-WebView2Runtime)) {
        Write-Host "[WARN] WebView2 Runtime 安装后仍未检测到。请重启此脚本，或手动安装后再运行生成的程序。" -ForegroundColor Yellow
        Show-ManualInstall $false $false $false $false $false $true
        Pause-AndExit 1
    }

    Write-Host ""
    Write-Host "[SUCCESS] 环境安装完成！" -ForegroundColor Green
    Write-Host "[!] 请注意：为了使环境变量生效，您可能需要重启此脚本。"
    Pause-AndExit 0
} catch {
    Write-Host "[ERROR] 构建引导脚本异常：$($_.Exception.Message)" -ForegroundColor Red
    Pause-AndExit 1
}

