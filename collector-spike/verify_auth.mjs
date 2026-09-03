async function main() {
    const targetsRes = await fetch('http://127.0.0.1:9222/json');
    const targets = await targetsRes.json();
    const mainTarget = targets.find(t => t.url.includes('discord.com'));
    if (!mainTarget) {
        console.error('No Discord target found:', targets);
        process.exit(1);
    }
    const ws = new WebSocket(mainTarget.webSocketDebuggerUrl);
    ws.onopen = () => {
        ws.send(JSON.stringify({
            id: 1,
            method: 'Runtime.evaluate',
            params: {
                expression: `(() => {
                    const mount = document.getElementById("app-mount");
                    const v = window.Vencord;
                    return {
                        title: document.title,
                        pathname: location.pathname,
                        htmlLength: mount ? mount.innerHTML.length : 0,
                        hasVencord: typeof v !== "undefined",
                        vencordVersion: v ? v.version : null,
                        hasWebpack: !!v?.Webpack,
                        samplePlugins: v ? Object.keys(v.Plugins?.plugins || {}).slice(0, 10) : []
                    };
                })()`,
                returnByValue: true
            }
        }));
    };
    ws.onmessage = (msg) => {
        const data = JSON.parse(msg.data);
        if (data.id === 1) {
            console.log('Discord + Vencord status:\n', JSON.stringify(data.result.result.value, null, 2));
            process.exit(0);
        }
    };
}
main().catch(console.error);
