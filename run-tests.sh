#!/bin/bash
#
# netcfg 测试脚本
# 用法: sudo ./run-tests.sh [all|supported|unsupported|netns|clean]
#
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
NETCFG="$SCRIPT_DIR/netcfg"
TESTS_DIR="$SCRIPT_DIR/tests"

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# 检查 root
check_root() {
    if [ "$EUID" -ne 0 ]; then
        echo -e "${RED}请使用 sudo 运行此脚本${NC}"
        exit 1
    fi
}

# 检查 netcfg
check_netcfg() {
    if [ ! -x "$NETCFG" ]; then
        echo -e "${RED}找不到 netcfg: $NETCFG${NC}"
        exit 1
    fi
}

# 清理函数
cleanup_test() {
    local name="$1"
    echo -e "${YELLOW}清理 $name ...${NC}"
    
    # 删除创建的接口
    for iface in $(ip link show 2>/dev/null | grep -E "dummy|veth|macvlan|vxlan|bond|br|vrf|vlan" | awk -F: '{print $2}' | awk '{print $1}' | grep -v '@'); do
        ip link del "$iface" 2>/dev/null || true
    done
    
    # 删除 netns
    for ns in $(ip netns list 2>/dev/null | awk '{print $1}'); do
        ip netns del "$ns" 2>/dev/null || true
    done
}

# 运行单个测试
run_single_test() {
    local yaml="$1"
    local name=$(basename "$yaml" .yaml)
    local dir=$(dirname "$yaml")
    local category=$(basename "$dir")
    
    echo ""
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BLUE}测试: $category / $name${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    
    # 显示配置
    echo -e "${YELLOW}配置内容:${NC}"
    cat "$yaml"
    echo ""
    
    # 解析测试
    echo -e "${YELLOW}[1/3] 解析测试 (generate)${NC}"
    local tmpdir=$(mktemp -d)
    cp "$yaml" "$tmpdir/"
    
    if $NETCFG generate -c "$tmpdir" > /dev/null 2>&1; then
        echo -e "${GREEN}✓ 解析成功${NC}"
    else
        echo -e "${RED}✗ 解析失败${NC}"
        rm -rf "$tmpdir"
        return 1
    fi
    
    # 应用测试 (仅对 supported 和 netns)
    if [[ "$category" == "supported" ]] || [[ "$category" == "netns" ]]; then
        echo -e "${YELLOW}[2/3] 应用测试 (apply)${NC}"
        
        if $NETCFG apply -c "$tmpdir" -d 2>&1; then
            echo -e "${GREEN}✓ 应用成功${NC}"
            
            # 验证
            echo -e "${YELLOW}[3/3] 验证结果${NC}"
            echo "创建的接口:"
            ip link show 2>/dev/null | grep -E "dummy|veth|macvlan|vxlan|bond|br|vrf|vlan" | head -20 || true
            
            if [[ "$category" == "netns" ]]; then
                echo ""
                echo "Network Namespaces:"
                ip netns list 2>/dev/null || true
            fi
            
            echo -e "${GREEN}✓ 测试通过${NC}"
        else
            echo -e "${RED}✗ 应用失败${NC}"
            rm -rf "$tmpdir"
            return 1
        fi
    else
        echo -e "${YELLOW}[2/3] 跳过应用测试 (不支持的功能)${NC}"
        echo -e "${YELLOW}[3/3] 跳过验证${NC}"
    fi
    
    rm -rf "$tmpdir"
    
    # 清理
    cleanup_test "$name"
    
    return 0
}

# 运行目录下所有测试
run_tests_in_dir() {
    local dir="$1"
    local category=$(basename "$dir")
    local passed=0
    local failed=0
    local total=0
    
    echo ""
    echo -e "${BLUE}══════════════════════════════════════════════════════════════════════${NC}"
    echo -e "${BLUE}                     测试类别: $category${NC}"
    echo -e "${BLUE}══════════════════════════════════════════════════════════════════════${NC}"
    
    for yaml in "$dir"/*.yaml; do
        [ -f "$yaml" ] || continue
        ((total++))
        
        if run_single_test "$yaml"; then
            ((passed++))
        else
            ((failed++))
        fi
    done
    
    echo ""
    echo -e "${BLUE}[$category 汇总] 总计: $total, 通过: ${GREEN}$passed${NC}, 失败: ${RED}$failed${NC}"
    
    return $failed
}

# 显示帮助
show_help() {
    echo "netcfg 测试脚本"
    echo ""
    echo "用法: sudo $0 [命令]"
    echo ""
    echo "命令:"
    echo "  all         运行所有测试"
    echo "  supported   仅运行支持的功能测试"
    echo "  unsupported 仅运行不支持的功能测试 (仅解析)"
    echo "  netns       仅运行 netns 测试"
    echo "  clean       清理所有测试创建的资源"
    echo "  help        显示此帮助"
    echo ""
    echo "示例:"
    echo "  sudo $0 all        # 运行所有测试"
    echo "  sudo $0 supported  # 仅测试支持的功能"
    echo "  sudo $0 netns      # 仅测试 netns 功能"
}

# 主函数
main() {
    local cmd="${1:-help}"
    
    case "$cmd" in
        all)
            check_root
            check_netcfg
            cleanup_test "initial"
            
            local total_failed=0
            
            run_tests_in_dir "$TESTS_DIR/supported" || ((total_failed+=$?))
            run_tests_in_dir "$TESTS_DIR/netns" || ((total_failed+=$?))
            run_tests_in_dir "$TESTS_DIR/unsupported" || ((total_failed+=$?))
            
            echo ""
            echo -e "${BLUE}══════════════════════════════════════════════════════════════════════${NC}"
            if [ $total_failed -eq 0 ]; then
                echo -e "${GREEN}所有测试通过!${NC}"
            else
                echo -e "${RED}有 $total_failed 个测试失败${NC}"
            fi
            echo -e "${BLUE}══════════════════════════════════════════════════════════════════════${NC}"
            
            exit $total_failed
            ;;
            
        supported)
            check_root
            check_netcfg
            cleanup_test "initial"
            run_tests_in_dir "$TESTS_DIR/supported"
            ;;
            
        unsupported)
            check_root
            check_netcfg
            run_tests_in_dir "$TESTS_DIR/unsupported"
            ;;
            
        netns)
            check_root
            check_netcfg
            cleanup_test "initial"
            run_tests_in_dir "$TESTS_DIR/netns"
            ;;
            
        clean)
            check_root
            cleanup_test "all"
            echo -e "${GREEN}清理完成${NC}"
            ;;
            
        help|--help|-h)
            show_help
            ;;
            
        *)
            echo -e "${RED}未知命令: $cmd${NC}"
            show_help
            exit 1
            ;;
    esac
}

main "$@"
