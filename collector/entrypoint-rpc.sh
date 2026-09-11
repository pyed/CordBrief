#!/usr/bin/env bash
set -eo pipefail

echo "=== CordBrief Official Discord RPC Collector Entrypoint ==="

# 1. Private runtime directory with 0700 permissions
export XDG_RUNTIME_DIR="/tmp/runtime-cordbrief"
mkdir -p -m 0700 "$XDG_RUNTIME_DIR"
export ELECTRON_DISABLE_SANDBOX=1
export CORDBRIEF_ROLE="collector"

# 2. Exchange and private directories
EXCHANGE_DIR="${CORDBRIEF_EXCHANGE_DIR:-/var/cordbrief/exchange}"
mkdir -p "$EXCHANGE_DIR/events"
COLLECTOR_DATA_DIR="${CORDBRIEF_COLLECTOR_DATA_DIR:-/var/lib/cordbrief}"
mkdir -p -m 0700 "$COLLECTOR_DATA_DIR"
RUNTIME_DIR="${CORDBRIEF_RUNTIME_DIR:-/var/cordbrief/runtime}"
mkdir -p "$RUNTIME_DIR"

# 3. Kernel flock(2) single-ownership lease
LOCK_FILE="$RUNTIME_DIR/runtime.lock"
exec 9>"$LOCK_FILE"
if ! flock -n 9; then
    echo "[Runtime] ERROR: Runtime lock is held by another process/container ($LOCK_FILE)."
    exit 1
fi
echo "[Runtime] Acquired kernel flock on $LOCK_FILE (FD 9 held by PID $$)."

# 4. Clean up stale X11 socket, lock files, and stale IPC sockets
DISPLAY_NUM="${DISPLAY:-:100}"
export DISPLAY="$DISPLAY_NUM"
NUM="${DISPLAY_NUM#:}"
rm -f "/tmp/.X${NUM}-lock" "/tmp/.X11-unix/X${NUM}" 2>/dev/null || true
rm -f "$XDG_RUNTIME_DIR"/discord-ipc-* 2>/dev/null || true
rm -f "/home/cordbrief/.config/discord"/Singleton* "/home/cordbrief/.config/discord"/DevToolsActivePort 2>/dev/null || true

# 5. D-Bus session bus
if [ -z "$DBUS_SESSION_BUS_ADDRESS" ]; then
    eval $(dbus-launch --sh-syntax)
    echo "[Runtime] D-Bus session bus started at: $DBUS_SESSION_BUS_ADDRESS"
fi

# 6. GNOME Keyring Secret Service
KEYRING_DIR="/home/cordbrief/.local/share/keyrings"
mkdir -p "$KEYRING_DIR"
printf 'cordbrief\n' | gnome-keyring-daemon --unlock --components=secrets 2>/dev/null || true
eval $(printf 'cordbrief\n' | gnome-keyring-daemon --start --components=secrets 2>/dev/null) || true
echo "[Runtime] GNOME Keyring Secret Service active."

# 7. Start headless Xvfb
echo "[Runtime] Launching headless Xvfb on display $DISPLAY_NUM..."
Xvfb "$DISPLAY_NUM" -screen 0 1280x800x24 +extension GLX +extension RANDR +extension RENDER +extension Composite -nolisten tcp -noreset &
XVFB_PID=$!

# Wait for Xvfb display socket
for i in $(seq 1 30); do
    if [ -S "/tmp/.X11-unix/X${NUM}" ]; then
        echo "[Runtime] Xvfb display $DISPLAY_NUM is ready."
        break
    fi
    sleep 0.1
done

# 8. Start Openbox
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
      <focus>yes</focus>
    </application>
    <application class="Discord" type="normal">
      <maximized>true</maximized>
      <focus>yes</focus>
    </application>
  </applications>
</openbox_config>
EOF
openbox &
OPENBOX_PID=$!

# 9. Start Xpra shadow on port 28742 for interactive approval viewing
XPRA_PORT="${XPRA_PORT:-28742}"
if command -v xpra >/dev/null 2>&1; then
    echo "[Runtime] Starting Xpra shadow viewer on 0.0.0.0:$XPRA_PORT..."
    xpra shadow "$DISPLAY_NUM" --bind-tcp="0.0.0.0:$XPRA_PORT" --html=on --daemon=yes --notifications=no --bell=no 2>/dev/null || true
fi

# 10. Start official, unmodified Discord desktop client with auto-relaunch for updates
echo "[Runtime] Launching official unmodified Discord desktop client supervisor..."
run_discord() {
    while true; do
        if [ -x "/home/cordbrief/.config/discord/Discord" ]; then
            BIN="/home/cordbrief/.config/discord/Discord"
        elif [ -x "/usr/share/discord/Discord" ]; then
            BIN="/usr/share/discord/Discord"
        elif command -v discord >/dev/null 2>&1; then
            BIN="$(command -v discord)"
        else
            echo "[Runtime] ERROR: Discord binary not found!"
            sleep 5
            continue
        fi
        echo "[Runtime] Starting Discord ($BIN)..."
        "$BIN" --disable-gpu-sandbox --no-sandbox
        STATUS=$?
        echo "[Runtime] Discord process exited with code $STATUS, restarting in 2s..."
        sleep 2
    done
}
run_discord &
DISCORD_PID=$!

# Watchdog to ensure headless Discord window receives initial focus/activation in Xvfb
run_window_activator() {
    local count=0
    while true; do
        sleep 3
        if [ ! -S "$XDG_RUNTIME_DIR/discord-ipc-0" ]; then
            count=$((count + 1))
            if [ "$count" -ge 3 ]; then
                WID=$(xdotool search --class discord 2>/dev/null | tail -n 1)
                if [ -n "$WID" ]; then
                    xdotool windowactivate "$WID" key --window "$WID" ctrl+r 2>/dev/null || true
                fi
                count=0
            fi
        else
            count=0
            sleep 30
        fi
    done
}
run_window_activator &
ACTIVATOR_PID=$!

cleanup() {
    echo "[Runtime] Cleaning up background processes..."
    kill "$ACTIVATOR_PID" 2>/dev/null || true
    kill "$DAEMON_PID" 2>/dev/null || true
    kill "$DISCORD_PID" 2>/dev/null || true
    pkill -u cordbrief -f "Discord" 2>/dev/null || true
    kill "$OPENBOX_PID" 2>/dev/null || true
    kill "$XVFB_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# 11. Run the RPC Collector Daemon with supervisor loop
run_daemon() {
    while true; do
        echo "[Runtime] Starting CordBrief RPC collector daemon..."
        node /home/cordbrief/collector/rpc/daemon.mjs || true
        echo "[Runtime] Collector daemon exited, restarting in 2s..."
        sleep 2
    done
}
run_daemon &
DAEMON_PID=$!

wait $DISCORD_PID



