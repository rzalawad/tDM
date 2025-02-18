#!/bin/bash

# Build script for Download Manager Go Client

# Exit on any error
set -e

# Color codes for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Project details
PROJECT_NAME="download-manager-client"
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_DIR="./dist"
# PLATFORMS=("linux/amd64" "darwin/amd64" "windows/amd64")
PLATFORMS=("linux/amd64")

# Clean previous builds
clean() {
    echo -e "${YELLOW}Cleaning previous builds...${NC}"
    rm -rf "${BUILD_DIR}"
    mkdir -p "${BUILD_DIR}"
}

# Run tests
run_tests() {
    echo -e "${YELLOW}Running tests...${NC}"
    go test ./... -v
}

# Build for a specific platform
build_for_platform() {
    local platform="$1"
    local os=$(echo "$platform" | cut -d'/' -f1)
    local arch=$(echo "$platform" | cut -d'/' -f2)
    local output_name="${BUILD_DIR}/${PROJECT_NAME}-${VERSION}-${os}-${arch}"

    # Add .exe extension for Windows
    [[ "$os" == "windows" ]] && output_name="${output_name}.exe"

    echo -e "${YELLOW}Building for ${platform}...${NC}"
    
    # Cross-compile with specific OS and architecture
    GOOS="$os" GOARCH="$arch" go build \
        -o "$output_name" \
        -ldflags "-X main.version=${VERSION} -s -w" \
        ./cmd/client/main.go
}

# Create compressed archives
package_artifacts() {
    echo -e "${YELLOW}Creating compressed archives...${NC}"
    
    for platform in "${PLATFORMS[@]}"; do
        local os=$(echo "$platform" | cut -d'/' -f1)
        local arch=$(echo "$platform" | cut -d'/' -f2)
        local binary_name="${PROJECT_NAME}-${VERSION}-${os}-${arch}"
        local archive_name="${binary_name}"

        # Windows gets .zip, others get .tar.gz
        if [[ "$os" == "windows" ]]; then
            (cd "${BUILD_DIR}" && zip "${archive_name}.zip" "${binary_name}.exe")
        else
            (cd "${BUILD_DIR}" && tar -czvf "${archive_name}.tar.gz" "${binary_name}")
        fi
    done
}

# Generate checksum file
generate_checksums() {
    echo -e "${YELLOW}Generating checksums...${NC}"
    (
        cd "${BUILD_DIR}"
        sha256sum *.{gz,zip} > "${PROJECT_NAME}-${VERSION}-checksums.txt"
    )
}

# Main build process
main() {
    clean
    run_tests

    # Build for all platforms
    for platform in "${PLATFORMS[@]}"; do
        build_for_platform "$platform"
    done

    package_artifacts
    generate_checksums

    echo -e "${GREEN}Build completed successfully!${NC}"
    echo -e "${YELLOW}Artifacts in ${BUILD_DIR}:${NC}"
    ls -l "${BUILD_DIR}"
}

# Allow running specific steps
case "$1" in
    "clean")
        clean
        ;;
    "test")
        run_tests
        ;;
    "build")
        main
        ;;
    *)
        main
        ;;
esac