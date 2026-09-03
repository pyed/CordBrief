import * as fs from 'fs';

const content = fs.readFileSync('/var/cordbrief/journal/discord_events.ndjson', 'utf8');
const lines = content.trim().split('\n').filter(Boolean);

console.log('Total Lines:', lines.length);

const aMessages = [];
const bMessages = [];
const cMessages = [];
let malformed = 0;
const ids = new Set();
let duplicates = 0;

for (let i = 0; i < lines.length; i++) {
    try {
        const entry = JSON.parse(lines[i]);
        if (ids.has(entry.message_id)) {
            duplicates++;
        }
        ids.add(entry.message_id);

        if (entry.channel_id === '1545115236619518014') {
            aMessages.push(entry.content);
        } else if (entry.channel_id === '1545115308556030042') {
            bMessages.push(entry.content);
        } else if (entry.channel_id === '1545114463701835849') {
            cMessages.push(entry.content);
        }
    } catch (e) {
        malformed++;
    }
}

console.log('Results:');
console.log('A (test-a) count:', aMessages.length, aMessages);
console.log('B (test-b) count:', bMessages.length, bMessages);
console.log('C (general) count:', cMessages.length, cMessages);
console.log('Malformed lines:', malformed);
console.log('Duplicates:', duplicates);
