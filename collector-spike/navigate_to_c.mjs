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
                    const nav = W?.findByProps('transitionTo');
                    if (nav && nav.transitionTo) {
                        nav.transitionTo('/channels/1545114461868658862/1545114463701835849');
                        return { method: 'transitionTo', path: location.pathname };
                    } else {
                        location.href = '/channels/1545114461868658862/1545114463701835849';
                        return { method: 'location.href', path: location.pathname };
                    }
                })()`,
                returnByValue: true
            }
        }));
    };
    ws.onmessage = (msg) => {
        const data = JSON.parse(msg.data);
        if (data.id === 1) {
            console.log('Navigation result:\n', JSON.stringify(data.result.result.value, null, 2));
            process.exit(0);
        }
    };
}
main().catch(console.error);
