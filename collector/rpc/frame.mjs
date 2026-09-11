/*
 * Discord IPC v1 Wire Framing Codec
 *
 * Frame structure:
 *   [opcode: 4 bytes uint32 LE]
 *   [length: 4 bytes uint32 LE]
 *   [payload: length bytes UTF-8 JSON]
 *
 * Opcodes:
 *   0: HANDSHAKE (client -> discord)
 *   1: FRAME (bidirectional commands, responses, dispatch events)
 *   2: CLOSE (bidirectional)
 *   3: PING (discord -> client)
 *   4: PONG (client -> discord)
 */

export const OPCODES = {
    HANDSHAKE: 0,
    FRAME: 1,
    CLOSE: 2,
    PING: 3,
    PONG: 4
};

/**
 * Encodes an opcode and payload object or string into a Discord IPC wire frame.
 * @param {number} opcode - Opcode (0..4)
 * @param {object|string} payload - JSON object or string
 * @returns {Buffer} Encoded wire frame
 */
export function encodeFrame(opcode, payload) {
    const jsonStr = typeof payload === "string" ? payload : JSON.stringify(payload);
    const payloadBuf = Buffer.from(jsonStr, "utf8");
    const headerBuf = Buffer.allocUnsafe(8);
    headerBuf.writeUInt32LE(opcode, 0);
    headerBuf.writeUInt32LE(payloadBuf.length, 4);
    return Buffer.concat([headerBuf, payloadBuf]);
}

/**
 * Streaming parser for Discord IPC wire frames.
 * Handles split chunks, fragmented headers, and concatenated frames.
 */
export class FrameParser {
    constructor() {
        this.buffer = Buffer.alloc(0);
    }

    /**
     * Pushes a new data chunk from the socket into the parser.
     * @param {Buffer} chunk
     * @returns {Array<{opcode: number, payload: any, raw: string}>} Complete frames parsed
     */
    push(chunk) {
        if (!chunk || chunk.length === 0) return [];
        this.buffer = this.buffer.length === 0 ? chunk : Buffer.concat([this.buffer, chunk]);

        const frames = [];
        while (this.buffer.length >= 8) {
            const opcode = this.buffer.readUInt32LE(0);
            const length = this.buffer.readUInt32LE(4);

            if (this.buffer.length < 8 + length) {
                // Incomplete frame, await more data
                break;
            }

            const rawPayload = this.buffer.subarray(8, 8 + length).toString("utf8");
            this.buffer = this.buffer.subarray(8 + length);

            let parsed = null;
            if (rawPayload.length > 0) {
                try {
                    parsed = JSON.parse(rawPayload);
                } catch (err) {
                    // ponytail: keep raw string if not valid JSON
                    parsed = null;
                }
            }

            frames.push({ opcode, length, payload: parsed, raw: rawPayload });
        }

        return frames;
    }

    /**
     * Resets parser state.
     */
    reset() {
        this.buffer = Buffer.alloc(0);
    }
}
