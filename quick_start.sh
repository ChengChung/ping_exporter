#!/bin/bash
# 启动交互式 systemd 安装向导。

set -Eeuo pipefail

if [[ "${1:-}" == -h || "${1:-}" == --help ]]; then
    cat <<'HELP'
使用方法:
    sudo bash quick_start.sh

请先重新编译 ping_exporter，确保它支持 PING_* 环境变量：
    go build -o ping_exporter .

向导会询问:
    1. 服务实例名（留空保留原来的非实例服务）
    2. 可执行文件路径（支持相对路径、空格和成对引号）
    3. 数据目录和 Web 监听地址
    4. 配置文件路径/HTTP(S) URL；留空时必须提供 Ping 目标
    5. 远程配置刷新间隔
    6. Prometheus 文本导入 URL（分号分隔，可留空禁用推送）
    7. 确认安装，以及是否立即启用并启动/重启服务

服务文件: /etc/systemd/system/ping_exporter.service
环境配置: /etc/default/ping_exporter
输入实例名（例如 cn）时:
    共享模板: /etc/systemd/system/ping_exporter@.service
    实例服务: ping_exporter@cn.service
    环境配置: /etc/default/ping_exporter-cn（模板通过 %i 引用）
    默认数据目录: /var/lib/ping_exporter/instances/cn
所有实例共用模板及程序路径，须分别配置监听地址/端口并使用独立数据目录。
已有文件会先备份。取消确认时不会写入配置。
HELP
    exit 0
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
exec bash "$script_dir/install_systemd.sh" "$@"
