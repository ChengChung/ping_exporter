#!/bin/bash
#
# ping_exporter systemd 服务安装脚本（简化版）
# 交互式配置，仅询问必要参数
#

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 打印带颜色的消息
print_info() {
    echo -e "${BLUE}ℹ${NC} $*"
}

print_success() {
    echo -e "${GREEN}✓${NC} $*"
}

print_error() {
    echo -e "${RED}✗${NC} $*"
}

print_warning() {
    echo -e "${YELLOW}⚠${NC} $*"
}

# 打印标题
print_title() {
    echo -e "\n${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BLUE}$*${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n"
}

# 检查是否为 root
check_root() {
    if [[ $EUID -ne 0 ]]; then
        print_error "此脚本必须以 root 身份运行"
        exit 1
    fi
}

# 询问用户输入
ask_input() {
    local prompt=$1
    local default=$2
    local input

    if [[ -n "$default" ]]; then
        echo -ne "${BLUE}?${NC} $prompt [$default]: "
    else
        echo -ne "${BLUE}?${NC} $prompt: "
    fi

    read -r input
    echo "${input:-$default}"
}

# 询问是/否问题
ask_yes_no() {
    local prompt=$1
    local default=$2
    local input

    while true; do
        if [[ "$default" == "y" ]]; then
            echo -ne "${BLUE}?${NC} $prompt [Y/n]: "
        else
            echo -ne "${BLUE}?${NC} $prompt [y/N]: "
        fi
        read -r input
        input=${input:-$default}

        case $input in
            [Yy]) return 0 ;;
            [Nn]) return 1 ;;
            *) echo "  请输入 y 或 n" ;;
        esac
    done
}

# 解析多个 URL 并验证
parse_urls() {
    local urls_input=$1
    local urls=()

    IFS=';' read -ra URL_ARRAY <<<"$urls_input"
    for url in "${URL_ARRAY[@]}"; do
        url=$(echo "$url" | xargs) # 移除前后空格
        if [[ -n "$url" ]]; then
            urls+=("$url")
        fi
    done

    printf '%s\n' "${urls[@]}"
}

