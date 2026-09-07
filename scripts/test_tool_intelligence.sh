#!/bin/bash
# Tool Intelligence Layer — Acceptance Test Suite
#
# Runs a battery of natural-language requests through the Capability Planner
# and verifies that each request is correctly routed to the expected tool.
#
# Usage:
#   ./test_tool_intelligence.sh              # full suite
#   ./test_tool_intelligence.sh -v            # verbose
#   ./test_tool_intelligence.sh -f query.txt  # custom queries file
#
# Exit code: 0 if all pass, 1 if any fail.

set -o pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PLANNER_PKG="./internal/tools/planner/"
EXIT_CODE=0
VERBOSE=false
PASS=0
FAIL=0

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# ── Parse flags ───────────────────────────────────────────
while getopts "vf:" opt; do
    case $opt in
        v) VERBOSE=true ;;
        f) QUERY_FILE="$OPTARG" ;;
        *) echo "Usage: $0 [-v] [-f query_file]"; exit 1 ;;
    esac
done

# ── Test cases ────────────────────────────────────────────
# Format: "query|expected_goal|expected_operation|expected_capability"
TESTS=(
    # ── Math ──
    "1+1等于多少|mathematical computation|arithmetic|Arithmetic"
    "计算 2的10次方|mathematical computation|arithmetic|Arithmetic"
    "sqrt(16) 等于多少|mathematical computation|arithmetic|Arithmetic"
    "从1累加到100万|mathematical computation|summation|Summation"
    "sum from 1 to 1000|mathematical computation|summation|Summation"

    # ── Text ──
    "把hello改成大写|text processing|string_manipulation|StringManipulation"
    "去掉这个字符串两端的空格|text processing|string_manipulation|StringManipulation"
    "用正则表达式匹配邮箱地址|text processing|regex|Regex"

    # ── Data ──
    "把这段json格式化一下|data transformation|json_processing|JSONProcessing"
    "parse this json data|data transformation|json_processing|JSONProcessing"

    # ── Hash / Crypto ──
    "计算hello的sha256哈希值|cryptographic operation|hashing|Hashing"
    "md5 of test string|cryptographic operation|hashing|Hashing"
    "把hello用base64编码|encoding operation|base64|Base64"

    # ── PDF / Document ──
    "提取这个PDF文件中的文字|document processing|pdf_parsing|PDFParsing"
    "read this pdf file|document processing|pdf_parsing|PDFParsing"

    # ── Network / Search ──
    "搜索一下Go语言的最新动态|information retrieval|search|WebSearch"
    "search the web for AI news|information retrieval|search|WebSearch"

    # ── ID / UUID ──
    "生成一个UUID|identifier generation|id_generation|IDGeneration"
    "generate a unique id|identifier generation|id_generation|IDGeneration"

    # ── Execution / Code ──
    "运行这段Python代码|code execution|code_execution|CodeExecution"
    "execute this shell script|code execution|code_execution|CodeExecution"

    # ── Embedding ──
    "把这个文本做向量化|vector embedding|embedding|Embedding"
    "generate embedding for this text|vector embedding|embedding|Embedding"

    # ── Advanced Math ──
    "10的阶乘是多少|mathematical computation|discrete_math|DiscreteMath"
    "计算从10个中选3个的组合数|mathematical computation|discrete_math|DiscreteMath"
    "12和18的最大公约数|mathematical computation|number_theory|NumberTheory"
    "求15和20的最小公倍数|mathematical computation|number_theory|NumberTheory"
    "17是素数吗|mathematical computation|number_theory|NumberTheory"
    "计算1,2,3,4,5的平均值|mathematical computation|statistics|Statistics"
    "计算1,2,3,4,5的标准差|mathematical computation|statistics|Statistics"
    "正态分布x=0 mu=0 sigma=1的概率密度|mathematical computation|probability|Probability"
    "2的10次方等于多少|mathematical computation|arithmetic|Arithmetic"
    "根号16等于多少|mathematical computation|arithmetic|Arithmetic"
    "3的平方等于多少|mathematical computation|arithmetic|Arithmetic"
)

# ── Build test binary ─────────────────────────────────────
echo -e "${CYAN}🔧 Building Capability Planner test harness...${NC}"
cd "$ROOT"

