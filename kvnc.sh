#!/bin/bash
# KubeVirt VNC Connection Script - Direct or WebSocket Proxy

# Usage information
show_usage() {
    echo "Usage: ./kvnc.sh [vmi-name] [proxy|manual] [proxy-port]"
    echo "  - No VM name: Interactive VM selection"
    echo "  - Default: Direct connection using nsenter"
    echo "  - proxy: Start websocket proxy for remote access"
    echo "  - manual: Manually enter container PID"
    echo "  - Optional: Specify custom proxy port (default: 6080)"
    exit 1
}

# List and select VM interactively
list_and_select_vm() {
    echo "Fetching available KubeVirt VMs..."
    VM_LIST=$(kubectl get vmi -o custom-columns=NAME:.metadata.name --no-headers)
    
    if [ -z "$VM_LIST" ]; then
        echo "No VMs found in the current namespace."
        exit 1
    fi
    
    echo "Available VMs:"
    echo "--------------"
    
    # Create an array of VM names
    readarray -t VM_ARRAY <<< "$VM_LIST"
    
    # Display the VM list with numbers
    for i in "${!VM_ARRAY[@]}"; do
        VM=${VM_ARRAY[$i]}
        STATUS=$(kubectl get vmi $VM -o jsonpath='{.status.phase}')
        echo "[$((i+1))] $VM ($STATUS)"
    done
    
    # Prompt user to select a VM
    echo ""
    read -p "Select VM number [1-${#VM_ARRAY[@]}]: " SELECTION
    
    # Validate input
    if ! [[ "$SELECTION" =~ ^[0-9]+$ ]] || [ "$SELECTION" -lt 1 ] || [ "$SELECTION" -gt "${#VM_ARRAY[@]}" ]; then
        echo "Invalid selection. Please enter a number between 1 and ${#VM_ARRAY[@]}."
        exit 1
    fi
    
    # Set the selected VM
    VMI_NAME=${VM_ARRAY[$((SELECTION-1))]}
    echo "Selected VM: $VMI_NAME"
}

# Check arguments
if [ -z "$1" ]; then
    # No VM specified, show list for selection
    list_and_select_vm
    PROXY_MODE=${2:-"direct"}
    PROXY_PORT=${3:-6080}
else
    VMI_NAME="$1"
    PROXY_MODE=${2:-"direct"}
    PROXY_PORT=${3:-6080}
fi

# Set additional parameters
MANUAL_PID=false
[ "$PROXY_MODE" = "manual" ] && MANUAL_PID=true && PROXY_MODE="direct"
LOCAL_VNC_PORT=15901

# Working directories
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NOVNC_DIR="${SCRIPT_DIR}/noVNC"

# Get VM information
get_vm_info() {
    # Get virt-launcher pod name
    POD_NAME=$(kubectl get pods -l "kubevirt.io=virt-launcher,kubevirt.io/created-by=$(kubectl get vmi $VMI_NAME -o jsonpath='{.metadata.uid}')" -o jsonpath='{.items[0].metadata.name}')
    [ -z "$POD_NAME" ] && echo "Error: Cannot find virt-launcher pod for VMI $VMI_NAME" && exit 1
    echo "Pod found: $POD_NAME"

    # Get pod IP
    POD_IP=$(kubectl get pod $POD_NAME -o jsonpath='{.status.podIP}')
    [ -z "$POD_IP" ] && echo "Error: Cannot find IP for pod $POD_NAME" && exit 1
    echo "Pod IP: $POD_IP"

    # Get VNC port from VMI or use default
    VNC_PORT=$(kubectl get vmi $VMI_NAME -o jsonpath='{.spec.directVNCAccess.port}')
    if [ -z "$VNC_PORT" ]; then
        VNC_PORT="5901"
        echo "Using default VNC port: $VNC_PORT"
    else
        echo "Configured VNC port: $VNC_PORT"
    fi
}

# Clean up existing processes
cleanup_processes() {
    echo "Terminating existing processes..."
    sudo lsof -t -i:$LOCAL_VNC_PORT | xargs -r sudo kill -9 2>/dev/null
    sudo lsof -t -i:$PROXY_PORT | xargs -r sudo kill -9 2>/dev/null
    pgrep -f "socat.*$LOCAL_VNC_PORT" | xargs -r sudo kill -9 2>/dev/null
    pgrep -f "websockify.*$PROXY_PORT" | xargs -r sudo kill -9 2>/dev/null
    sleep 1
}

