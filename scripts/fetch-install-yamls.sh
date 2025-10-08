#!/bin/bash
set -e

VERSIONS=("v5.2.3.4")

RAW_FILE_URL="https://raw.githubusercontent.com/IBM/ibm-spectrum-scale-container-native"

for VERSION in "${VERSIONS[@]}"; do
    echo "Processing branch: $VERSION"
    mkdir -p "files/$VERSION"
    
    FILE_URL="$RAW_FILE_URL/$VERSION/generated/scale/install.yaml"
    curl -L -o "files/$VERSION/install.yaml" "$FILE_URL"
    if [[ $? -eq 0 ]]; then
        echo "Downloaded install.yaml for $VERSION"
    else
        echo "Failed to download install.yaml for $VERSION"
    fi
done