# 验证 URL 格式
validate_url() {
    local url=$1
    if [[ $url =~ ^https?:// ]]; then
        return 0
    else
        return 1
    fi
}

# 确认 URLs
confirm_urls() {
    local urls=$1
    print_info "已输入的 Prometheus 推送地址："
    echo "$urls" | nl -v 1 -w 2 -s '. '
    echo ""
}

# 主菜单
main_menu() {
    print_title "ping_exporter systemd 服务安装配置（简化版）"

    print_info "仅需配置以下必要参数"
    echo ""

    # 1. 获取 ping_exporter 可执行文件路径
    local ping_exporter_path
    ping_exporter_path=$(ask_input "ping_exporter 可执行文件路径" "/usr/local/bin/ping_exporter")
    
    if [[ ! -f "$ping_exporter_path" ]]; then
        print_warning "文件不存在: $ping_exporter_path"
        if ! ask_yes_no "继续使用此路径?" "n"; then
            print_error "安装已取消"
            exit 1
        fi
    fi
    echo ""

    # 2. 使用默认的 systemd 和 env 目录
    local systemd_dir="/etc/systemd/system"
    local env_file="/etc/default/ping_exporter"
    
    if [[ ! -d "$systemd_dir" ]]; then
        print_error "目录不存在: $systemd_dir"
        exit 1
    fi
    
    print_success "使用默认配置目录"
    echo "  • systemd 服务: $systemd_dir/ping_exporter.service"
    echo "  • 环境变量文件: $env_file"
    echo ""

    # 3. 获取数据目录 (用于存放队列、缓存等)
    local data_dir
    data_dir=$(ask_input "数据文件存储目录" "/var/lib/ping_exporter")
    
    # 确保目录存在
    if [[ ! -d "$data_dir" ]]; then
        print_info "创建数据目录: $data_dir"
        mkdir -p "$data_dir"
        chmod 755 "$data_dir"
    fi
    echo ""

    # 4. 获取 Web 服务监听地址
    local listen_address
    listen_address=$(ask_input "Web 服务监听地址" "127.0.0.1:9427")
    echo ""

    # 5. 获取配置文件路径（可选）
    local config_file
    config_file=$(ask_input "配置文件路径（留空表示不使用）")
    echo ""

    # 6. 获取配置刷新间隔
    local config_refresh
    config_refresh=$(ask_input "配置刷新间隔" "1m")
    echo ""

    # 7. 获取 Prometheus 推送地址
    print_info "请输入 Prometheus 推送地址（多个地址使用分号分隔）"
    print_info "示例: http://prom1:9091/metrics/job/ping;http://prom2:9091/metrics/job/ping"
    echo ""

    local pushmetrics_urls
    local valid=false
    while [[ $valid == false ]]; do
        pushmetrics_urls=$(ask_input "推送地址列表")
        if [[ -z "$pushmetrics_urls" ]]; then
            print_error "至少需要输入一个地址"
            continue
        fi

        # 验证所有 URL
        local all_valid=true
        while IFS= read -r url; do
            if ! validate_url "$url"; then
                print_error "无效的 URL 格式: $url"
                all_valid=false
            fi
        done <<<"$(parse_urls "$pushmetrics_urls")"

        if [[ $all_valid == true ]]; then
            valid=true
            confirm_urls "$(parse_urls "$pushmetrics_urls")"
        fi
    done
    echo ""

    # 显示配置总结
    show_summary "$ping_exporter_path" "$systemd_dir" "$env_file" "$data_dir" "$listen_address" \
        "$config_file" "$config_refresh" "$pushmetrics_urls"
}

# 显示配置总结
show_summary() {
    local ping_exporter_path=$1
    local systemd_dir=$2
    local env_file=$3
    local data_dir=$4
    local listen_address=$5
    local config_file=$6
    local config_refresh=$7
    local pushmetrics_urls=$8

    print_title "配置总结"

    echo "文件位置："
    echo "  • ping_exporter: $ping_exporter_path"
    echo "  • systemd 文件: $systemd_dir/ping_exporter.service"
    echo "  • 环境文件: $env_file"
    echo ""

    echo "数据存储目录："
    echo "  • 基础目录: $data_dir"
    echo "  • 队列目录: $data_dir/pushmetrics-queue"
    echo "  • 缓存目录: $data_dir/config-cache"
    echo ""

    echo "必要参数："
    echo "  • Web 监听地址: $listen_address"
    if [[ -n "$config_file" ]]; then
        echo "  • 配置文件: $config_file"
    else
        echo "  • 配置文件: （未使用）"
    fi
    echo "  • 配置刷新间隔: $config_refresh"
    echo ""

    echo "推送地址："
    echo "$pushmetrics_urls" | nl -v 1 -w 2 -s '. '
    echo ""

    if ! ask_yes_no "确认安装上述配置?" "y"; then
        print_error "安装已取消"
        exit 1
    fi

    # 执行安装
    install_config "$ping_exporter_path" "$systemd_dir" "$env_file" "$data_dir" "$listen_address" \
        "$config_file" "$config_refresh" "$pushmetrics_urls"
}

# 安装配置文件
install_config() {
    local ping_exporter_path=$1
    local systemd_dir=$2
    local env_file=$3
    local data_dir=$4
    local listen_address=$5
    local config_file=$6
    local config_refresh=$7
    local pushmetrics_urls=$8

    echo ""
    print_title "安装配置文件"

    # 创建数据目录及其子目录
    print_info "创建数据存储目录..."
    mkdir -p "$data_dir/pushmetrics-queue"
    mkdir -p "$data_dir/config-cache"
    chmod 755 "$data_dir"
    chmod 755 "$data_dir/pushmetrics-queue"
    chmod 755 "$data_dir/config-cache"
    print_success "数据目录已创建"
    echo ""

    # 创建环境变量文件
    print_info "创建环境变量文件: $env_file"

    cat >"$env_file" <<EOF
# ping_exporter 环境变量配置
# Generated: $(date)

# ===== Web 服务器配置 =====
PING_LISTEN_ADDRESS=$listen_address

# ===== 配置文件相关 =====
EOF

    if [[ -n "$config_file" ]]; then
        echo "PING_CONFIG_FILE=$config_file" >> "$env_file"
    fi
    echo "PING_CONFIG_REFRESH=$config_refresh" >> "$env_file"

    cat >>"$env_file" <<EOF

# ===== 推送指标配置 =====
PING_PUSHMETRICS_BASE_DIR=$data_dir
PING_PUSHMETRICS_URLS=$pushmetrics_urls

EOF

    chmod 644 "$env_file"
    print_success "环境变量文件已创建"
    echo ""

    # 创建 systemd 服务文件
    local systemd_service="$systemd_dir/ping_exporter.service"
    print_info "创建 systemd 服务文件: $systemd_service"

    cat >"$systemd_service" <<EOF
[Unit]
Description=Ping Exporter - Prometheus Ping Monitor
Documentation=https://github.com/ChengChung/ping_exporter
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
EnvironmentFile=$env_file
ExecStart=$ping_exporter_path
Restart=on-failure
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

    chmod 644 "$systemd_service"
    print_success "systemd 服务文件已创建"
    echo ""
    
    # 提示用户后续操作
    print_title "安装完成"
    print_success "所有配置文件已生成"
    echo ""
    echo "下一步操作："
    echo ""
    echo "1. 加载 systemd 配置："
    echo "   $ sudo systemctl daemon-reload"
    echo ""
    echo "2. 启动 ping_exporter 服务："
    echo "   $ sudo systemctl start ping_exporter"
    echo ""
    echo "3. 设置开机自启（可选）："
    echo "   $ sudo systemctl enable ping_exporter"
    echo ""
    echo "4. 查看服务状态："
    echo "   $ sudo systemctl status ping_exporter"
    echo ""
    echo "5. 查看实时日志："
    echo "   $ sudo journalctl -u ping_exporter -f"
    echo ""
    echo "配置文件位置："
    echo "  • systemd: $systemd_service"
    echo "  • 环境文件: $env_file"
    echo "  • 数据目录: $data_dir"
    echo ""
}
ExecStart=$ping_exporter_path
Restart=on-failure
RestartSec=5s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

    chmod 644 "$systemd_service"
    print_success "systemd 服务文件已创建"
    echo ""

    # 重新加载 systemd
    print_info "重新加载 systemd 配置..."
    systemctl daemon-reload
    print_success "systemd 配置已重新加载"
    echo ""

    # 启用服务
    if ask_yes_no "立即启用并启动服务?" "y"; then
        systemctl enable ping_exporter.service
        print_success "服务已启用"

        if systemctl start ping_exporter.service; then
            print_success "服务已启动"
            echo ""
            sleep 2
            show_status
        else
            print_error "服务启动失败，请检查日志"
            systemctl status ping_exporter.service
        fi
    fi

    echo ""
    print_title "安装完成"
    show_next_steps "$systemd_service" "$env_file"
}

# 显示服务状态
show_status() {
    print_info "服务状态："
    echo ""
    systemctl status ping_exporter.service --no-pager || true
}

# 显示后续步骤
show_next_steps() {
    local systemd_service=$1
    local env_file=$2

    echo "后续步骤："
    echo ""
    echo "  1. 查看服务状态:"
    echo "     ${BLUE}sudo systemctl status ping_exporter${NC}"
    echo ""
    echo "  2. 查看实时日志:"
    echo "     ${BLUE}sudo journalctl -u ping_exporter -f${NC}"
    echo ""
    echo "  3. 修改配置:"
    echo "     ${BLUE}sudo vim $env_file${NC}"
    echo "     ${BLUE}sudo systemctl restart ping_exporter${NC}"
    echo ""
    echo "配置文件说明:"
    echo ""
    echo "  • 环境变量文件: $env_file"
    echo "    - 修改参数后需要重启服务"
    echo ""
    echo "  • systemd 服务文件: $systemd_service"
    echo "    - 通常无需修改"
    echo ""
}

# 主程序入口
main() {
    print_title "ping_exporter systemd 安装脚本（简化版）"

    # 检查 root 权限
    check_root

    # 检查依赖
    for cmd in systemctl grep sed; do
        if ! command -v "$cmd" &>/dev/null; then
            print_error "缺少必要的命令: $cmd"
            exit 1
        fi
    done

    # 运行主菜单
    main_menu
}

# 如果脚本被直接执行（不是 sourced）
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi
