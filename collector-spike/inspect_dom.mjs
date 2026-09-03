async function main() {
    const targetsRes = await fetch('http://127.0.0.1:9222/json');
    const targets = await targetsRes.json();
    const mainTarget = targets.find(t => t.url.includes('discord.com'));
    const ws = new WebSocket(mainTarget.webSocketDebuggerUrl);
    ws.onopen = () => {
        ws.send(JSON.stringify({ id: 1, method: 'Network.enable' }));
        ws.send(JSON.stringify({ id: 2, method: 'Console.enable' }));
        ws.send(JSON.stringify({ id: 3, method: 'Runtime.enable' }));
        setTimeout(() => {
            console.log('Reloading page...');
            ws.send(JSON.stringify({ id: 4, method: 'Page.reload' }));
        }, 500);
    };
    ws.onmessage = (msg) => {
        const data = JSON.parse(msg.data);
        if (data.method === 'Network.loadingFailed') {
            console.log('[NET FAIL]', data.params.errorText, data.params.requestId);
        } else if (data.method === 'Console.messageAdded') {
            console.log('[CONSOLE]', data.params.message.text);
        } else if (data.method === 'Runtime.exceptionThrown') {
            console.log('[EXCEPTION]', JSON.stringify(data.params.exceptionDetails));
        } else if (data.method === 'Network.responseReceived') {
            const url = data.params.response.url;
            if (url.includes('web.') || url.includes('login')) {
                console.log('[NET RESP]', data.params.response.status, url);
            }
        } else if (data.method === 'Network.loadingFinished') {
            // Check if web chunk finished
        }
    };
    setTimeout(() => {
        console.log('Capture timeout (10s).');
        process.exit(0);
    }, 10000);
}
main().catch(console.error);
