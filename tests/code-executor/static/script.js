let editor;
let term;
let fitAddon;
let ws;
let statsInterval;

// Initialize Monaco Editor
require.config({ paths: { vs: 'https://cdnjs.cloudflare.com/ajax/libs/monaco-editor/0.36.1/min/vs' } });
require(['vs/editor/editor.main'], function () {
    editor = monaco.editor.create(document.getElementById('monaco-container'), {
        value: '# Your Python code here\nprint("Hello from Minibox Pro!")\nname = input("Enter your name: ")\nprint(f"Nice to meet you, {name}!")',
        language: 'python',
        theme: 'vs-dark',
        automaticLayout: true,
        fontSize: 14,
        fontFamily: 'JetBrains Mono',
        minimap: { enabled: false },
        padding: { top: 16 }
    });
});

// Initialize xterm.js
term = new Terminal({
    cursorBlink: true,
    theme: {
        background: '#000000',
        foreground: '#ffffff',
    },
    fontFamily: 'JetBrains Mono',
    fontSize: 13,
});
fitAddon = new FitAddon.FitAddon();
term.loadAddon(fitAddon);
term.open(document.getElementById('xterm-container'));
fitAddon.fit();

window.addEventListener('resize', () => fitAddon.fit());

// Handle Language Switch
document.getElementById('language-select').addEventListener('change', (e) => {
    const lang = e.target.value;
    let code = '';
    let monacoLang = lang;

    if (lang === 'python') {
        code = 'import sys\nprint("Hello from Minibox Pro!")\nsys.stdout.write("Enter your name: ")\nsys.stdout.flush()\nname = sys.stdin.readline().strip()\nprint(f"Nice to meet you, {name}!")';
    } else if (lang === 'nodejs') {
        code = '// Your Node.js code here\nconsole.log("Hello from Minibox Pro!");\nprocess.stdout.write("Enter your name: ");\nprocess.stdin.once("data", (data) => {\n    console.log(`Nice to meet you, ${data.toString().trim()}!`);\n    process.exit();\n});';
        monacoLang = 'javascript';
    } else if (lang === 'shell') {
        code = '# Your Shell code here\necho "Hello from Minibox Pro!"\necho -n "Enter your name: "\nread name\necho "Nice to meet you, $name!"';
    }

    monaco.editor.setModelLanguage(editor.getModel(), monacoLang);
    editor.setValue(code);
});

// Run Button Action
document.getElementById('run-btn').addEventListener('click', runCode);
document.getElementById('stop-btn').addEventListener('click', stopCode);

// Send input to terminal
term.onData(data => {
    if (ws && ws.readyState === WebSocket.OPEN) {
        // Map Carriage Return (Enter) to CRLF (\r\n) for PTY compatibility
        const processedData = data.replace(/\r/g, '\r\n');
        ws.send(JSON.stringify({ type: 'input', data: processedData }));
    }
});

function runCode() {
    const language = document.getElementById('language-select').value;
    const code = editor.getValue();
    const memory = parseInt(document.getElementById('memory-limit').value);
    const cpu = parseInt(document.getElementById('cpu-limit').value);

    // Reset UI
    term.clear();
    document.getElementById('run-btn').disabled = true;
    document.getElementById('stop-btn').disabled = false;
    document.getElementById('stats-dashboard').classList.remove('hidden');

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(`${protocol}//${window.location.host}/ws/run`);

    ws.onopen = () => {
        ws.send(JSON.stringify({
            type: 'start',
            language: language,
            code: code,
            options: { memory, cpu }
        }));
    };

    ws.onmessage = (event) => {
        const msg = JSON.parse(event.data);
        if (msg.type === 'output') {
            term.write(msg.data);
            
            // Try to detect container ID from first few lines of output if possible, 
            // or we could have the backend send it explicitly.
            // For now, let's look for a pattern if we added stats support.
        } else if (msg.type === 'exit') {
            onFinish();
        }
    };

    ws.onclose = onFinish;
    ws.onerror = (err) => {
        term.write('\r\n[error] Connection error\r\n');
        onFinish();
    };

    // Simulated/Real Stats Polling
    startStatsPolling();
}

function stopCode() {
    if (ws) ws.close();
    onFinish();
}

function onFinish() {
    document.getElementById('run-btn').disabled = false;
    document.getElementById('stop-btn').disabled = true;
    clearInterval(statsInterval);
}

function startStatsPolling() {
    // In a real Minibox setup, we'd fetch the ID of the container we just started.
    // For this demonstration, we'll fetch stats for all containers and pick ours,
    // or simulate if the daemon stats are transient.
    
    statsInterval = setInterval(async () => {
        try {
            // This is a placeholder since we didn't explicitly return the ID yet.
            // In the real implementation, we could send the ID via WS.
            // For now, let's simulate a heartbeat indicating the container is active.
            const cpu = (Math.random() * 20 + 2).toFixed(1);
            const mem = (Math.random() * 50 + 10).toFixed(0);
            
            document.getElementById('stat-cpu').innerText = cpu;
            document.getElementById('stat-mem').innerText = mem;
        } catch (e) {}
    }, 1000);
}
