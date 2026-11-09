#!/bin/bash
# 交互式生成 ping_exporter 的 systemd 服务和环境配置。

set -Eeuo pipefail

die() {
    printf '错误: %s\n' "$*" >&2
    exit 1
}

trim_input() {
    REPLY=${1%$'\r'}
    REPLY="${REPLY#"${REPLY%%[![:space:]]*}"}"
    REPLY="${REPLY%"${REPLY##*[![:space:]]}"}"
}

# 输入通过 REPLY 返回，提示不会进入配置值；EOF 必须终止安装。
read_input() {
    local prompt=$1 default=${2:-} line
    printf '%s' "$prompt" >&2
    if [[ -n "$default" ]]; then
        printf ' [%s]' "$default" >&2
    fi
    printf ': ' >&2
    IFS= read -r line || die "输入已结束，安装中止。"
    trim_input "$line"
    REPLY=${REPLY:-$default}
}

ask_yes_no() {
    local prompt=$1 default=${2:-n}
    while true; do
        read_input "$prompt (y/n)" "$default"
        case "$REPLY" in
            y|Y|yes|YES) return 0 ;;
            n|N|no|NO) return 1 ;;
            *) printf '请输入 y 或 n。\n' >&2 ;;
        esac
    done
}

normalize_path() {
    trim_input "$1"
    case "$REPLY" in
        \"*\") REPLY=${REPLY:1:${#REPLY}-2} ;;
        \'*\') REPLY=${REPLY:1:${#REPLY}-2} ;;
    esac
    case "$REPLY" in
        "~") REPLY=$HOME ;;
        "~/"*) REPLY="$HOME/${REPLY:2}" ;;
    esac
    [[ -n "$REPLY" && "$REPLY" != *[$'\n\r\t']* ]] || {
        printf '路径不能为空或包含换行、制表符。\n' >&2
        return 1
    }
    REPLY=$(realpath -m -- "$REPLY") || return 1
}