find_available_port() {
    local start_port=$1
    local current_port=$start_port
    local max_port=$((start_port + 100))  # Try up to 100 ports
    
    while [ $current_port -lt $max_port ]; do
        if ! sudo lsof -i:$current_port -P -n | grep LISTEN > /dev/null 2>&1; then
            echo $current_port
            return 0
        fi
        current_port=$((current_port + 1))
        echo "Port $((current_port - 1)) is in use, trying $current_port..." >&2
    done
    
    echo "Error: Could not find an available port in range $start_port-$max_port" >&2
    return 1
}

# Get container PID
get_container_pid() {
    # Handle manual PID entry
    if [ "$MANUAL_PID" = true ]; then
        echo "Manual PID entry mode enabled."
        read -p "Enter container PID: " CONTAINER_PID
        [[ "$CONTAINER_PID" =~ ^[0-9]+$ ]] || { echo "Error: Invalid PID"; exit 1; }
        echo "Using PID: $CONTAINER_PID"
        return
    fi
    
    echo "Searching for PID to access network namespace..."
    CONTAINER_PID=""

    # Method 0: Get PID directly from container ID
    echo "Trying to get PID from container ID..."
    CONTAINER_ID=$(kubectl get pod $POD_NAME -o jsonpath='{.status.containerStatuses[0].containerID}' | sed 's/.*:\/\///')
    if [ -n "$CONTAINER_ID" ]; then
        # Try with crictl
        if command -v crictl &>/dev/null; then
            CONTAINER_PID=$(sudo crictl inspect --output json $CONTAINER_ID | jq '.info.pid' 2>/dev/null)
            if [ -n "$CONTAINER_PID" ] && [ "$CONTAINER_PID" != "null" ]; then
                echo "PID found with crictl: $CONTAINER_PID"
                return
            fi
        fi
        
        # Try with docker
        if command -v docker &>/dev/null; then
            CONTAINER_PID=$(sudo docker inspect --format '{{.State.Pid}}' $CONTAINER_ID 2>/dev/null)
            if [ -n "$CONTAINER_PID" ] && [ "$CONTAINER_PID" != "0" ]; then
                echo "PID found with docker: $CONTAINER_PID"
                return
            fi
        fi
    fi
    
    # Method 1: Search for qemu processes
    echo "Searching for QEMU processes..."
    CONTAINER_PID=$(ps aux | grep -E "[q]emu-system" | grep -i $VMI_NAME | awk '{print $2}' | head -1)
    if [ -n "$CONTAINER_PID" ]; then
        echo "PID found with qemu-system: $CONTAINER_PID"
        return
    fi
    
    # Method 2: Search using runc/containerd
    echo "Searching for container runtime processes..."
    CONTAINER_PID=$(sudo ps ax | grep -E "[r]unc|[c]ontainerd" | grep $POD_NAME | awk '{print $1}' | head -1)
    if [ -n "$CONTAINER_PID" ]; then
        echo "PID found with runc/containerd: $CONTAINER_PID"
        return
    fi
    
    # Method 3: Scan active network namespaces (fixed grep issue)
    echo "Scanning network namespaces (may take time)..."
    for p in $(sudo find /proc -maxdepth 2 -name ns -type d | grep "/proc/[0-9]*/ns" | cut -d/ -f3); do
        if [[ "$p" =~ ^[0-9]+$ ]] && [ -d "/proc/$p" ]; then
            if sudo timeout 1 nsenter -t $p -n ping -c 1 -W 1 $POD_IP >/dev/null 2>&1; then
                echo "PID found with namespace scan: $p"
                CONTAINER_PID=$p
                return
            fi
        fi
    done
    
    # Method 4: Find the compute container in the pod
    echo "Searching for compute container..."
    for container in $(kubectl get pod $POD_NAME -o jsonpath='{.status.containerStatuses[*].name}'); do
        if [[ "$container" == "compute" ]]; then
            CONTAINER_ID=$(kubectl get pod $POD_NAME -o jsonpath="{.status.containerStatuses[?(@.name==\"compute\")].containerID}" | sed 's/.*:\/\///')
            if [ -n "$CONTAINER_ID" ]; then
                if command -v crictl &>/dev/null; then
                    CONTAINER_PID=$(sudo crictl inspect --output json $CONTAINER_ID | jq '.info.pid' 2>/dev/null)
                    if [ -n "$CONTAINER_PID" ] && [ "$CONTAINER_PID" != "null" ]; then
                        echo "PID found for compute container: $CONTAINER_PID"
                        return
                    fi
                fi
            fi
        fi
    done
    
    # If PID not found, ask for manual entry
    echo "⚠️ Cannot automatically find container PID."
    read -p "Enter PID manually (use 'ps aux | grep virt-launcher' to find it): " CONTAINER_PID
    [[ "$CONTAINER_PID" =~ ^[0-9]+$ ]] || { echo "Error: Invalid PID"; exit 1; }
}

