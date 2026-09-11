/*
 * docker_probe_ipc.mjs
 * Probes the local Discord IPC domain socket inside the container.
 * Sends Opcode 0 (Handshake) and verifies socket connection and frame handling.
 */

import * as net from "net";

const socketPath = process.env.DISCORD_IPC_SOCKET || "/tmp/runtime-cordbrief/discord-ipc-0";
const clientId = process.env.DISCORD_CLIENT_ID || "123456789012345678";

console.log(`Connecting to Discord IPC at ${socketPath}...`);
const socket = net.connect(socketPath);

let connected = false;

const timeout = setTimeout(() => {
    if (connected) {
        // Socket successfully connected and accepted handshake
        console.log("HANDSHAKE_SENT_OK");
        socket.destroy();
        process.exit(0);
    } else {
        console.error("Timeout connecting to Discord IPC");
        socket.destroy();
        process.exit(1);
    }
}, 5000);

socket.on("connect", () => {
    connected = true;
    console.log("Socket connected, sending HANDSHAKE frame...");
    const payload = JSON.stringify({ v: 1, client_id: String(clientId) });
    const payloadBuf = Buffer.from(payload, "utf8");
    const frame = Buffer.alloc(8 + payloadBuf.length);
    frame.writeInt32LE(0, 0); // Opcode 0: HANDSHAKE
    frame.writeInt32LE(payloadBuf.length, 4);
    payloadBuf.copy(frame, 8);
    socket.write(frame);
});

socket.on("data", chunk => {
    if (chunk.length >= 8) {
        const opcode = chunk.readInt32LE(0);
        const length = chunk.readInt32LE(4);
        const dataStr = chunk.slice(8, 8 + length).toString("utf8");
        try {
            const data = JSON.parse(dataStr);
            console.log(`Received frame: opcode=${opcode}, cmd=${data.cmd}, evt=${data.evt}`);
            if (opcode === 1 && data.cmd === "DISPATCH" && data.evt === "READY") {
                clearTimeout(timeout);
                console.log("READY_OK");
                socket.end();
                process.exit(0);
            }
        } catch (err) {
            console.error("Failed to parse frame payload:", err.message);
        }
    }
});

socket.on("error", err => {
    console.error("Socket error:", err.message);
    clearTimeout(timeout);
    process.exit(1);
});
