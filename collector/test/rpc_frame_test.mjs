/*
 * rpc_frame_test.mjs
 * Unit tests for Discord IPC v1 wire framing codec
 */

import assert from "assert";
import { encodeFrame, FrameParser, OPCODES } from "../rpc/frame.mjs";

function testBasicRoundtrip() {
    const payload = { v: 1, client_id: "123456789" };
    const encoded = encodeFrame(OPCODES.HANDSHAKE, payload);

    assert.strictEqual(encoded.readUInt32LE(0), OPCODES.HANDSHAKE);
    const len = encoded.readUInt32LE(4);
    assert.strictEqual(encoded.length, 8 + len);

    const parser = new FrameParser();
    const frames = parser.push(encoded);
    assert.strictEqual(frames.length, 1);
    assert.strictEqual(frames[0].opcode, OPCODES.HANDSHAKE);
    assert.deepStrictEqual(frames[0].payload, payload);
}

function testFragmentedChunks() {
    const parser = new FrameParser();
    const payload = { cmd: "DISPATCH", evt: "MESSAGE_CREATE", data: { content: "Hello world 🚀 with emojis!" } };
    const encoded = encodeFrame(OPCODES.FRAME, payload);

    // Feed 1 byte at a time to test header and payload fragmentation
    let frames = [];
    for (let i = 0; i < encoded.length; i++) {
        const slice = encoded.subarray(i, i + 1);
        const res = parser.push(slice);
        frames.push(...res);
    }

    assert.strictEqual(frames.length, 1);
    assert.strictEqual(frames[0].opcode, OPCODES.FRAME);
    assert.deepStrictEqual(frames[0].payload, payload);
}

function testMultipleFramesInOneChunk() {
    const parser = new FrameParser();
    const f1 = encodeFrame(OPCODES.PING, { nonce: "111" });
    const f2 = encodeFrame(OPCODES.FRAME, { cmd: "GET_GUILDS", nonce: "222" });
    const f3 = encodeFrame(OPCODES.CLOSE, { code: 1000, message: "Normal closure" });

    const combined = Buffer.concat([f1, f2, f3]);
    const frames = parser.push(combined);

    assert.strictEqual(frames.length, 3);
    assert.strictEqual(frames[0].opcode, OPCODES.PING);
    assert.deepStrictEqual(frames[0].payload, { nonce: "111" });
    assert.strictEqual(frames[1].opcode, OPCODES.FRAME);
    assert.deepStrictEqual(frames[1].payload, { cmd: "GET_GUILDS", nonce: "222" });
    assert.strictEqual(frames[2].opcode, OPCODES.CLOSE);
    assert.deepStrictEqual(frames[2].payload, { code: 1000, message: "Normal closure" });
}

function testMultibyteUtf8Payload() {
    const parser = new FrameParser();
    const complexStr = "日本語テスト & Русский текст & 특수문자: 𠮷野家";
    const payload = { content: complexStr };
    const encoded = encodeFrame(OPCODES.FRAME, payload);

    // Verify length is byte count, not character count
    const byteLen = Buffer.byteLength(JSON.stringify(payload), "utf8");
    assert.strictEqual(encoded.readUInt32LE(4), byteLen);

    const frames = parser.push(encoded);
    assert.strictEqual(frames.length, 1);
    assert.strictEqual(frames[0].payload.content, complexStr);
}

function runAll() {
    testBasicRoundtrip();
    testFragmentedChunks();
    testMultipleFramesInOneChunk();
    testMultibyteUtf8Payload();
    console.log("rpc_frame_test passed");
}

runAll();