# Set up prerequisites
setup_prerequisites() {
    # Check for socat
    if ! command -v socat &> /dev/null; then
        echo "Installing socat..."
        sudo apt-get update && sudo apt-get install -y socat || {
            echo "Error installing socat"
            exit 1
        }
    fi
    
    # Check for websockify
    if ! command -v websockify &> /dev/null; then
        echo "Installing websockify..."
        sudo apt-get update && sudo apt-get install -y python3-pip
        sudo pip3 install websockify || {
            echo "Error installing websockify"
            exit 1
        }
    fi
    
    # Install noVNC if needed
    if [ ! -d "$NOVNC_DIR" ]; then
        echo "Installing noVNC..."
        command -v git &> /dev/null || sudo apt-get update && sudo apt-get install -y git
        git clone https://github.com/novnc/noVNC.git "$NOVNC_DIR" || {
            echo "Error installing noVNC"
            exit 1
        }
    fi
}

# Start proxy mode for remote access
start_proxy_mode() {
    # Find available ports first
    local requested_proxy_port=$PROXY_PORT
    local requested_local_port=$LOCAL_VNC_PORT
    
    # Check and update LOCAL_VNC_PORT if needed
    LOCAL_VNC_PORT=$(find_available_port $LOCAL_VNC_PORT)
    if [ $? -ne 0 ]; then
        echo "Error finding available local port"
        exit 1
    fi
    
    # Check and update PROXY_PORT if needed
    PROXY_PORT=$(find_available_port $PROXY_PORT)
    if [ $? -ne 0 ]; then
        echo "Error finding available proxy port"
        exit 1
    fi
    
    # Notify if ports were changed
    if [ "$PROXY_PORT" != "$requested_proxy_port" ]; then
        echo "Note: Requested proxy port $requested_proxy_port was in use, using $PROXY_PORT instead"
    fi
    
    cleanup_processes
    setup_prerequisites
    get_container_pid
    
    echo "Creating socat tunnel with PID $CONTAINER_PID..."
    # Create a helper script for the EXEC command to avoid quoting issues
    TMP_SCRIPT=$(mktemp)
    echo "#!/bin/bash" > $TMP_SCRIPT
    echo "nsenter -t $CONTAINER_PID -n socat STDIO TCP:$POD_IP:$VNC_PORT" >> $TMP_SCRIPT
    chmod +x $TMP_SCRIPT
    
    # Use the helper script with socat
    sudo socat TCP-LISTEN:$LOCAL_VNC_PORT,bind=127.0.0.1,fork,reuseaddr EXEC:$TMP_SCRIPT &
    SOCAT_PID=$!
    
    # Verify tunnel
    sleep 2
    if ! ps -p $SOCAT_PID > /dev/null; then
        echo "Error: socat tunnel failed to start"
        rm $TMP_SCRIPT
        exit 1
    fi
    
    # Start noVNC/websockify
    echo "Starting websocket proxy on port $PROXY_PORT..."
    websockify --web=$NOVNC_DIR 0.0.0.0:$PROXY_PORT localhost:$LOCAL_VNC_PORT &
    WEBSOCKIFY_PID=$!
    
    # Verify websockify
    sleep 2
    if ! ps -p $WEBSOCKIFY_PID > /dev/null; then
        echo "Error: websockify failed to start"
        sudo kill $SOCAT_PID 2>/dev/null
        rm $TMP_SCRIPT
        exit 1
    fi
    
    # Show connection info
    SERVER_IP=$(hostname -I | awk '{print $1}')
    echo "======================================================"
    echo "VNC proxy for $VMI_NAME ready! Access VM with noVNC at:"
    echo ""
    echo "http://$SERVER_IP:$PROXY_PORT/vnc.html"
    echo ""
    echo "Press Ctrl+C to stop the proxy"
    echo "======================================================"
    
    # Termination handler
    trap 'echo "Stopping processes..."; kill $WEBSOCKIFY_PID 2>/dev/null; sudo kill $SOCAT_PID 2>/dev/null; rm '$TMP_SCRIPT'; exit 0' SIGINT SIGTERM
    
    wait $WEBSOCKIFY_PID
    rm $TMP_SCRIPT
}

# Start direct mode with nsenter
start_direct_mode() {
    get_container_pid
    echo "Direct connection to $POD_IP:$VNC_PORT with PID $CONTAINER_PID..."
    sudo nsenter -t $CONTAINER_PID -n vncviewer $POD_IP:$VNC_PORT
}

# Main execution

# Get VM information
get_vm_info

# Execute requested mode
if [ "$PROXY_MODE" = "proxy" ]; then
    start_proxy_mode
else
    start_direct_mode
fi