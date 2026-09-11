/*
 * docker_append_test_event.mjs
 * Appends a test Discord message event conforming to Schema v1 to the active segment.
 */

import * as fs from "fs";
import * as path from "path";

const exchangeDir = process.env.CORDBRIEF_EXCHANGE_DIR || "/var/cordbrief/exchange";
const eventsDir = path.join(exchangeDir, "events");
fs.mkdirSync(eventsDir, { recursive: true });

const segmentPath = path.join(eventsDir, "segment-000001.ndjson");

const msgId = String(Date.now()) + "000001";
const testEvent = {
    version: 1,
    event: "message_create",
    message_id: msgId,
    guild_id: "1545115236619518000",
    channel_id: "1545115236619518014",
    timestamp: new Date().toISOString(),
    captured_at: new Date().toISOString(),
    author: {
        id: "1545115236619518001",
        name: "cordbrief_test_user",
        display_name: "CordBrief Tester",
        bot: false
    },
    content: "RPC Docker proof verification message " + Date.now(),
    reply_to_message_id: null,
    attachments: []
};

fs.appendFileSync(segmentPath, JSON.stringify(testEvent) + "\n", "utf8");
console.log(`APPENDED_EVENT_ID:${msgId}`);
