#!/bin/bash
#
# Install netcfg as cloud-init network renderer
#

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Detect cloud-init renderer directory
CLOUDINIT_RENDERER_DIR=""
for dir in \
    "/usr/lib/python3/dist-packages/cloudinit/net/renderers" \
    "/usr/lib/python3.*/site-packages/cloudinit/net/renderers" \
    "/usr/local/lib/python3.*/dist-packages/cloudinit/net/renderers"; do
    found=$(ls -d $dir 2>/dev/null | head -1)
    if [ -n "$found" ] && [ -d "$found" ]; then
        CLOUDINIT_RENDERER_DIR="$found"
        break
    fi
done

if [ -z "$CLOUDINIT_RENDERER_DIR" ]; then
    echo "Error: cloud-init renderers directory not found"
    echo "Is cloud-init installed?"
    exit 1
fi

echo "Installing netcfg cloud-init renderer..."

# Copy renderer
cp "$SCRIPT_DIR/netcfg.py" "$CLOUDINIT_RENDERER_DIR/"
echo "  Installed: $CLOUDINIT_RENDERER_DIR/netcfg.py"

# Register renderer in __init__.py
INIT_FILE="$CLOUDINIT_RENDERER_DIR/__init__.py"
if [ -f "$INIT_FILE" ]; then
    if ! grep -q '"netcfg"' "$INIT_FILE"; then
        # Add netcfg to NAME_TO_RENDERER
        if grep -q "NAME_TO_RENDERER" "$INIT_FILE"; then
            # Backup
            cp "$INIT_FILE" "$INIT_FILE.bak"
            
            # Try to add netcfg to the renderer list
            # This is fragile and may need manual adjustment
            echo "  Note: You may need to manually add 'netcfg' to NAME_TO_RENDERER in $INIT_FILE"
        fi
    else
        echo "  netcfg already registered in $INIT_FILE"
    fi
fi

# Configure cloud-init to use netcfg
CLOUD_CFG="/etc/cloud/cloud.cfg"
CLOUD_CFG_D="/etc/cloud/cloud.cfg.d"

if [ -d "$CLOUD_CFG_D" ]; then
    cat > "$CLOUD_CFG_D/99-netcfg-renderer.cfg" << 'EOF'
# Use netcfg as the primary network renderer
network:
  renderers: ['netcfg', 'netplan', 'eni', 'sysconfig', 'networkd']
EOF
    echo "  Created: $CLOUD_CFG_D/99-netcfg-renderer.cfg"
else
    echo "  Warning: $CLOUD_CFG_D not found, manual configuration required"
    echo "  Add the following to $CLOUD_CFG:"
    echo ""
    echo "    network:"
    echo "      renderers: ['netcfg', 'netplan', 'eni', 'sysconfig']"
fi

echo ""
echo "Installation complete!"
echo ""
echo "Verify with:"
echo "  cloud-init query --list-keys"
echo "  cloud-init status"
echo ""
echo "Test renderer:"
echo "  cloud-init clean --logs"
echo "  cloud-init init --local"
