/*
 * Mock Discord RPC Server for Automated Testing
 * Emulates official Discord desktop client IPC v1 transport and RPC protocol.
 */

import * as net from "net";
import * as path from "path";
import * as os from "os";
import * as fs from "fs";
import { encodeFrame, FrameParser, OPCODES } from "../rpc/frame.mjs";

export class MockDiscordRpcServer {
    constructor(options = {}) {
        this.server = null;
        this.pipePath = null;
        this.clients = new Set();
        this.subscriptions = new Set(); // set of "channelId:MESSAGE_CREATE"

        // Configurable mock data
        this.guilds = options.guilds || [
            { id: "1001", name: "Alpha Guild" },
            { id: "1002", name: "Beta Guild" }
        ];

        this.channelsByGuild = options.channelsByGuild || {
            "1001": [
                { id: "2001", name: "general", type: 0 },
                { id: "2002", name: "announcements", type: 0 }
            ],
            "1002": [
                { id: "3001", name: "dev", type: 0 }
            ]
        };

        this.channelData = options.channelData || {};
    }

    /**
     * Starts listening on a unique local named pipe (Windows) or UNIX domain socket (Linux).
     * @returns {Promise<string>} Bound socket/pipe path
     */
    async start() {
        return new Promise((resolve, reject) => {
            const rand = Math.random().toString(36).slice(2, 10);
            if (process.platform === "win32") {
                this.pipePath = `\\\\.\\pipe\\cordbrief-mock-rpc-${rand}`;
            } else {
                this.pipePath = path.join(os.tmpdir(), `cordbrief-mock-rpc-${rand}.sock`);
                try {
                    if (fs.existsSync(this.pipePath)) fs.unlinkSync(this.pipePath);
                } catch {}
            }

            this.server = net.createServer(socket => this._handleClient(socket));

            this.server.listen(this.pipePath, () => {
                resolve(this.pipePath);
            });

            this.server.on("error", reject);
        });
    }

    _handleClient(socket) {
        this.clients.add(socket);
        const parser = new FrameParser();

        socket.on("data", chunk => {
            const frames = parser.push(chunk);
            for (const frame of frames) {
                this._processFrame(socket, frame);
            }
        });

        socket.on("close", () => {
            this.clients.delete(socket);
        });

        socket.on("error", () => {
            this.clients.delete(socket);
        });
    }

    _processFrame(socket, frame) {
        const { opcode, payload } = frame;

        if (opcode === OPCODES.HANDSHAKE) {
            // Send READY dispatch
            const readyPayload = {
                cmd: "DISPATCH",
                evt: "READY",
                data: {
                    v: 1,
                    config: {
                        cdn_host: "cdn.discordapp.com",
                        api_endpoint: "//discord.com/api",
                        environment: "production"
                    },
                    user: {
                        id: "999888777",
                        username: "cordbrief_test_user",
                        discriminator: "0",
                        global_name: "CordBrief Test User",
                        bot: false
                    }
                }
            };
            socket.write(encodeFrame(OPCODES.FRAME, readyPayload));
            return;
        }

        if (opcode === OPCODES.PING) {
            socket.write(encodeFrame(OPCODES.PONG, payload));
            return;
        }

        if (opcode === OPCODES.FRAME && payload) {
            const nonce = payload.nonce;
            const cmd = payload.cmd;
            const args = payload.args || {};

            if (cmd === "AUTHORIZE") {
                socket.write(encodeFrame(OPCODES.FRAME, {
                    cmd: "AUTHORIZE",
                    data: { code: "mock_auth_code_" + Math.random().toString(36).slice(2) },
                    nonce
                }));
                return;
            }

            if (cmd === "AUTHENTICATE") {
                socket.write(encodeFrame(OPCODES.FRAME, {
                    cmd: "AUTHENTICATE",
                    data: {
                        user: { id: "999888777", username: "cordbrief_test_user" },
                        scopes: ["rpc", "identify", "messages.read"],
                        expires: new Date(Date.now() + 86400000).toISOString()
                    },
                    nonce
                }));
                return;
            }

            if (cmd === "GET_GUILDS") {
                socket.write(encodeFrame(OPCODES.FRAME, {
                    cmd: "GET_GUILDS",
                    data: { guilds: this.guilds },
                    nonce
                }));
                return;
            }

            if (cmd === "GET_CHANNELS") {
                const channels = this.channelsByGuild[args.guild_id] || [];
                socket.write(encodeFrame(OPCODES.FRAME, {
                    cmd: "GET_CHANNELS",
                    data: { channels },
                    nonce
                }));
                return;
            }

            if (cmd === "SUBSCRIBE") {
                const key = `${args.channel_id}:${payload.evt || "MESSAGE_CREATE"}`;
                this.subscriptions.add(key);
                socket.write(encodeFrame(OPCODES.FRAME, {
                    cmd: "SUBSCRIBE",
                    evt: payload.evt,
                    data: { evt: payload.evt },
                    nonce
                }));
                return;
            }

            if (cmd === "UNSUBSCRIBE") {
                const key = `${args.channel_id}:${payload.evt || "MESSAGE_CREATE"}`;
                this.subscriptions.delete(key);
                socket.write(encodeFrame(OPCODES.FRAME, {
                    cmd: "UNSUBSCRIBE",
                    evt: payload.evt,
                    data: { evt: payload.evt },
                    nonce
                }));
                return;
            }

            if (cmd === "GET_CHANNEL") {
                const ch = this.channelData[args.channel_id] || {
                    id: args.channel_id,
                    name: "channel-" + args.channel_id,
                    type: 0,
                    messages: []
                };
                socket.write(encodeFrame(OPCODES.FRAME, {
                    cmd: "GET_CHANNEL",
                    data: ch,
                    nonce
                }));
                return;
            }
        }
    }

    /**
     * Broadcasts a live MESSAGE_CREATE dispatch event to all subscribed sockets.
     * @param {string} channelId
     * @param {object} message
     */
    dispatchMessage(channelId, message) {
        const dispatchPayload = {
            cmd: "DISPATCH",
            evt: "MESSAGE_CREATE",
            data: {
                channel_id: String(channelId),
                message
            }
        };
        const encoded = encodeFrame(OPCODES.FRAME, dispatchPayload);
        for (const socket of this.clients) {
            try {
                socket.write(encoded);
            } catch {}
        }
    }

    /**
     * Stops the mock server and cleans up socket file.
     */
    async stop() {
        for (const socket of this.clients) {
            try { socket.destroy(); } catch {}
        }
        this.clients.clear();

        return new Promise(resolve => {
            if (this.server) {
                this.server.close(() => {
                    if (process.platform !== "win32" && this.pipePath) {
                        try { if (fs.existsSync(this.pipePath)) fs.unlinkSync(this.pipePath); } catch {}
                    }
                    resolve();
                });
            } else {
                resolve();
            }
        });
    }
}
