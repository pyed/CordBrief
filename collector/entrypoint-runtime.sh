#!/usr/bin/env bash
set -eo pipefail

echo "=== CordBrief Minimal Runtime Collector Entrypoint ==="

# 1. Private runtime directory with 0700 permissions
export XDG_RUNTIME_DIR="/tmp/runtime-cordbrief"
mkdir -p -m 0700 "$XDG_RUNTIME_DIR"

# 2. Globally disable Electron sandbox so container execution succeeds
export ELECTRON_DISABLE_SANDBOX=1
export CORDBRIEF_ROLE="collector"

# 3. Exchange and private data directory preparation
EXCHANGE_DIR="${CORDBRIEF_EXCHANGE_DIR:-/var/cordbrief/exchange}"
mkdir -p "$EXCHANGE_DIR/events"
COLLECTOR_DATA_DIR="${CORDBRIEF_COLLECTOR_DATA_DIR:-/var/lib/cordbrief}"
mkdir -p -m 0700 "$COLLECTOR_DATA_DIR"
RUNTIME_DIR="${CORDBRIEF_RUNTIME_DIR:-/var/cordbrief/runtime}"
mkdir -p "$RUNTIME_DIR"

# 4. Kernel flock(2) single ownership lease
LOCK_FILE="$RUNTIME_DIR/runtime.lock"
exec 9>"$LOCK_FILE"
if ! flock -n 9; then
    echo "[Runtime] ERROR: Runtime lock is held by another process/container ($LOCK_FILE)."
    echo "[Runtime] An ephemeral setup or another collector instance is active. Refusing concurrent execution."
    exit 1
fi
echo "[Runtime] Acquired kernel flock on $LOCK_FILE (FD 9 held by PID $$)."
cat << EOF > "$RUNTIME_DIR/runtime-owner.json"
{
  "holder": "collector",
  "pid": $$,
  "hostname": "$HOSTNAME",
  "acquired_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "instance_nonce": "$$"
}
EOF

# 5. Clean up stale X11 socket and lock files
DISPLAY_NUM="${DISPLAY:-:100}"
export DISPLAY="$DISPLAY_NUM"
NUM="${DISPLAY_NUM#:}"
rm -f "/tmp/.X${NUM}-lock" "/tmp/.X11-unix/X${NUM}" 2>/dev/null || true
rm -f "/home/cordbrief/.config/discord"/Singleton* "/home/cordbrief/.config/discord"/DevToolsActivePort 2>/dev/null || true

# 6. Check for runtime release manifest before launching graphical subsystem
MANIFEST_FILE="$RUNTIME_DIR/current/runtime-manifest.json"
if [ ! -f "$MANIFEST_FILE" ] && [ -f "$RUNTIME_DIR/runtime-manifest.json" ]; then
    MANIFEST_FILE="$RUNTIME_DIR/runtime-manifest.json"
fi

if [ ! -f "$MANIFEST_FILE" ]; then
    echo "[Runtime] WARNING: Runtime manifest not found at $MANIFEST_FILE"
    echo "[Runtime] Setup has not yet been executed. Please run:"
    echo "  docker compose --profile setup up cordbrief-setup"
    
    # Write initial setup_required status so Core UI immediately informs the operator
    node -e '
        const fs = require("fs");
        const status = {
            version: 1,
            updated_at: new Date().toISOString(),
            mode: "setup",
            collector_state: "setup_required",
            discord_authenticated: false,
            catalog_state: "unavailable",
            catalog_updated_at: null,
            watched_generation: 0,
            watched_channel_count: 0,
            active_segment: 1,
            last_event_at: null,
            last_error: "runtime_manifest_missing",
            recovery_state: "idle",
            recovery_last_at: null,
            recovery_pending_channels: 0,
            recovery_last_error: null
        };
        const dest = process.argv[1];
        const tmp = dest + "." + Date.now() + ".tmp";
        fs.writeFileSync(tmp, JSON.stringify(status, null, 2));
        fs.renameSync(tmp, dest);
    ' "$EXCHANGE_DIR/collector-status.json" 2>/dev/null || true

    # Wait for manifest to appear instead of crash-looping
    while [ ! -f "$MANIFEST_FILE" ]; do
        sleep 5
        if [ -f "$RUNTIME_DIR/current/runtime-manifest.json" ]; then
            MANIFEST_FILE="$RUNTIME_DIR/current/runtime-manifest.json"
        fi
    done
    echo "[Runtime] Detected runtime manifest! Resuming startup sequence..."
fi

# 7. Start D-Bus session bus
if [ -z "$DBUS_SESSION_BUS_ADDRESS" ]; then
    eval $(dbus-launch --sh-syntax)
    echo "[Runtime] D-Bus session bus started at: $DBUS_SESSION_BUS_ADDRESS"
fi

# 8. Initialize and unlock GNOME Keyring Daemon for Secret Service API (libsecret)
KEYRING_DIR="/home/cordbrief/.local/share/keyrings"
mkdir -p "$KEYRING_DIR"
printf 'cordbrief\n' | gnome-keyring-daemon --unlock --components=secrets 2>/dev/null || true
eval $(printf 'cordbrief\n' | gnome-keyring-daemon --start --components=secrets 2>/dev/null) || true
echo "[Runtime] GNOME Keyring Secret Service active (GNOME_KEYRING_CONTROL=$GNOME_KEYRING_CONTROL)"

# 9. Start headless Xvfb display server
echo "[Runtime] Launching headless Xvfb on display $DISPLAY_NUM..."
Xvfb "$DISPLAY_NUM" -screen 0 1280x800x24 +extension GLX +extension RANDR +extension RENDER +extension Composite -nolisten tcp -noreset &
XVFB_PID=$!

# Trap signals for graceful teardown
cleanup() {
    echo "[Runtime] Cleaning up background processes..."
    if [ -n "$OPENBOX_PID" ]; then
        kill "$OPENBOX_PID" 2>/dev/null || true
    fi
    if [ -n "$XVFB_PID" ]; then
        kill "$XVFB_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT INT TERM

# Wait for Xvfb display socket to become available
for i in $(seq 1 30); do
    if [ -S "/tmp/.X11-unix/X${NUM}" ]; then
        echo "[Runtime] Xvfb display $DISPLAY_NUM is ready (socket /tmp/.X11-unix/X${NUM})."
        break
    fi
    sleep 0.1
done

# 10. Configure Openbox window manager with auto-maximize rules
mkdir -p /home/cordbrief/.config/openbox
cat << 'EOF' > /home/cordbrief/.config/openbox/rc.xml
<?xml version="1.0" encoding="UTF-8"?>
<openbox_config xmlns="http://openbox.org/3.4/rc">
  <applications>
    <application class="*">
      <decor>yes</decor>
    </application>
    <application class="discord" type="normal">
      <maximized>true</maximized>
      <iconic>no</iconic>
      <focus>yes</focus>
    </application>
    <application class="discord" title="*Updater*">
      <maximized>false</maximized>
      <iconic>no</iconic>
      <focus>yes</focus>
    </application>
  </applications>
</openbox_config>
EOF

echo "[Runtime] Starting Openbox window manager on display $DISPLAY_NUM..."
openbox &
OPENBOX_PID=$!

# 11. Launch supervisor in runtime collector mode
echo "[Runtime] Starting runtime collector supervisor..."
exec node /home/cordbrief/supervisor.mjs