HARNESS="./examples/_fixtures/tool-intelligence/cmd/check/"

# ── Verify harness builds ─────────────────────────────────
if ! go build "$HARNESS" 2>/dev/null; then
    echo -e "${RED}✗ HARNESS BUILD FAILED${NC}"
    go build "$HARNESS" 2>&1
    exit 1
fi

# ── If custom query file provided, parse it ───────────────
if [ -n "$QUERY_FILE" ]; then
    if [ ! -f "$QUERY_FILE" ]; then
        echo -e "${RED}✗ Query file not found: $QUERY_FILE${NC}"
        exit 1
    fi
    # Read queries from file, one per line
    CUSTOM_TESTS=()
    while IFS='|' read -r query expected_goal expected_op expected_cap; do
        # Skip empty lines and comments
        [[ -z "$query" || "$query" == \#* ]] && continue
        CUSTOM_TESTS+=("${query}|${expected_goal}|${expected_op}|${expected_cap}")
    done < "$QUERY_FILE"
    TESTS=("${CUSTOM_TESTS[@]}")
fi

# ── Run tests ─────────────────────────────────────────────
echo ""
echo -e "${CYAN}🧪 Running ${#TESTS[@]} acceptance tests...${NC}"
echo ""

run_test() {
    local line="$1"
    IFS='|' read -r query expected_goal expected_op expected_cap <<< "$line"

    # Truncate query for display
    local display="${query:0:50}"
    [ "${#query}" -gt 50 ] && display="${display}..."

    local output
    output=$(go run "$HARNESS" "$query" 2>&1)
    local status=$?

    if [ $status -ne 0 ]; then
        echo -e "  ${RED}✗${NC} ${display}"
        echo "    → ${output#*|}"
        FAIL=$((FAIL + 1))
        EXIT_CODE=1
        return
    fi

    # Parse output: OK|query|goal|operation|capabilities
    IFS='|' read -r _ _ actual_goal actual_op actual_caps <<< "$output"

    local errors=""
    # Check expected goal (if specified)
    if [ "$expected_goal" != "*" ] && [ "$actual_goal" != "$expected_goal" ]; then
        errors="${errors}goal:expect=$expected_goal actual=$actual_goal "
    fi
    # Check expected operation (if specified)
    if [ "$expected_op" != "*" ] && [ "$actual_op" != "$expected_op" ]; then
        errors="${errors}op:expect=$expected_op actual=$actual_op "
    fi
    # Check expected capability is present
    if [ "$expected_cap" != "*" ] && ! echo "$actual_caps" | grep -q "$expected_cap"; then
        errors="${errors}capa:expect=$expected_cap in [$actual_caps] "
    fi

    if [ -n "$errors" ]; then
        echo -e "  ${RED}✗${NC} ${display}"
        echo -e "    ${RED}  ${errors}${NC}"
        if $VERBOSE; then
            echo "    actual: goal=$actual_goal op=$actual_op caps=$actual_caps"
        fi
        FAIL=$((FAIL + 1))
        EXIT_CODE=1
    else
        echo -e "  ${GREEN}✓${NC} ${display}"
        if $VERBOSE; then
            echo "    → $actual_goal / $actual_op / $actual_caps"
        fi
        PASS=$((PASS + 1))
    fi
}

for test_case in "${TESTS[@]}"; do
    run_test "$test_case"
done

# ── Summary ───────────────────────────────────────────────
echo ""
echo -e "${CYAN}══════════════════════════════════════════════${NC}"
echo -e "  ${GREEN}Pass: $PASS${NC}"
echo -e "  ${RED}Fail: $FAIL${NC}"
echo -e "  Total: $((PASS + FAIL))"
echo -e "${CYAN}══════════════════════════════════════════════${NC}"
echo ""

# ── Run the full example as integration check ─────────────
echo -e "${CYAN}🔍 Running full tool-intelligence example...${NC}"
go run ./examples/_fixtures/tool-intelligence/ > /tmp/planner_demo_output.txt 2>&1
if [ $? -eq 0 ]; then
    echo -e "  ${GREEN}✓${NC} Full example runs successfully"
else
    echo -e "  ${RED}✗${NC} Full example failed"
    cat /tmp/planner_demo_output.txt
    EXIT_CODE=1
fi

exit $EXIT_CODE