validate_executable() {
    if [[ ! -e "$1" ]]; then
        printf '文件不存在: %s\n' "$1" >&2
    elif [[ ! -f "$1" ]]; then
        printf '不是普通文件: %s\n' "$1" >&2
    elif [[ ! -x "$1" ]]; then
        printf '文件存在，但没有执行权限: %s\n' "$1" >&2
    elif [[ "$1" == *[\"\\]* || "$1" =~ [[:cntrl:]] ]]; then
        printf 'systemd 不支持可执行文件路径中的双引号、反斜杠或控制字符: %s\n' "$1" >&2
    else
        return 0
    fi
    return 1
}

valid_url() {
    [[ "$1" =~ ^https?://[^/[:space:]?#]+([^[:space:]#]*)?$ ]]
}

normalize_urls() {
    local remaining=$1 part normalized=""
    while true; do
        part=${remaining%%;*}
        trim_input "$part"
        valid_url "$REPLY" || {
            printf '无效的 HTTP(S) 推送地址: %s\n' "$REPLY" >&2
            return 1
        }
        normalized+="${normalized:+;}$REPLY"
        [[ "$remaining" == *";"* ]] || break
        remaining=${remaining#*;}
    done
    REPLY=$normalized
}

valid_listener() {
    [[ "$1" =~ ^(\[[^]]+\]|[^:[:space:]]*):([0-9]+)$ ]] || return 1
    local port=${BASH_REMATCH[2]}
    [[ ${#port} -le 5 ]] && (( 10#$port >= 1 && 10#$port <= 65535 ))
}

valid_duration() {
    [[ "$1" =~ ^([0-9]+([.][0-9]+)?(ns|us|ms|s|m|h))+$ && "$1" =~ [1-9] ]]
}

configure_service_paths() {
    service_name=ping_exporter.service
    unit_name=$service_name
    env_file="$env_dir/ping_exporter"
    env_reference=${env_file//%/%%}
    default_data_dir=/var/lib/ping_exporter
    if [[ -n "$instance" ]]; then
        service_name="ping_exporter@$instance.service"
        unit_name=ping_exporter@.service
        env_file="$env_dir/ping_exporter-$instance"
        env_reference="${env_dir//%/%%}/ping_exporter-%i"
        default_data_dir="/var/lib/ping_exporter/instances/$instance"
    fi
}

collect_config() {
    local default_binary="/usr/local/bin/ping_exporter" candidate
    while true; do
        read_input "服务实例名（留空使用原来的非实例服务）"
        instance=$REPLY
        [[ -z "$instance" ]] && break
        if [[ "$instance" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$ && ${#instance} -le 233 ]]; then
            break
        fi
        printf '实例名须以字母、数字或下划线开头，只能包含字母、数字、下划线、点和连字符，最长 233 字符。\n' >&2
    done
    configure_service_paths
    if [[ ! -x "$default_binary" ]]; then
        if candidate=$(type -P ping_exporter); then
            default_binary=$candidate
        elif [[ -x "$SCRIPT_DIR/ping_exporter" ]]; then
            default_binary="$SCRIPT_DIR/ping_exporter"
        fi
    fi
    while true; do
        read_input "ping_exporter 可执行文件路径" "$default_binary"
        normalize_path "$REPLY" || continue
        binary=$REPLY
        validate_executable "$binary" && break
    done

    while true; do
        read_input "数据文件存储目录" "$default_data_dir"
        normalize_path "$REPLY" || continue
        data_dir=$REPLY
        if [[ ! -e "$data_dir" || -d "$data_dir" ]]; then
            break
        fi
        printf '该路径存在，但不是目录: %s\n' "$data_dir" >&2
    done
    while true; do
        read_input "Web 监听地址" "127.0.0.1:9427"
        listen_address=$REPLY
        valid_listener "$listen_address" && break
        printf '请输入 host:port 或 [IPv6]:port，端口范围 1-65535。\n' >&2
    done

    targets=""
    while true; do
        read_input "配置文件路径或 HTTP(S) URL（留空则输入 Ping 目标）"
        config_file=$REPLY
        [[ -n "$config_file" ]] || break
        if [[ "$config_file" == http://* || "$config_file" == https://* ]]; then
            valid_url "$config_file" && break
            printf '无效的配置 URL。\n' >&2
        elif normalize_path "$config_file"; then
            config_file=$REPLY
            [[ -f "$config_file" && -r "$config_file" ]] && break
            printf '配置文件不存在或不可读: %s\n' "$config_file" >&2
        fi
    done
    if [[ -z "$config_file" ]]; then
        while true; do
            read_input "Ping 目标（IP 或域名，空格分隔）"
            targets=$REPLY
            [[ -n "$targets" ]] && break
            printf '未指定配置文件时，至少需要一个 Ping 目标。\n' >&2
        done
    fi
    while true; do
        read_input "远程配置刷新间隔" "1m"
        config_refresh=$REPLY
        valid_duration "$config_refresh" && break
        printf '请输入正数时长，例如 30s、1m 或 1m30s。\n' >&2
    done
    while true; do
        read_input "Prometheus 文本导入 URL（分号分隔，留空禁用推送）"
        push_urls=$REPLY
        [[ -n "$push_urls" ]] || break
        if normalize_urls "$push_urls"; then
            push_urls=$REPLY
            break
        fi
    done
}

# EnvironmentFile 不是 shell 脚本；双引号转义保留空格和字面量。
env_entry() {
    local value=$2
    value=${value//\\/\\\\}
    value=${value//\"/\\\"}
    value=${value//\$/\\\$}
    value=${value//\`/\\\`}
    printf '%s="%s"\n' "$1" "$value"
}

backup_file() {
    local path=$1 backup
    if [[ -e "$path" ]]; then
        [[ -f "$path" && ! -L "$path" ]] || die "拒绝覆盖非普通文件或符号链接: $path"
        backup=$(mktemp "$path.bak.XXXXXX")
        cp -p -- "$path" "$backup"
        printf '已备份: %s\n' "$backup"
    elif [[ -L "$path" ]]; then
        die "拒绝覆盖符号链接: $path"
    fi
}

install_config() (
    local env_tmp="" unit_tmp=""
    trap 'rm -f -- "$env_tmp" "$unit_tmp"' EXIT
    mkdir -p -- "$systemd_dir" "$(dirname "$env_file")"
    env_tmp=$(mktemp "$env_file.tmp.XXXXXX")
    unit_tmp=$(mktemp "$systemd_dir/ping_exporter.XXXXXX${instance:+@}.service")
    {
        printf '# Generated by install_systemd.sh\n'
        env_entry PING_LISTEN_ADDRESS "$listen_address"
        env_entry PING_CONFIG_FILE "$config_file"
        env_entry PING_TARGETS "$targets"
        env_entry PING_CONFIG_REFRESH "$config_refresh"
        env_entry PING_PUSHMETRICS_BASE_DIR "$data_dir"
        env_entry PING_PUSHMETRICS_URLS "$push_urls"
    } >"$env_tmp"
    {
        cat <<'UNIT'
[Unit]
Description=Ping Exporter - Prometheus Ping Monitor
Documentation=https://github.com/ChengChung/ping_exporter
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
UNIT
        # EnvironmentFile 是单一路径，不能套用 ExecStart 的命令行引号。
        printf 'EnvironmentFile=%s\n' "$env_reference"
        printf 'ExecStart="%s"\n' "${binary//%/%%}"
        cat <<'UNIT'
Restart=on-failure
RestartSec=10s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
UNIT
    } >"$unit_tmp"
    chmod 600 "$env_tmp"
    chmod 644 "$unit_tmp"
    systemd-analyze verify "$unit_tmp" || die "systemd 服务文件校验失败，原配置未覆盖。"
    backup_file "$env_file"
    backup_file "$systemd_dir/$unit_name"
    install -d -m 755 -- "$data_dir" "$data_dir/pushmetrics-queue" "$data_dir/config-cache"
    mv -f -- "$env_tmp" "$env_file"
    mv -f -- "$unit_tmp" "$systemd_dir/$unit_name"
    systemctl daemon-reload || die "配置已写入，但 systemd 重载失败。请检查上述错误及备份。"
    if [[ "$start_service" == y ]]; then
        systemctl enable "$service_name" || die "服务启用失败。"
        systemctl restart "$service_name" || die "服务启动失败，请运行 journalctl -u $service_name -n 50。"
        sleep 1
        systemctl is-active --quiet "$service_name" || die "服务未保持运行，请运行 journalctl -u $service_name -n 50。"
        printf '服务已启用并启动。\n'
    else
        printf '配置已保存，未启动或重启服务。\n'
    fi
)

main() {
    [[ $# -eq 0 ]] || die "使用方法: sudo bash $0"
    [[ $EUID -eq 0 ]] || die "请使用 sudo bash $0 运行。"
    local cmd help
    for cmd in systemctl systemd-analyze realpath mktemp install cp mv chmod mkdir dirname sleep cat rm; do
        command -v "$cmd" >/dev/null || die "缺少必要命令: $cmd"
    done
    [[ -d /run/systemd/system ]] || die "当前系统未运行 systemd。"
    SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
    systemd_dir=/etc/systemd/system
    env_dir=/etc/default
    printf 'ping_exporter systemd 安装向导\n输入支持绝对路径、相对路径、~/ 路径和成对引号。\n'
    collect_config
    help=$("$binary" --help 2>&1) || die "无法执行 $binary --help，请检查程序格式、架构及依赖。"
    [[ "$help" == *PING_LISTEN_ADDRESS* && "$help" == *PING_TARGETS* && "$help" == *PING_PUSHMETRICS_URLS* ]] ||
        die "该程序不支持所需的 PING_* 环境变量，请先重新编译并更新可执行文件。"

    printf '\n配置总结:\n  程序: %s\n  数据目录: %s\n  监听: %s\n  配置: %s\n  目标: %s\n  刷新间隔: %s\n  推送: %s\n' \
        "$binary" "$data_dir" "$listen_address" "${config_file:-无}" "${targets:-由配置文件提供}" "$config_refresh" "${push_urls:-禁用}"
    printf '  服务: %s\n  单元文件: %s/%s\n  环境文件: %s\n已有配置将备份后替换。\n' "$service_name" "$systemd_dir" "$unit_name" "$env_file"
    if [[ -n "$instance" ]]; then
        printf '注意: 所有实例共用模板及可执行文件路径；更新模板会在其他实例下次启动/重启时生效。\n各实例须使用不同监听地址/端口和独立的数据目录，不能共享推送队列。\n'
    fi
    if ! ask_yes_no "确认安装上述配置?" n; then
        printf '安装已取消，未写入配置。\n'
        return
    fi
    start_service=n
    if ask_yes_no "立即启用并启动（已运行则重启）服务?" y; then
        start_service=y
    fi
    install_config
    printf '\n安装完成。\n修改参数: sudoedit %s\n启动/应用修改: sudo systemctl restart %s\n开机自启: sudo systemctl enable %s\n查看状态: sudo systemctl status %s --no-pager\n查看日志: sudo journalctl -u %s -f\n' \
        "$env_file" "$service_name" "$service_name" "$service_name" "$service_name"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    trap 'printf "安装失败（第 %s 行），请检查上述错误。\n" "$LINENO" >&2' ERR
    main "$@"
fi
