#!/bin/bash

# PostgreSQL 安装与配置脚本
# 支持 Ubuntu/Debian/CentOS/RHEL/Fedora/Arch Linux
# 用于 QMediaSync 应用的数据库环境配置

set -e  # 遇到错误立即退出

# 默认变量定义
DEFAULT_PG_VERSION="15"
DEFAULT_DB_HOST="localhost"
DEFAULT_DB_PORT="5432"
DEFAULT_DB_USER="qms"
DEFAULT_DB_PASSWORD="qms123456"
DEFAULT_DB_NAME="qms"

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 日志函数
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 检测系统类型
detect_os() {
    if [ -f /etc/os-release ]; then
        . /etc/os-release
        OS=$ID
        OS_VERSION=$VERSION_ID
    elif type lsb_release >/dev/null 2>&1; then
        OS=$(lsb_release -si | tr '[:upper:]' '[:lower:]')
        OS_VERSION=$(lsb_release -sr)
    else
        log_error "无法检测操作系统类型"
        exit 1
    fi
    
    log_info "检测到操作系统: $OS $OS_VERSION"
}

# 检查是否以 root 权限运行
check_root() {
    if [ "$EUID" -ne 0 ]; then
        log_error "请使用 root 权限运行此脚本"
        log_info "可以尝试: sudo $0"
        exit 1
    fi
}

# 检查 PostgreSQL 是否已安装
check_postgresql_installed() {
    if command -v psql >/dev/null 2>&1; then
        log_info "检测到 PostgreSQL 已安装"
        return 0
    else
        log_info "未检测到 PostgreSQL 安装"
        return 1
    fi
}

# 测试 PostgreSQL 连接
test_postgresql_connection() {
    local host="$1"
    local port="$2"
    local user="$3"
    local password="$4"
    
    log_info "测试 PostgreSQL 连接..."
    
    # 检查是否为本地连接
    if [ "$host" = "localhost" ] || [ "$host" = "127.0.0.1" ]; then
        # 本地连接使用peer认证，不需要密码
        if sudo -u postgres psql -p "$port" -d postgres -c "SELECT version();" >/dev/null 2>&1; then
            log_info "PostgreSQL 连接测试成功"
            return 0
        fi
    fi
    
    # 非本地连接使用密码认证
    export PGPASSWORD="$password"
    if psql -h "$host" -p "$port" -U "$user" -d postgres -c "SELECT version();" >/dev/null 2>&1; then
        log_info "PostgreSQL 连接测试成功"
        unset PGPASSWORD
        return 0
    else
        log_error "PostgreSQL 连接测试失败"
        unset PGPASSWORD
        return 1
    fi
}

# 在 Debian/Ubuntu 上安装
install_on_debian() {
    local version=$1
    
    log_info "更新软件包列表..."
    apt-get update -y
    
    if [ "$version" = "latest" ]; then
        log_info "安装最新版本的 PostgreSQL..."
        apt-get install -y postgresql postgresql-contrib
    else
        log_info "安装 PostgreSQL 版本: $version"
        # 对于特定版本，可能需要添加官方仓库
        if ! apt-cache show postgresql-$version >/dev/null 2>&1; then
            log_warn "特定版本 $version 未找到，尝试安装最新版本"
            apt-get install -y postgresql postgresql-contrib
        else
            apt-get install -y postgresql-$version postgresql-contrib-$version
        fi
    fi
    
    # 获取实际安装的版本
    local installed_version=$(pg_config --version | awk '{print $2}' | cut -d. -f1)
    log_info "成功安装 PostgreSQL $installed_version"
}

# 在 RedHat/CentOS/Fedora 上安装
install_on_redhat() {
    local version=$1
    
    # 安装 EPEL 仓库（CentOS/RHEL）
    if [ "$OS" = "centos" ] || [ "$OS" = "rhel" ] || [ "$OS" = "rocky" ] || [ "$OS" = "almalinux" ]; then
        log_info "安装 EPEL 仓库..."
        yum install -y epel-release
    fi
    
    # 对于较新的版本，可能需要添加 PostgreSQL 官方仓库
    if [ "$version" != "latest" ]; then
        log_warn "在 RHEL 系系统上，建议使用 'latest' 或手动添加 PostgreSQL 官方仓库"
    fi
    
    log_info "安装 PostgreSQL..."
    if command -v dnf >/dev/null 2>&1; then
        dnf install -y postgresql-server postgresql-contrib
    else
        yum install -y postgresql-server postgresql-contrib
    fi
    
    # 初始化数据库
    log_info "初始化 PostgreSQL 数据库..."
    postgresql-setup initdb
}

