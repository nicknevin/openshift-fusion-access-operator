#!/bin/bash
set -x -e -o pipefail

CATALOGSOURCE="test-openshift-fusion-access-operator"
NS="ibm-fusion-access"
OPERATOR="openshift-fusion-access-operator"
VERSION="${VERSION:-6.6.6}"
REGISTRY="${REGISTRY:-kuemper.int.rhx/bandini}"

# Image cleanup configuration
CLEANUP_IMAGES="${CLEANUP_IMAGES:-true}"  # Set to false to disable automatic cleanup
CONTAINER_TOOL="${CONTAINER_TOOL:-podman}"

cleanup_dangling_images() {
    if [ "$CLEANUP_IMAGES" != "true" ]; then
        echo "🔧 Image cleanup disabled (CLEANUP_IMAGES=$CLEANUP_IMAGES)"
        return 0
    fi
    
    echo "🧹 Cleaning up dangling images to free disk space..."
    
    # Count images before cleanup
    IMAGES_BEFORE=$($CONTAINER_TOOL images -a | wc -l)
    echo "   Images before cleanup: $((IMAGES_BEFORE - 1))"  # Subtract header line
    
    # Remove dangling images (untagged, orphaned layers)
    echo "   Removing dangling images..."
    $CONTAINER_TOOL image prune -f || {
        echo "⚠️  Warning: Failed to prune dangling images (this is usually safe to ignore)"
    }
    
    # Remove unused images older than 24 hours (keeps recent cache layers)
    echo "   Removing unused images older than 24 hours..."
    $CONTAINER_TOOL image prune -a --filter "until=24h" -f || {
        echo "⚠️  Warning: Failed to prune old images (this is usually safe to ignore)"
    }
    
    # Count images after cleanup
    IMAGES_AFTER=$($CONTAINER_TOOL images -a | wc -l)
    CLEANED=$((IMAGES_BEFORE - IMAGES_AFTER))
    echo "   Images after cleanup: $((IMAGES_AFTER - 1))"
    echo "   ✅ Cleaned up $CLEANED dangling/unused images"
    
    # Show disk space saved
    echo "   Current disk usage:"
    $CONTAINER_TOOL system df || true
}

cleanup_build_cache() {
    if [ "$CLEANUP_IMAGES" != "true" ]; then
        return 0
    fi
    
    echo "🧹 Cleaning up build cache..."
    
    # Remove build cache (but keep base images)
    $CONTAINER_TOOL builder prune -f || {
        echo "⚠️  Note: Build cache cleanup not available (older podman version)"
    }
}

wait_for_resource() {
    local resource_type=$1  # Either "packagemanifest", "operator", or "csv"
    local name=$2           # Name of the resource (e.g., Operator or CSV)
    local namespace=$3      # Namespace (optional, required for CSV and Operator)
    local label=$4          # Label selector (only for packagemanifests)
    local max_retries=${5:-30}  # Maximum retries (default: 30 = 5 minutes)
    local retry_count=0

    echo "⏳ Waiting for $resource_type: $name"
    while [ $retry_count -lt $max_retries ]; do
        set +e
        if [[ "$resource_type" == "packagemanifest" ]]; then
            oc get -n openshift-marketplace packagemanifests -l "catalog=${label}" --field-selector "metadata.name=${name}" &> /dev/null
        elif [[ "$resource_type" == "operator" ]]; then
            oc get operators.operators.coreos.com "${name}.${namespace}" &> /dev/null
        elif [[ "$resource_type" == "csv" ]]; then
            STATUS=$(oc get csv "$name" -n "$namespace" -o jsonpath='{.status.phase}' 2>/dev/null)
            if [[ "$STATUS" == "Succeeded" ]]; then
                echo "✅ Operator installation completed successfully!"
                break
            fi
            echo "⏳ Operator installation in progress... (Current status: ${STATUS:-Not Found}, attempt $((retry_count + 1))/$max_retries)"
        else
            echo "❌ Unknown resource type: $resource_type"
            return 1
        fi
        ret=$?
        set -e

        if [[ $ret -eq 0 && "$resource_type" != "csv" ]]; then
            echo "✅ $resource_type: $name is available!"
            break
        fi

        retry_count=$((retry_count + 1))
        sleep 10
    done

    if [ $retry_count -eq $max_retries ]; then
        echo "❌ Error: $resource_type $name was not available after $max_retries attempts (5 minutes)"
        return 1
    fi
}

