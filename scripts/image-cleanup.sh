#!/bin/bash
# Shared image cleanup utilities for build system
# Plain text output for better CI compatibility and machine readability
# Used by both fusion-access-operator-build.sh and Makefile targets

set -e

# Configuration
CONTAINER_TOOL="${CONTAINER_TOOL:-podman}"
CLEANUP_IMAGES="${CLEANUP_IMAGES:-true}"
# Plain text output for better CI compatibility and machine readability

get_image_count() {
    # Use proper container tool output instead of fragile wc -l approach
    $CONTAINER_TOOL images -q | wc -l
}

check_until_filter_support() {
    # Check if container tool supports 'until' filter
    if $CONTAINER_TOOL image prune --help 2>/dev/null | grep -q "until"; then
        return 0
    else
        return 1
    fi
}

cleanup_dangling_images() {
    if [ "$CLEANUP_IMAGES" != "true" ]; then
        echo "INFO: Image cleanup disabled (CLEANUP_IMAGES=$CLEANUP_IMAGES)"
        return 0
    fi
    
    echo "INFO: Cleaning up dangling images to free disk space..."
    
    # Count images before cleanup using proper method
    IMAGES_BEFORE=$(get_image_count)
    echo "   Images before cleanup: $IMAGES_BEFORE"
    
    # Remove dangling images (untagged, orphaned layers)
    echo "   Removing dangling images..."
    $CONTAINER_TOOL image prune -f || {
        echo "   WARNING: Failed to prune dangling images (this is usually safe to ignore)"
    }
    
    # Remove unused images with version compatibility check
    echo "   Removing unused images..."
    if check_until_filter_support; then
        echo "   Using 24-hour filter (keeps recent cache layers)..."
        $CONTAINER_TOOL image prune -a --filter "until=24h" -f || {
            echo "   WARNING: Failed to prune old images with filter, trying without filter..."
            $CONTAINER_TOOL image prune -a -f || true
        }
    else
        echo "   Container tool doesn't support 'until' filter, using basic cleanup..."
        $CONTAINER_TOOL image prune -a -f || {
            echo "   WARNING: Failed to prune images (this is usually safe to ignore)"
        }
    fi
    
    # Count images after cleanup
    IMAGES_AFTER=$(get_image_count)
    CLEANED=$((IMAGES_BEFORE - IMAGES_AFTER))
    echo "   Images after cleanup: $IMAGES_AFTER"
    echo "   SUCCESS: Cleaned up $CLEANED dangling/unused images"
    
    # Show disk space saved
    echo "   Current disk usage:"
    $CONTAINER_TOOL system df || true
}

check_builder_prune_support() {
    # Check if container tool supports 'builder prune' command
    if $CONTAINER_TOOL builder prune --help &>/dev/null; then
        return 0
    else
        return 1
    fi
}

cleanup_build_cache() {
    if [ "$CLEANUP_IMAGES" != "true" ]; then
        return 0
    fi
    
    echo "INFO: Cleaning up build cache..."
    
    # Check podman version and builder support
    if check_builder_prune_support; then
        echo "   Using podman builder prune for build cache cleanup..."
        $CONTAINER_TOOL builder prune -f || {
            echo "   WARNING: Build cache prune failed (this is usually safe to ignore)"
        }
    else
        # Fallback for older podman versions - clean system cache
        echo "   Using podman system prune for cache cleanup (builder prune not supported)..."
        $CONTAINER_TOOL system prune -f || {
            echo "   WARNING: System cache prune failed (this is usually safe to ignore)"
        }
    fi
}

# Allow script to be sourced or executed directly
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    # Script is being executed directly, not sourced
    case "${1:-}" in
        "cleanup_dangling_images"|"dangling")
            cleanup_dangling_images
            ;;
        "cleanup_build_cache"|"cache")
            cleanup_build_cache
            ;;
        "all")
            cleanup_dangling_images
            cleanup_build_cache
            ;;
        *)
            echo "Usage: $0 {dangling|cache|all}"
            echo "  dangling - Clean up dangling and unused images"
            echo "  cache    - Clean up build cache"  
            echo "  all      - Run all cleanup operations"
            exit 1
            ;;
    esac
fi