# 在 Arch Linux 上安装
install_on_arch() {
    local version=$1
    
    log_info "更新系统..."
    pacman -Syu --noconfirm
    
    log_info "安装 PostgreSQL..."
    pacman -S --noconfirm postgresql
    
    log_info "初始化数据库集群..."
    sudo -u postgres initdb -D /var/lib/postgres/data
}

# 安装 PostgreSQL
install_postgresql() {
    local version=${1:-$DEFAULT_PG_VERSION}
    
    log_info "开始安装 PostgreSQL..."
    
    case $OS in
        ubuntu|debian)
            install_on_debian "$version"
            ;;
        centos|rhel|fedora|rocky|almalinux)
            install_on_redhat "$version"
            ;;
        arch|manjaro)
            install_on_arch "$version"
            ;;
        *)
            log_error "不支持的操作系统: $OS"
            exit 1
            ;;
    esac
    
    log_info "PostgreSQL 安装完成"
}

# 启动并启用 PostgreSQL 服务
setup_postgresql_service() {
    log_info "启动 PostgreSQL 服务..."
    
    case $OS in
        ubuntu|debian|arch|manjaro|centos|rhel|fedora|rocky|almalinux)
            systemctl enable postgresql
            systemctl start postgresql
            ;;
        *)
            log_error "不支持的操作系统服务管理: $OS"
            exit 1
            ;;
    esac
    
    # 检查服务状态
    if systemctl is-active --quiet postgresql; then
        log_info "PostgreSQL 服务正在运行"
    else
        log_error "PostgreSQL 服务启动失败"
        exit 1
    fi
}

# 数据库初始化与配置
initialize_database() {
    local host="$1"
    local port="$2"
    local admin_user="$3"
    local db_user="$4"
    local db_password="$5"
    local db_name="$6"
    
    log_info "开始数据库初始化..."
    log_info "连接信息:"
    log_info "  主机: ${host}"
    log_info "  端口: ${port}"
    log_info "  管理员用户: ${admin_user}"
    log_info "  数据库用户: ${db_user}"
    log_info "  数据库名称: ${db_name}"
    
    # 检查是否为本地连接
    if [ "$host" = "localhost" ] || [ "$host" = "127.0.0.1" ]; then
        # 本地连接使用peer认证，直接使用sudo -u postgres执行
        
        # 创建数据库用户
        log_info "创建数据库用户..."
        sudo -u postgres psql -p "$port" -d postgres -c "CREATE USER $db_user WITH PASSWORD '$db_password';" 2>/dev/null || log_warn "用户 $db_user 可能已存在"
        
        # 创建数据库
        log_info "创建数据库..."
        sudo -u postgres psql -p "$port" -d postgres -c "CREATE DATABASE $db_name OWNER $db_user;" 2>/dev/null || log_warn "数据库 $db_name 可能已存在"
        
        # 授予权限
        log_info "授予用户权限..."
        sudo -u postgres psql -p "$port" -d postgres -c "GRANT ALL PRIVILEGES ON DATABASE $db_name TO $db_user;"
        sudo -u postgres psql -p "$port" -d $db_name -c "GRANT ALL ON SCHEMA public TO $db_user;"
    else
        # 非本地连接使用密码认证
        export PGPASSWORD="$db_password"
        
        # 创建数据库用户
        log_info "创建数据库用户..."
        psql -h "$host" -p "$port" -U "$admin_user" -d postgres -c "CREATE USER $db_user WITH PASSWORD '$db_password';" 2>/dev/null || log_warn "用户 $db_user 可能已存在"
        
        # 创建数据库
        log_info "创建数据库..."
        psql -h "$host" -p "$port" -U "$admin_user" -d postgres -c "CREATE DATABASE $db_name OWNER $db_user;" 2>/dev/null || log_warn "数据库 $db_name 可能已存在"
        
        # 授予权限
        log_info "授予用户权限..."
        psql -h "$host" -p "$port" -U "$admin_user" -d postgres -c "GRANT ALL PRIVILEGES ON DATABASE $db_name TO $db_user;"
        psql -h "$host" -p "$port" -U "$admin_user" -d $db_name -c "GRANT ALL ON SCHEMA public TO $db_user;"
        
        # 清理环境变量
        unset PGPASSWORD
    fi
    
    log_info "数据库初始化完成"
}

# 检查 systemctl 是否可用
check_systemctl() {
    if ! command -v systemctl >/dev/null 2>&1; then
        log_error "systemctl 不可用，此系统不支持 systemd 服务管理"
        return 1
    fi
    return 0
}