wait_for_catalogsource_ready() {
    local catalogsource_name=$1
    local namespace=${2:-openshift-marketplace}
    local max_retries=${3:-30}  # Maximum retries (default: 30 = 5 minutes)
    local retry_count=0

    echo "⏳ Waiting for CatalogSource ${catalogsource_name} to be fully ready..."
    while [ $retry_count -lt $max_retries ]; do
        set +e
        # Check if CatalogSource exists
        oc get catalogsource "${catalogsource_name}" -n "${namespace}" &> /dev/null
        cs_exists=$?
        
        if [ $cs_exists -ne 0 ]; then
            echo "⚠️  CatalogSource ${catalogsource_name} not found in namespace ${namespace}, retrying... (attempt $((retry_count + 1))/$max_retries)"
            retry_count=$((retry_count + 1))
            sleep 10
            continue
        fi

        # Check if pod exists and is ready
        POD_STATUS=$(oc get pod -n "${namespace}" -l "olm.catalogSource=${catalogsource_name}" -o jsonpath='{.items[0].status.phase}' 2>/dev/null)
        POD_READY=$(oc get pod -n "${namespace}" -l "olm.catalogSource=${catalogsource_name}" -o jsonpath='{.items[0].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)
        POD_NAME=$(oc get pod -n "${namespace}" -l "olm.catalogSource=${catalogsource_name}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
        
        # Check CatalogSource connection state (this is the critical check)
        CS_STATUS=$(oc get catalogsource "${catalogsource_name}" -n "${namespace}" -o jsonpath='{.status.connectionState.lastObservedState}' 2>/dev/null)
        CS_GRPC_STATUS=$(oc get catalogsource "${catalogsource_name}" -n "${namespace}" -o jsonpath='{.status.grpcConnectionState.lastObservedState}' 2>/dev/null)
        set -e

        if [ -z "${POD_NAME}" ]; then
            echo "⏳ CatalogSource pod not found yet, waiting... (attempt $((retry_count + 1))/$max_retries)"
        elif [ "${POD_STATUS}" != "Running" ]; then
            echo "⏳ CatalogSource pod ${POD_NAME} status: ${POD_STATUS}, waiting... (attempt $((retry_count + 1))/$max_retries)"
        elif [ "${POD_READY}" != "True" ]; then
            echo "⏳ CatalogSource pod ${POD_NAME} not ready yet, waiting... (attempt $((retry_count + 1))/$max_retries)"
        elif [ "${CS_STATUS}" != "READY" ] && [ "${CS_GRPC_STATUS}" != "READY" ]; then
            # Wait for connection state to be READY - this is critical for gRPC to work
            echo "⏳ CatalogSource pod ready, but connection state: ${CS_STATUS:-Unknown}/${CS_GRPC_STATUS:-Unknown}, waiting for READY... (attempt $((retry_count + 1))/$max_retries)"
            # If in TRANSIENT_FAILURE or error state, show pod details for debugging
            if [[ "${CS_STATUS}" == "TRANSIENT_FAILURE" ]] || [[ "${CS_STATUS}" == "CONNECTING" ]]; then
                echo "   Checking pod status..."
                oc get pod "${POD_NAME}" -n "${namespace}" -o jsonpath='{.status.phase}' 2>/dev/null | xargs -I {} echo "   Pod phase: {}" || true
                oc get pod "${POD_NAME}" -n "${namespace}" -o jsonpath='{.status.containerStatuses[0].state}' 2>/dev/null | xargs -I {} echo "   Container state: {}" || true
            fi
        else
            echo "✅ CatalogSource ${catalogsource_name} pod ${POD_NAME} is ready!"
            echo "✅ CatalogSource connection state: ${CS_STATUS:-${CS_GRPC_STATUS}}"
            # Wait an additional 10 seconds to ensure gRPC service is fully operational
            echo "⏳ Waiting additional 10 seconds for gRPC service to stabilize..."
            sleep 10
            break
        fi

        retry_count=$((retry_count + 1))
        sleep 10
    done

    if [ $retry_count -eq $max_retries ]; then
        echo "❌ Error: CatalogSource ${catalogsource_name} was not ready after $max_retries attempts (5 minutes)"
        echo "Checking CatalogSource status:"
        oc get catalogsource "${catalogsource_name}" -n "${namespace}" -o yaml || true
        echo ""
        echo "Checking CatalogSource pod:"
        oc get pod -n "${namespace}" -l "olm.catalogSource=${catalogsource_name}" || true
        echo ""
        echo "Checking CatalogSource pod logs (last 30 lines):"
        POD_NAME=$(oc get pod -n "${namespace}" -l "olm.catalogSource=${catalogsource_name}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
        if [ -n "${POD_NAME}" ]; then
            oc logs -n "${namespace}" "${POD_NAME}" --tail=30 || true
        fi
        return 1
    fi
}

create_catalog_pull_secret() {
    echo "Setting up image pull secret for CatalogSource..."
    # Extract registry from REGISTRY (e.g., quay.io/rh-ee-nlevanon -> quay.io)
    REGISTRY_HOST=$(echo "${REGISTRY}" | cut -d'/' -f1)
    
    # Check if we need authentication (quay.io usually requires auth)
    if [[ "${REGISTRY_HOST}" == "quay.io" ]]; then
        echo "Detected quay.io registry, setting up pull secret..."
        
        # Check if secret already exists
        if oc get secret quay-pull-secret -n openshift-marketplace &>/dev/null; then
            echo "✅ Pull secret already exists in openshift-marketplace"
        else
            echo "Creating pull secret from podman credentials..."
            # Get username from podman
            QUAY_USER=$(podman login --get-login quay.io 2>/dev/null || echo "")
            
            if [ -z "${QUAY_USER}" ]; then
                echo "⚠️  Could not get quay.io username from podman login"
                echo "⚠️  Please create the pull secret manually:"
                echo "   oc create secret docker-registry quay-pull-secret \\"
                echo "     --docker-server=quay.io \\"
                echo "     --docker-username=<your-username> \\"
                echo "     --docker-password=<your-token> \\"
                echo "     --docker-email=\"\" \\"
                echo "     -n openshift-marketplace"
                return 1
            fi
            
            # Try to extract password from auth.json if available
            AUTH_FILE="${HOME}/.config/containers/auth.json"
            if [ -f "${AUTH_FILE}" ] && command -v jq &> /dev/null; then
                QUAY_AUTH=$(jq -r ".auths.\"quay.io\".auth // empty" "${AUTH_FILE}" 2>/dev/null || echo "")
                if [ -n "${QUAY_AUTH}" ]; then
                    # Decode base64 and extract password (format: username:password)
                    DECODED=$(echo "${QUAY_AUTH}" | base64 -d 2>/dev/null || echo "")
                    QUAY_PASSWORD=$(echo "${DECODED}" | cut -d':' -f2)
                fi
            fi
            
            # If still no password, provide instructions
            if [ -z "${QUAY_PASSWORD}" ]; then
                echo "⚠️  Could not automatically extract quay.io credentials"
                echo "⚠️  Please create the pull secret manually before running this script:"
                echo ""
                echo "   oc create secret docker-registry quay-pull-secret \\"
                echo "     --docker-server=quay.io \\"
                echo "     --docker-username=${QUAY_USER} \\"
                echo "     --docker-password=<your-token-or-password> \\"
                echo "     --docker-email=\"\" \\"
                echo "     -n openshift-marketplace"
                echo ""
                echo "   oc patch serviceaccount default -n openshift-marketplace \\"
                echo "     --type='json' \\"
                echo "     -p='[{\"op\": \"add\", \"path\": \"/imagePullSecrets/-\", \"value\": {\"name\": \"quay-pull-secret\"}}]'"
                echo ""
                return 1
            fi
            
            # Create docker-registry secret
            oc create secret docker-registry quay-pull-secret \
                --docker-server=quay.io \
                --docker-username="${QUAY_USER}" \
                --docker-password="${QUAY_PASSWORD}" \
                --docker-email="" \
                -n openshift-marketplace || {
                echo "⚠️  Failed to create pull secret"
                return 1
            }
            
            echo "✅ Pull secret created successfully"
        fi
        
        # Add secret to default service account in openshift-marketplace
        echo "Adding pull secret to default service account..."
        oc patch serviceaccount default -n openshift-marketplace \
            --type='json' \
            -p='[{"op": "add", "path": "/imagePullSecrets/-", "value": {"name": "quay-pull-secret"}}]' 2>/dev/null || {
            # Check if it's already there
            if oc get serviceaccount default -n openshift-marketplace -o jsonpath='{.imagePullSecrets[*].name}' | grep -q quay-pull-secret; then
                echo "✅ Pull secret already added to default service account"
            else
                echo "⚠️  Failed to add pull secret to default service account (may already exist)"
            fi
        }
        
        echo "✅ Image pull secret configured for openshift-marketplace namespace"
        echo "   Note: Pull secret will be added to CatalogSource service account after CatalogSource is created"
    else
        echo "Registry ${REGISTRY_HOST} detected, skipping pull secret setup"
    fi
}

cleanup_old_catalogsource() {
    echo "Cleaning up old CatalogSource resources..."
    local catalogsource_name=$1
    local namespace=${2:-openshift-marketplace}
    
    # Delete old CatalogSource pods (they will be recreated automatically)
    echo "Deleting old CatalogSource pods..."
    oc delete pod -n "${namespace}" -l "olm.catalogSource=${catalogsource_name}" --ignore-not-found=true --wait=true --timeout=30s || true
    
    # Wait a moment for pods to be deleted
    sleep 5
    
    # Delete the CatalogSource itself (it will be recreated by catalog-install)
    echo "Deleting old CatalogSource resource..."
    oc delete catalogsource "${catalogsource_name}" -n "${namespace}" --ignore-not-found=true --wait=true --timeout=30s || true
    
    # Wait for cleanup to complete and ensure pods are gone
    echo "Waiting for all pods to be deleted..."
    for i in {1..10}; do
        if ! oc get pod -n "${namespace}" -l "olm.catalogSource=${catalogsource_name}" &>/dev/null; then
            break
        fi
        echo "   Waiting for pods to be deleted... (attempt $i/10)"
        sleep 2
    done
    
    # Force delete any remaining pods
    oc delete pod -n "${namespace}" -l "olm.catalogSource=${catalogsource_name}" --ignore-not-found=true --force --grace-period=0 || true
    
    echo "✅ Cleanup completed"
}

verify_catalog_image() {
    echo "Verifying catalog image exists and is accessible..."
    local catalog_image="${REGISTRY}/openshift-fusion-access-catalog:${VERSION}"
    
    # Try to pull the image locally to verify it exists
    if command -v podman &> /dev/null; then
        echo "Checking if catalog image exists: ${catalog_image}"
        if podman pull "${catalog_image}" &>/dev/null; then
            echo "✅ Catalog image exists and is accessible"
            return 0
        else
            echo "⚠️  Warning: Could not pull catalog image ${catalog_image}"
            echo "   This might be due to authentication issues or the image doesn't exist"
            echo "   The script will continue, but the CatalogSource may fail to start"
            return 1
        fi
    else
        echo "⚠️  podman not found, skipping image verification"
        return 0
    fi
}

apply_subscription() {
    echo "Creating/updating namespace and subscription resources..."
    # Delete existing subscription if it exists (this is safe to do)
    oc delete -n ${NS} subscription/${OPERATOR} || /bin/true
    # Note: We do NOT delete the CatalogSource here - it must exist in openshift-marketplace
    # for the subscription to work. The CatalogSource is created by 'catalog-install' above.
    
    oc apply -f - <<EOF
    apiVersion: v1
    kind: Namespace
    metadata:
      name: ${NS}
    spec:
EOF
    oc apply -f - <<EOF
    apiVersion: operators.coreos.com/v1
    kind: OperatorGroup
    metadata:
      name: fusion-access-operator-group
      namespace: ${NS}
    spec:
      upgradeStrategy: Default
EOF
    oc apply -f - <<EOF
    apiVersion: operators.coreos.com/v1alpha1
    kind: Subscription
    metadata:
      name: ${OPERATOR}
      namespace: ${NS}
    spec:
      channel: fast
      installPlanApproval: Automatic
      name: ${OPERATOR}
      source: ${CATALOGSOURCE}
      sourceNamespace: openshift-marketplace
EOF
}

if [[ -n $(git status --porcelain) ]]; then
    echo "Uncommitted changes detected."
    exit 1
fi

echo "Checking for cluster reachability:"
OUT=$(oc cluster-info 2>&1)
ret=$?
if [ $ret -ne 0 ]; then
    echo "Could not reach cluster: ${OUT}"
    exit 1
fi

# Clean up old images before starting to free disk space
cleanup_dangling_images

echo "🚀 Building and pushing all images..."
make VERSION=${VERSION} IMAGE_TAG_BASE=${REGISTRY}/openshift-fusion-access CHANNELS=fast USE_IMAGE_DIGESTS="" \
    manifests bundle generate docker-build docker-push bundle-build bundle-push console-build console-push \
    devicefinder-docker-build devicefinder-docker-push catalog-build catalog-push

# Clean up dangling images created during build
cleanup_dangling_images

# Verify catalog image exists before proceeding
verify_catalog_image || echo "⚠️  Image verification failed, continuing anyway..."

# Setup image pull secret for CatalogSource if needed (do this BEFORE creating CatalogSource)
create_catalog_pull_secret || echo "⚠️  Pull secret setup skipped, ensure images are publicly accessible or configure manually"

# Clean up old CatalogSource resources before creating new ones
# This must be done BEFORE creating CatalogSource to ensure clean state
cleanup_old_catalogsource "${CATALOGSOURCE}" "openshift-marketplace"

# Double-check: if CatalogSource still exists, delete it again (might have been recreated)
if oc get catalogsource "${CATALOGSOURCE}" -n openshift-marketplace &>/dev/null; then
    echo "⚠️  CatalogSource still exists after cleanup, force deleting..."
    oc delete catalogsource "${CATALOGSOURCE}" -n openshift-marketplace --ignore-not-found=true --wait=true --timeout=30s || true
    # Wait and ensure all pods are gone
    sleep 5
    oc delete pod -n openshift-marketplace -l "olm.catalogSource=${CATALOGSOURCE}" --ignore-not-found=true --force --grace-period=0 || true
fi

# Install the new CatalogSource
make VERSION=${VERSION} IMAGE_TAG_BASE=${REGISTRY}/openshift-fusion-access catalog-install

# Add pull secret to CatalogSource service account (OLM creates it when CatalogSource is created)
# This must be done AFTER CatalogSource creation but BEFORE pod starts pulling
echo "Ensuring pull secret is added to CatalogSource service account..."
REGISTRY_HOST=$(echo "${REGISTRY}" | cut -d'/' -f1)
if [[ "${REGISTRY_HOST}" == "quay.io" ]] && oc get secret quay-pull-secret -n openshift-marketplace &>/dev/null; then
    # Wait a moment for OLM to create the service account
    sleep 2
    # Ensure service account exists
    oc create serviceaccount "${CATALOGSOURCE}" -n openshift-marketplace --dry-run=client -o yaml | oc apply -f - 2>/dev/null || true
    # Add pull secret to CatalogSource service account
    oc patch serviceaccount "${CATALOGSOURCE}" -n openshift-marketplace \
        --type='json' \
        -p='[{"op": "add", "path": "/imagePullSecrets/-", "value": {"name": "quay-pull-secret"}}]' 2>/dev/null || {
        # Check if it's already there
        if oc get serviceaccount "${CATALOGSOURCE}" -n openshift-marketplace -o jsonpath='{.imagePullSecrets[*].name}' 2>/dev/null | grep -q quay-pull-secret; then
            echo "✅ Pull secret already added to CatalogSource service account"
        else
            echo "⚠️  Adding pull secret to CatalogSource service account..."
            # Try alternative method if patch fails
            oc get serviceaccount "${CATALOGSOURCE}" -n openshift-marketplace -o json | \
                jq '.imagePullSecrets = (.imagePullSecrets // []) + [{"name": "quay-pull-secret"}]' | \
                oc apply -f - 2>/dev/null || echo "⚠️  Could not add pull secret, you may need to add it manually"
        fi
    }
    # Delete any existing pods to force recreation with new service account config
    echo "Deleting any existing CatalogSource pods to force recreation with pull secret..."
    oc delete pod -n openshift-marketplace -l "olm.catalogSource=${CATALOGSOURCE}" --ignore-not-found=true --wait=true --timeout=30s || true
    
    # Force delete any stuck pods
    for pod in $(oc get pod -n openshift-marketplace -l "olm.catalogSource=${CATALOGSOURCE}" -o name 2>/dev/null || true); do
        echo "   Force deleting pod: ${pod}"
        oc delete "${pod}" -n openshift-marketplace --ignore-not-found=true --force --grace-period=0 || true
    done
    
    # Wait a moment for pods to be fully deleted
    sleep 3
    
    echo "✅ Pull secret configured for CatalogSource service account"
fi

echo "Waiting for CatalogSource to be ready before proceeding..."
wait_for_catalogsource_ready "${CATALOGSOURCE}" "openshift-marketplace"

wait_for_resource "packagemanifest" "${OPERATOR}" "" "${CATALOGSOURCE}"
apply_subscription
wait_for_resource "operator" "${OPERATOR}" "${NS}"

echo "⏳ Waiting for Subscription to install CSV..."
MAX_RETRIES=30
RETRY_COUNT=0
while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    set +e
    INSTALLED_CSV=$(oc get subscription "${OPERATOR}" -n "${NS}" -o jsonpath='{.status.installedCSV}' 2>/dev/null)
    ret=$?
    set -e
    
    if [ $ret -ne 0 ]; then
        echo "⚠️  Subscription ${OPERATOR} not found in namespace ${NS}, retrying... (attempt $((RETRY_COUNT + 1))/$MAX_RETRIES)"
    elif [ -n "${INSTALLED_CSV}" ]; then
        echo "✅ CSV installed: ${INSTALLED_CSV}"
        break
    else
        echo "⏳ Waiting for CSV to be installed... (attempt $((RETRY_COUNT + 1))/$MAX_RETRIES)"
        # Check subscription status for debugging
        set +e
        SUB_STATUS=$(oc get subscription "${OPERATOR}" -n "${NS}" -o jsonpath='{.status.conditions[?(@.type=="CatalogSourcesUnhealthy")].message}' 2>/dev/null || echo "")
        RESOLUTION_ERROR=$(oc get subscription "${OPERATOR}" -n "${NS}" -o jsonpath='{.status.conditions[?(@.type=="ResolutionFailed")].message}' 2>/dev/null || echo "")
        if [ -n "${SUB_STATUS}" ]; then
            echo "   Subscription status: ${SUB_STATUS}"
        fi
        if [ -n "${RESOLUTION_ERROR}" ]; then
            echo "   ⚠️  Resolution error: ${RESOLUTION_ERROR}"
            # If there's a resolution error, check CatalogSource pod status
            CS_POD=$(oc get pod -n openshift-marketplace -l "olm.catalogSource=${CATALOGSOURCE}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
            if [ -n "${CS_POD}" ]; then
                CS_POD_STATUS=$(oc get pod -n openshift-marketplace "${CS_POD}" -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")
                echo "   CatalogSource pod ${CS_POD} status: ${CS_POD_STATUS}"
            fi
        fi
        set -e
    fi
    
    RETRY_COUNT=$((RETRY_COUNT + 1))
    sleep 10
done

if [ -z "${INSTALLED_CSV}" ]; then
    echo "❌ Error: CSV was not installed after $MAX_RETRIES attempts (5 minutes)"
    echo "Checking subscription status:"
    oc get subscription "${OPERATOR}" -n "${NS}" -o yaml || true
    exit 1
fi

wait_for_resource "csv" "${INSTALLED_CSV}" "${NS}"

# Final cleanup to remove any remaining dangling images
echo "🎉 Build and deployment completed successfully!"
cleanup_dangling_images
cleanup_build_cache

echo "✅ All done! Operator ${OPERATOR} is now installed and running in namespace ${NS}"
echo "📊 Final system status:"
$CONTAINER_TOOL system df || true
