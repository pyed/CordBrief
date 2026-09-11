/*
 * Discord IPC Transport
 * Manages low-level socket connection, wire framing, ping/pong, and nonce request matching.
 */

import * as net from "net";
import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import * as crypto from "crypto";
import { EventEmitter } from "events";
import { encodeFrame, FrameParser, OPCODES } from "./frame.mjs";

/**
 * Discovers the active Discord local IPC socket/pipe path across platforms.
 * @returns {string|null} Resolved path or null if not found
 */
export function findDiscordIPCPath() {
    if (process.env.DISCORD_IPC_PATH) {
        return process.env.DISCORD_IPC_PATH;
    }

    if (process.platform === "win32") {
        // Windows uses named pipes: \\.\pipe\discord-ipc-[0..9]
        // In Node.js, connecting to the pipe path directly checks existence
        // ponytail: default to index 0 on Windows unless overridden
        return "\\\\.\\pipe\\discord-ipc-0";
    }

    // Unix / Linux: check XDG_RUNTIME_DIR, TMPDIR, /tmp, /run/user/<uid>
    const candidates = [];
    if (process.env.XDG_RUNTIME_DIR) candidates.push(process.env.XDG_RUNTIME_DIR);
    if (process.env.TMPDIR) candidates.push(process.env.TMPDIR);
    candidates.push("/tmp/runtime-cordbrief");
    candidates.push("/tmp");
    try {
        const uid = process.getuid ? process.getuid() : 1000;
        candidates.push(`/run/user/${uid}`);
    } catch {}

    for (const dir of candidates) {
        for (let i = 0; i < 10; i++) {
            const socketPath = path.join(dir, `discord-ipc-${i}`);
            try {
                if (fs.existsSync(socketPath)) {
                    return socketPath;
                }
            } catch {}
        }
    }

    // Fallback default
    return path.join(process.env.XDG_RUNTIME_DIR || "/tmp", "discord-ipc-0");
}

export class RpcTransport extends EventEmitter {
    constructor(options = {}) {
        super();
        this.socketPath = options.socketPath || null;
        this.socket = null;
        this.parser = new FrameParser();
        this.pendingRequests = new Map(); // nonce -> { resolve, reject, timer }
        this.handshakeDeferred = null;
        this.connected = false;
        this.readyData = null;
    }

    /**
     * Connects to Discord IPC socket and performs protocol handshake.
     * @param {string} clientId - Discord Application Client ID
     * @param {object} [options]
     * @returns {Promise<object>} READY data
     */
    async connect(clientId, options = {}) {
        if (this.connected && this.readyData) {
            return this.readyData;
        }

        const socketPath = this.socketPath || findDiscordIPCPath();
        const timeoutMs = options.timeoutMs || 30000;

        return new Promise((resolve, reject) => {
            const connectTimer = setTimeout(() => {
                this.close();
                reject(new Error(`Timed out connecting to Discord IPC at ${socketPath}`));
            }, timeoutMs);

            this.socket = net.connect(socketPath);

            this.socket.on("connect", () => {
                this.connected = true;
                // Perform Handshake (Opcode 0)
                this.handshakeDeferred = { resolve, reject, connectTimer };
                const handshakePayload = { v: 1, client_id: String(clientId) };
                this.socket.write(encodeFrame(OPCODES.HANDSHAKE, handshakePayload));
            });

            this.socket.on("data", chunk => {
                const frames = this.parser.push(chunk);
                for (const frame of frames) {
                    this._handleFrame(frame);
                }
            });

            this.socket.on("error", err => {
                if (this.handshakeDeferred) {
                    clearTimeout(this.handshakeDeferred.connectTimer);
                    const reject = this.handshakeDeferred.reject;
                    this.handshakeDeferred = null;
                    reject(err);
                }
                if (this.listenerCount("error") > 0) {
                    this.emit("error", err);
                }
            });

            this.socket.on("close", hadError => {
                this.connected = false;
                this._rejectAllPending(new Error("Discord IPC connection closed"));
                if (this.listenerCount("close") > 0) {
                    this.emit("close", hadError);
                }
            });
        });
    }

    _handleFrame(frame) {
        const { opcode, payload } = frame;

        if (opcode === OPCODES.PING) {
            // Heartbeat ping: reply with PONG
            this.socket.write(encodeFrame(OPCODES.PONG, payload));
            return;
        }

        if (opcode === OPCODES.CLOSE) {
            this.emit("close", false);
            this.close();
            return;
        }

        if (opcode === OPCODES.FRAME && payload) {
            // Check if this is the READY dispatch during handshake
            if (payload.cmd === "DISPATCH" && payload.evt === "READY") {
                this.readyData = payload.data;
                if (this.handshakeDeferred) {
                    clearTimeout(this.handshakeDeferred.connectTimer);
                    const resolve = this.handshakeDeferred.resolve;
                    this.handshakeDeferred = null;
                    resolve(payload.data);
                }
                this.emit("ready", payload.data);
                return;
            }

            // Check if this matches a pending nonce request
            if (payload.nonce && this.pendingRequests.has(payload.nonce)) {
                const req = this.pendingRequests.get(payload.nonce);
                this.pendingRequests.delete(payload.nonce);
                clearTimeout(req.timer);

                if (payload.evt === "ERROR") {
                    const err = new Error(payload.data?.message || "Discord RPC error");
                    err.code = payload.data?.code;
                    req.reject(err);
                } else {
                    req.resolve(payload.data);
                }
                return;
            }

            // Check if this is a live event dispatch
            if (payload.cmd === "DISPATCH" && payload.evt) {
                this.emit("dispatch", { evt: payload.evt, data: payload.data });
                return;
            }
        }
    }

    /**
     * Sends an RPC command with a nonce and awaits the matching response.
     * @param {string} cmd
     * @param {object} args
     * @param {string|null} evt
     * @param {number} [timeoutMs=15000]
     * @returns {Promise<any>} Response data
     */
    async request(cmd, args = {}, evt = null, timeoutMs = 15000) {
        if (!this.connected || !this.socket) {
            throw new Error("Discord IPC is not connected");
        }

        const nonce = crypto.randomUUID();
        const payload = { cmd, args, nonce };
        if (evt) payload.evt = evt;

        return new Promise((resolve, reject) => {
            const timer = setTimeout(() => {
                if (this.pendingRequests.has(nonce)) {
                    this.pendingRequests.delete(nonce);
                    reject(new Error(`Discord RPC request timed out for cmd ${cmd} (nonce ${nonce})`));
                }
            }, timeoutMs);

            this.pendingRequests.set(nonce, { resolve, reject, timer });
            this.socket.write(encodeFrame(OPCODES.FRAME, payload));
        });
    }

    _rejectAllPending(err) {
        for (const [nonce, req] of this.pendingRequests.entries()) {
            clearTimeout(req.timer);
            req.reject(err);
        }
        this.pendingRequests.clear();
    }

    close() {
        this.connected = false;
        if (this.socket) {
            try {
                this.socket.destroy();
            } catch {}
            this.socket = null;
        }
        this.parser.reset();
        this._rejectAllPending(new Error("Discord IPC connection closed"));
    }
}
