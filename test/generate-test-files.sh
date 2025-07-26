#!/usr/bin/env bash
set -e

# Create test files directory
TEST_FILES_DIR="test-files"
mkdir -p "$TEST_FILES_DIR"

echo "Generating test files..."

# Function to create a file with random data
create_test_file() {
    local name=$1
    local size=$2
    local path="$TEST_FILES_DIR/$name"
    
    if [ -f "$path" ]; then
        echo "  $name already exists, skipping"
    else
        echo "  Creating $name (${size}MB)..."
        dd if=/dev/urandom of="$path" bs=1M count=$size status=progress 2>/dev/null || \
        dd if=/dev/urandom of="$path" bs=1M count=$size
    fi
}

# Create various sized test files
create_test_file "small-1mb.bin" 1
create_test_file "small-10mb.bin" 10
create_test_file "medium-50mb.bin" 50
create_test_file "medium-100mb.bin" 100
create_test_file "large-250mb.bin" 250
create_test_file "large-500mb.bin" 500

# Create some text files for variety
echo "Test content for file1" > "$TEST_FILES_DIR/test1.txt"
echo "Test content for file2" > "$TEST_FILES_DIR/test2.txt"

# Create a nested directory structure
mkdir -p "$TEST_FILES_DIR/subdir/nested"
echo "Nested file content" > "$TEST_FILES_DIR/subdir/nested/deep.txt"
dd if=/dev/urandom of="$TEST_FILES_DIR/subdir/nested/binary.dat" bs=1K count=100 2>/dev/null

echo "Test files generated in $TEST_FILES_DIR/"
ls -lh "$TEST_FILES_DIR/"