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
                expression: `(async () => {
                    const Native = window.VencordNative?.pluginHelpers?.CordBriefCollector;
                    const watched = Native ? await Native.getWatchedChannelsConfig() : null;
                    const journal = Native ? await Native.getJournalPathConfig() : null;
                    return {
                        watched,
                        journal,
                        started: window.Vencord?.Plugins?.plugins?.CordBriefCollector?.started
                    };
                })()`,
                awaitPromise: true,
                returnByValue: true
            }
        }));
    };
    ws.onmessage = (msg) => {
        const data = JSON.parse(msg.data);
        if (data.id === 1) {
            console.log('Collector config in runtime:\n', JSON.stringify(data.result.result.value, null, 2));
            process.exit(0);
        }
    };
}
main().catch(console.error);
