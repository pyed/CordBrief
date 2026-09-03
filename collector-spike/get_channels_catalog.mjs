async function main() {
    const targetsRes = await fetch('http://127.0.0.1:9222/json');
    const targets = await targetsRes.json();
    const mainTarget = targets.find(t => t.url.includes('discord.com'));
    const ws = new WebSocket(mainTarget.webSocketDebuggerUrl);
    ws.onopen = () => {
        ws.send(JSON.stringify({
            id: 1,
            method: 'Runtime.evaluate',
            params: {
                expression: `(() => {
                    const W = window.Vencord?.Webpack;
                    const ChannelStore = W?.findStore('ChannelStore');
                    const guildId = "1489945055962857583";
                    let channels = [];
                    if (ChannelStore?.getMutableGuildChannelsForGuild) {
                        const map = ChannelStore.getMutableGuildChannelsForGuild(guildId);
                        channels = Object.values(map || {}).map(c => ({ id: c.id, name: c.name, type: c.type }));
                    }
                    return channels;
                })()`,
                returnByValue: true
            }
        }));
    };
    ws.onmessage = (msg) => {
        const data = JSON.parse(msg.data);
        if (data.id === 1) {
            console.log('Hidden Signal channels:\n', JSON.stringify(data.result.result.value, null, 2));
            process.exit(0);
        }
    };
}
main().catch(console.error);
