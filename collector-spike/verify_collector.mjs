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
                    const plugins = v?.Plugins?.plugins;
                    const cordbrief = plugins?.CordBriefCollector;
                    const helpers = window.VencordNative?.pluginHelpers;
                    return {
                        title: document.title,
                        pathname: location.pathname,
                        htmlLength: mount ? mount.innerHTML.length : 0,
                        hasVencord: typeof v !== "undefined",
                        hasCordBriefCollector: !!cordbrief,
                        cordbriefPluginName: cordbrief?.name,
                        cordbriefDescription: cordbrief?.description,
                        cordbriefStarted: cordbrief?.started,
                        hasNativeHelpers: !!helpers?.CordBriefCollector,
                        nativeHelpersMethods: helpers?.CordBriefCollector ? Object.keys(helpers.CordBriefCollector) : null
                    };
                })()`,
                returnByValue: true
            }
        }));
    };
    ws.onmessage = (msg) => {
        const data = JSON.parse(msg.data);
        if (data.id === 1) {
            console.log('CordBriefCollector status:\n', JSON.stringify(data.result.result.value, null, 2));
            process.exit(0);
        }
    };
}
main().catch(console.error);
