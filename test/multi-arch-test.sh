#!/bin/bash
set -e

echo "=== Multi-Architecture fatimg Test Suite ==="

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Test results file
RESULTS_FILE="/tmp/fatimg-test-results.txt"
rm -f "$RESULTS_FILE"
touch "$RESULTS_FILE"

# Function to record test result
record_result() {
    local key="$1"
    local value="$2"
    echo "$key=$value" >> "$RESULTS_FILE"
}

# Function to get test result
get_result() {
    local key="$1"
    grep "^$key=" "$RESULTS_FILE" 2>/dev/null | cut -d= -f2 || echo ""
}

# Run goreleaser to build all binaries
goreleaser build --snapshot --clean

# Function to run test on specific architecture
run_arch_test() {
    local arch=$1
    local test_name=$2
    local partition_size=$3
    local test_files=$4
    
    # Map architecture to binary name
    local binary_name=""
    case "$arch" in
        "linux/amd64")
            binary_name="fatimg_linux_amd64_v1"
            ;;
        "linux/arm64")
            binary_name="fatimg_linux_arm64_v8.0"
            ;;
        "linux/386")
            binary_name="fatimg_linux_386_sse2"
            ;;
        "linux/arm/v7")
            binary_name="fatimg_linux_arm_7"
            ;;
    esac
    
    echo -e "\n${YELLOW}Testing on $arch: $test_name${NC}"
    echo "  Partition size: ${partition_size}MB"
    echo "  Test files: $test_files"
    echo "  Binary: $binary_name"
    
    # Find the binary
    local binary_path="dist/$binary_name/fatimg"
    if [ -z "$binary_path" ]; then
        echo -e "${RED}Binary not found: $binary_name${NC}"
        record_result "$arch-$test_name" "FAIL"
        return
    fi
    
    # Build the Docker command
    docker run --rm \
        --platform "$arch" \
        -v "$(pwd)":/workspace \
        -w / \
        debian:bookworm-slim \
        sh -c "
            set -e

            # Ensure test files exist
            echo 'Preparing test files...'
            /workspace/test/generate-test-files.sh

            echo 'Creating disk image...'
            /workspace/$binary_path create --output test-${partition_size}mb.img --size $partition_size $test_files
            
            echo 'Listing contents...'
            /workspace/$binary_path ls test-${partition_size}mb.img
            
            echo 'Extracting files...'
            rm -rf extracted-test
            mkdir -p extracted-test
            /workspace/$binary_path cp test-${partition_size}mb.img extracted-test/
            
            echo 'Verifying extraction...'
            find extracted-test -type f -exec ls -l {} \;
            
            echo 'Cleaning up...'
            rm -f test-${partition_size}mb.img
            rm -rf extracted-test
        "
    
    if [ $? -eq 0 ]; then
        record_result "$arch-$test_name" "PASS"
    else
        record_result "$arch-$test_name" "FAIL"
    fi
}

# Define test architectures
ARCHITECTURES=(
    "linux/amd64"
    "linux/arm64"
    "linux/386"      # 32-bit x86
    "linux/arm/v7"   # 32-bit ARM
)

# Define test cases
# Format: "test_name:partition_size_mb:test_files"
TEST_CASES=(
    "small-image:512:test-files/small-*.bin test-files/*.txt"
    "medium-image:1024:test-files/medium-*.bin test-files/subdir"
    "near-2gb:2000:test-files/large-*.bin"
    "over-2gb:4096:test-files/"
    "4gb-image:4096:test-files/"
)

# Run tests for each architecture and test case
for arch in "${ARCHITECTURES[@]}"; do
    for test_case in "${TEST_CASES[@]}"; do
        IFS=':' read -r test_name partition_size test_files <<< "$test_case"
        run_arch_test "$arch" "$test_name" "$partition_size" "$test_files"
    done
done

# Print summary
echo -e "\n\n${YELLOW}=== Test Summary ===${NC}"
for arch in "${ARCHITECTURES[@]}"; do
    echo -e "\n$arch:"
    for test_case in "${TEST_CASES[@]}"; do
        IFS=':' read -r test_name partition_size test_files <<< "$test_case"
        result=$(get_result "$arch-$test_name")
        
        if [ "$result" == "PASS" ]; then
            echo -e "  $test_name: ${GREEN}PASS${NC}"
        elif [ "$result" == "FAIL" ]; then
            echo -e "  $test_name: ${RED}FAIL${NC}"
        elif [ "$result" == "SKIPPED" ]; then
            echo -e "  $test_name: ${YELLOW}SKIPPED${NC}"
        else
            echo -e "  $test_name: ${YELLOW}NOT RUN${NC}"
        fi
    done
done

# Check for any failures
FAILED=0
while IFS= read -r line; do
    if [[ "$line" == *"=FAIL" ]]; then
        FAILED=1
        break
    fi
done < "$RESULTS_FILE"

# Cleanup
rm -f "$RESULTS_FILE" .goreleaser.test.yaml

if [ $FAILED -eq 1 ]; then
    echo -e "\n${RED}Some tests failed!${NC}"
    exit 1
else
    echo -e "\n${GREEN}All tests passed!${NC}"
    exit 0
fi