#!/bin/bash
set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source "$script_dir/install_systemd.sh"

test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT
systemd_dir="$test_dir/systemd"
env_dir="$test_dir/default"
SCRIPT_DIR=$script_dir
calls="$test_dir/systemctl.log"

systemctl() {
    printf '%s\n' "$*" >>"$calls"
}

sleep() { :; }

assert_contains() {
    grep -Fqx -- "$2" "$1" || die "$1 缺少预期内容: $2"
}

collect_config <<EOF
bad/name
../escape
bad@instance
cn
/bin/true

127.0.0.1:9428

127.0.0.1


EOF
[[ "$instance" == cn && "$data_dir" == /var/lib/ping_exporter/instances/cn ]] ||
    die "实例输入验证或默认数据目录错误"
[[ "$service_name" == ping_exporter@cn.service && "$unit_name" == ping_exporter@.service ]] ||
    die "实例服务命名错误"
[[ "$env_file" == "$env_dir/ping_exporter-cn" ]] || die "实例环境文件命名错误"
data_dir="$test_dir/data-cn"
start_service=y
install_config
assert_contains "$systemd_dir/ping_exporter@.service" "EnvironmentFile=$env_dir/ping_exporter-%i"
assert_contains "$env_file" 'PING_LISTEN_ADDRESS="127.0.0.1:9428"'
assert_contains "$calls" 'enable ping_exporter@cn.service'
assert_contains "$calls" 'restart ping_exporter@cn.service'
assert_contains "$calls" 'is-active --quiet ping_exporter@cn.service'
[[ $(stat -c %a "$env_file") == 600 && $(stat -c %a "$systemd_dir/$unit_name") == 644 ]] ||
    die "配置文件权限错误"
[[ -d "$data_dir/pushmetrics-queue" && -d "$data_dir/config-cache" ]] ||
    die "实例数据目录未创建"
cp "$env_file" "$test_dir/cn.original"

instance=us
configure_service_paths
[[ "$default_data_dir" == /var/lib/ping_exporter/instances/us ]] || die "实例数据目录未隔离"
data_dir="$test_dir/data-us"
listen_address=127.0.0.1:9429
start_service=n
: >"$calls"
install_config
assert_contains "$env_file" 'PING_LISTEN_ADDRESS="127.0.0.1:9429"'
cmp "$env_dir/ping_exporter-cn" "$test_dir/cn.original" || die "其他实例环境文件被修改"
[[ $(cat "$calls") == daemon-reload ]] || die "不启动服务时仍执行了服务操作"
compgen -G "$systemd_dir/ping_exporter@.service.bak.*" >/dev/null || die "共享模板未备份"

listen_address=127.0.0.1:9430
install_config
compgen -G "$env_file.bak.*" >/dev/null || die "实例环境文件未备份"
assert_contains "$env_file" 'PING_LISTEN_ADDRESS="127.0.0.1:9430"'

collect_config <<EOF

/bin/true



127.0.0.1


EOF
[[ -z "$instance" && "$data_dir" == /var/lib/ping_exporter ]] || die "原安装默认值发生变化"
[[ "$service_name" == ping_exporter.service && "$unit_name" == ping_exporter.service ]] ||
    die "原服务命名发生变化"
data_dir="$test_dir/data-default"
start_service=y
: >"$calls"
install_config
assert_contains "$systemd_dir/ping_exporter.service" "EnvironmentFile=$env_dir/ping_exporter"
assert_contains "$calls" 'enable ping_exporter.service'
assert_contains "$calls" 'restart ping_exporter.service'
[[ -f "$systemd_dir/ping_exporter@.service" && -f "$env_dir/ping_exporter-us" ]] ||
    die "原服务安装影响了实例服务"
cmp "$env_dir/ping_exporter-cn" "$test_dir/cn.original" || die "实例环境文件被修改"

if (collect_config </dev/null); then
    die "输入结束后未中止安装"
fi

printf 'systemd installer tests passed\n'
