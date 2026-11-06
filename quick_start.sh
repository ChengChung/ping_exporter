#!/bin/bash
#
# 快速开始：ping_exporter systemd 安装示例脚本
# 这个脚本展示如何在无交互模式下使用 install_systemd.sh
#

set -e

echo "======================================================================"
echo "ping_exporter systemd 安装 - 快速开始"
echo "======================================================================"
echo ""

# 检查是否为 root
if [[ $EUID -ne 0 ]]; then
    echo "错误: 此脚本必须以 root 身份运行"
    echo "请使用: sudo bash $0"
    exit 1
fi

# 打印使用说明
usage() {
    cat << EOF
使用方法:
    sudo bash $0

交互式安装步骤:
    1. 运行脚本: sudo bash install_systemd.sh
    2. 按照提示输入配置参数
    3. 查看配置总结
    4. 确认安装
    5. 服务自动启动

快速示例（以下是典型的回答）:

    问: ping_exporter 可执行文件路径
    答: /usr/local/bin/ping_exporter
    
    问: systemd 服务文件目录
    答: /etc/systemd/system
    
    问: 环境变量文件路径
    答: /etc/default/ping_exporter
    
    问: 推送地址列表
    答: http://prometheus1:9091/metrics/job/ping;http://prometheus2:9091/metrics/job/ping
    
    问: Ping 目标地址
    答: 8.8.8.8 1.1.1.1 119.29.29.29
    
    问: Web 服务监听地址
    答: 127.0.0.1:9427
    
    问: 日志级别
    答: info
    
    问: Ping 间隔
    答: 5s
    
    问: Ping 超时
    答: 5s
    
    问: 推送指标间隔
    答: 15s

完成后会自动启用并启动服务。

====================================================================
EOF
}

# 打印说明
if [[ "$1" == "-h" ]] || [[ "$1" == "--help" ]]; then
    usage
    exit 0
fi

echo "下面是一个典型的安装流程示例："
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "步骤 1: 准备条件"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# 检查 ping_exporter 是否存在
if command -v ping_exporter &>/dev/null; then
    echo "✓ ping_exporter 已安装"
    echo "  位置: $(which ping_exporter)"
else
    echo "⚠ ping_exporter 未发现"
    echo "  请确保 ping_exporter 已安装并在 PATH 中"
    echo "  或将其放在: /usr/local/bin/ping_exporter"
fi
echo ""

# 检查 systemd
if command -v systemctl &>/dev/null; then
    echo "✓ systemd 已安装"
else
    echo "✗ 此系统不支持 systemd"
    exit 1
fi
echo ""

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "步骤 2: 查看安装脚本"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_SCRIPT="$SCRIPT_DIR/install_systemd.sh"

if [[ ! -f "$INSTALL_SCRIPT" ]]; then
    echo "✗ 找不到安装脚本: $INSTALL_SCRIPT"
    exit 1
fi

echo "✓ 安装脚本位置: $INSTALL_SCRIPT"
echo "✓ 脚本大小: $(du -h "$INSTALL_SCRIPT" | cut -f1)"
echo ""

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "步骤 3: 查看可选参数"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "关键参数说明:"
echo ""
echo "  【pushMetricsURLs - Prometheus 推送地址】"
echo "  • 格式: URL1;URL2;URL3（分号分隔）"
echo "  • 示例: http://prom1:9091/metrics/job/ping;http://prom2:9091/metrics/job/ping"
echo "  • 说明: 支持多个 Prometheus 实例，每个实例独立队列"
echo ""
echo "  【systemd 文件位置】"
echo "  • 默认: /etc/systemd/system/ping_exporter.service"
echo "  • 说明: systemd 服务配置文件"
echo ""
echo "  【env 文件位置】"
echo "  • 默认: /etc/default/ping_exporter"
echo "  • 说明: 环境变量配置文件，所有服务参数都在这里"
echo ""

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "步骤 4: 启动交互式安装"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "现在将启动交互式配置向导..."
echo ""

read -p "按 Enter 键继续..." -r

echo ""

# 运行安装脚本
"$INSTALL_SCRIPT"

# 检查安装是否成功
if systemctl is-active --quiet ping_exporter; then
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "安装完成！"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "✓ 服务正在运行"
    echo ""
    echo "查看服务状态:"
    echo "  sudo systemctl status ping_exporter"
    echo ""
    echo "查看实时日志:"
    echo "  sudo journalctl -u ping_exporter -f"
    echo ""
    echo "修改配置:"
    echo "  sudo vim /etc/default/ping_exporter"
    echo "  sudo systemctl restart ping_exporter"
    echo ""
fi