# 创建 QMediaSync 服务文件
create_qmediasync_service() {
    local service_file="/etc/systemd/system/qmediasync.service"
    
    # 检查服务文件是否已存在
    if [ -f "$service_file" ]; then
        log_warn "QMediaSync 服务文件已存在，将进行更新"
    fi
    
    # 获取 QMediaSync 可执行文件路径
    local qmediasync_path="$(pwd)/QMediaSync"
    if [ ! -f "$qmediasync_path" ]; then
        log_error "QMediaSync 可执行文件不存在，请确保在 QMediaSync 目录下运行此脚本"
        return 1
    fi
    
    # 创建服务文件
    cat > "$service_file" << EOF
[Unit]
Description=QMediaSync Media Synchronization Service
After=network.target postgresql.service

[Service]
Type=simple
User=$SUDO_USER
Group=$SUDO_USER
WorkingDirectory=$(pwd)
ExecStart=$qmediasync_path
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF
    
    if [ $? -eq 0 ]; then
        log_info "QMediaSync 服务文件创建成功"
        return 0
    else
        log_error "QMediaSync 服务文件创建失败"
        return 1
    fi
}

# 安装 QMediaSync 服务
install_qmediasync_service() {
    if ! check_systemctl; then
        return 1
    fi
    
    log_info "安装 QMediaSync 服务并设置开机启动..."
    
    # 创建服务文件
    if ! create_qmediasync_service; then
        return 1
    fi
    
    # 重新加载 systemd 配置
    if systemctl daemon-reload >/dev/null 2>&1; then
        log_info "systemd 配置已重新加载"
    else
        log_error "无法重新加载 systemd 配置"
        return 1
    fi
    
    # 设置开机启动
    if systemctl enable qmediasync >/dev/null 2>&1; then
        log_info "QMediaSync 服务已设置开机启动"
    else
        log_error "无法设置 QMediaSync 服务开机启动"
        return 1
    fi
    
    # 启动服务
    if systemctl start qmediasync >/dev/null 2>&1; then
        log_info "QMediaSync 服务已启动"
    else
        log_error "无法启动 QMediaSync 服务"
        return 1
    fi
    
    # 检查服务状态
    if systemctl is-active --quiet qmediasync; then
        log_info "QMediaSync 服务正在运行"
        return 0
    else
        log_error "QMediaSync 服务未正常运行"
        log_info "查看服务状态: sudo systemctl status qmediasync"
        return 1
    fi
}

# 删除 QMediaSync 服务
remove_qmediasync_service() {
    if ! check_systemctl; then
        return 1
    fi
    
    log_info "删除 QMediaSync 服务..."
    
    # 停止服务
    if systemctl stop qmediasync >/dev/null 2>&1; then
        log_info "QMediaSync 服务已停止"
    else
        log_warn "无法停止 QMediaSync 服务（可能未运行）"
    fi
    
    # 禁用服务
    if systemctl disable qmediasync >/dev/null 2>&1; then
        log_info "QMediaSync 服务已禁用开机启动"
    else
        log_warn "无法禁用 QMediaSync 服务开机启动"
    fi
    
    # 删除服务文件
    local service_file="/etc/systemd/system/qmediasync.service"
    if [ -f "$service_file" ]; then
        if rm "$service_file" >/dev/null 2>&1; then
            log_info "QMediaSync 服务文件已删除"
        else
            log_warn "无法删除 QMediaSync 服务文件"
        fi
    fi
    
    # 重新加载 systemd 配置
    systemctl daemon-reload >/dev/null 2>&1
    
    log_info "QMediaSync 服务删除完成"
    return 0
}

# 显示使用说明
usage() {
    echo "用法: $0 [选项]"
    echo "选项:"
    echo "  -v version    指定 PostgreSQL 版本 (默认: $DEFAULT_PG_VERSION)"
    echo "  -h host       指定 PostgreSQL 主机地址 (默认: $DEFAULT_DB_HOST)"
    echo "  -P port       指定 PostgreSQL 端口 (默认: $DEFAULT_DB_PORT)"
    echo "  -u user       指定 QMediaSync 数据库用户 (默认: $DEFAULT_DB_USER)"
    echo "  -U admin      指定 PostgreSQL 管理员用户 (默认: postgres)"
    echo "  -p password   设置 QMediaSync 数据库密码 (默认: $DEFAULT_DB_PASSWORD)"
    echo "  -n name       指定 QMediaSync 数据库名称 (默认: $DEFAULT_DB_NAME)"
    echo "  -i            安装 QMediaSync 为系统服务并设置开机启动"
    echo "  -r            删除 QMediaSync 系统服务"
    echo "  -?            显示此帮助信息"
    echo ""
    echo "示例:"
    echo "  $0                          # 安装默认版本 PostgreSQL 并初始化数据库"
    echo "  $0 -v 14 -p mypass          # 安装 PostgreSQL 14 并设置自定义密码"
    echo "  $0 -h localhost -u qms -p mypass  # 初始化数据库和用户（PostgreSQL 已安装）"
    echo "  $0 -i                       # 安装并配置 PostgreSQL，然后设置 QMediaSync 服务"
    echo "  $0 -r                       # 删除 QMediaSync 服务"
    echo ""
    echo "注意:"
    echo "  - 安装 PostgreSQL 时需要 root 权限"
    echo "  - 如果 PostgreSQL 已安装，脚本将检查连接并初始化数据库和用户"
    echo "  - 完成后通过首次配置向导或 config/config.yaml 指定数据库连接"
    echo "  - 服务管理功能需要 systemd 支持"
    echo "  - 请确保在 QMediaSync 应用程序目录下运行服务相关命令"
}

# 主函数
main() {
    log_info "QMediaSync PostgreSQL 安装与配置脚本"
    
    # 解析命令行参数
    local pg_version="$DEFAULT_PG_VERSION"
    local db_host="$DEFAULT_DB_HOST"
    local db_port="$DEFAULT_DB_PORT"
    local db_user="$DEFAULT_DB_USER"
    local admin_user="postgres"
    local db_password="$DEFAULT_DB_PASSWORD"
    local db_name="$DEFAULT_DB_NAME"
    local install_service=false
    local remove_service=false
    
    while getopts "v:h:P:u:U:p:n:ir?" opt; do
        case $opt in
            v) pg_version="$OPTARG" ;;
            h) db_host="$OPTARG" ;;
            P) db_port="$OPTARG" ;;
            u) db_user="$OPTARG" ;;
            U) admin_user="$OPTARG" ;;
            p) db_password="$OPTARG" ;;
            n) db_name="$OPTARG" ;;
            i) install_service=true ;;
            r) remove_service=true ;;
            ?) usage; exit 0 ;;
            *) usage; exit 1 ;;
        esac
    done
    
    # 检查服务管理参数冲突
    if [ "$install_service" = "true" ] && [ "$remove_service" = "true" ]; then
        log_error "不能同时使用 -i 和 -r 参数"
        exit 1
    fi
    
    # 处理服务管理操作
    if [ "$remove_service" = "true" ]; then
        if remove_qmediasync_service; then
            log_info "QMediaSync 服务删除完成"
        else
            log_error "QMediaSync 服务删除失败"
        fi
        exit 0
    fi
    
    # 检查 root 权限
    check_root
    
    # 检测操作系统
    detect_os
    
    # 检查 PostgreSQL 是否已安装
    local installed=false
    if check_postgresql_installed; then
        installed=true
        log_info "PostgreSQL 已安装，将检查连接并初始化数据库和用户"
    fi
    
    # 如果 PostgreSQL 未安装，执行安装流程
    if [ "$installed" = "false" ]; then
        # 安装 PostgreSQL
        install_postgresql "$pg_version"
        
        # 启动并启用服务
        setup_postgresql_service
    fi
    
    # 测试数据库连接
    log_info "测试数据库连接..."
    if ! test_postgresql_connection "$db_host" "$db_port" "$admin_user" "$db_password"; then
        log_error "无法连接到 PostgreSQL 数据库"
        log_info "请检查连接信息和数据库状态"
        exit 1
    fi
    
    # 初始化数据库
    initialize_database "$db_host" "$db_port" "$admin_user" "$db_user" "$db_password" "$db_name"
    
    # 安装 QMediaSync 服务（如果需要）
    if [ "$install_service" = "true" ]; then
        if install_qmediasync_service; then
            log_info "QMediaSync 服务安装完成"
        else
            log_error "QMediaSync 服务安装失败"
            exit 1
        fi
    fi
    
    # 显示完成信息
    log_info "\n=== 配置完成 ==="
    log_info "PostgreSQL 数据库和用户已初始化"
    log_info "请通过首次配置向导或 config/config.yaml 填写以下数据库连接信息:"
    log_info "  主机: ${db_host}"
    log_info "  端口: ${db_port}"
    log_info "  用户: ${db_user}"
    log_info "  数据库: ${db_name}"
    log_info ""
    log_info "连接数据库命令: psql -h ${db_host} -p ${db_port} -U ${db_user} -d ${db_name}"
    
    if [ "$install_service" = "true" ]; then
        log_info "\nQMediaSync 服务已安装并启动"
        log_info "查看服务状态: systemctl status qmediasync"
        log_info "停止服务: systemctl stop qmediasync"
        log_info "重启服务: systemctl restart qmediasync"
    fi
    
    log_info "\n所有操作已完成！"
}

# 运行主函数
main "$@"
