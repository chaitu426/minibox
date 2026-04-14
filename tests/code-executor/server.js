const express = require('express');
const http = require('http');
const bodyParser = require('body-parser');
const path = require('path');
const { WebSocketServer } = require('ws');
const url = require('url');
const net = require('net');

const app = express();
const port = 3000;
const MINIBOX_API = 'http://127.0.0.1:8080';
const MINIBOX_HOST = '127.0.0.1';
const MINIBOX_PORT = 8080;

app.use(bodyParser.json());
app.use(express.static(path.join(__dirname, 'static')));

// Native Stats Proxy
app.get('/api/stats/:id', (req, res) => {
    const { id } = req.params;
    const statsUrl = `${MINIBOX_API}/containers/stats?id=${id}`;
    
    http.get(statsUrl, (apiRes) => {
        apiRes.pipe(res);
    }).on('error', (err) => {
        res.status(500).json({ error: err.message });
    });
});

const server = http.createServer(app);
const wss = new WebSocketServer({ server });

wss.on('connection', (ws, req) => {
    const location = url.parse(req.url, true);
    
    if (location.pathname === '/ws/run') {
        let miniboxSocket = null;
        let headerFinished = false;
        let inputBuffer = "";

        ws.on('message', (message) => {
            let data;
            try {
                data = JSON.parse(message);
            } catch (e) { return; }

            if (data.type === 'start') {
                const { language, code, options } = data;

                let image = 'python:3.9-alpine';
                let command = ['python3', '-u', '-c', code];

                if (language === 'nodejs') {
                    image = 'node:16-alpine';
                    command = ['node', '-e', code];
                } else if (language === 'shell') {
                    image = 'alpine:latest';
                    command = ['sh', '-c', code];
                }

                const runRequest = {
                    image: image,
                    command: command,
                    interactive: true,
                    detached: false,
                    memory: options?.memory || 0,
                    cpu: options?.cpu || 0
                };

                miniboxSocket = net.connect(MINIBOX_PORT, MINIBOX_HOST, () => {
                    const body = JSON.stringify(runRequest);
                    const head = [
                        'POST /containers/run HTTP/1.1',
                        `Host: ${MINIBOX_HOST}:${MINIBOX_PORT}`,
                        'Content-Type: application/json',
                        `Content-Length: ${Buffer.byteLength(body)}`,
                        '',
                        body
                    ].join('\r\n');

                    miniboxSocket.write(head);
                });

                let httpResponseBuffer = '';

                miniboxSocket.on('data', (chunk) => {
                    if (!headerFinished) {
                        httpResponseBuffer += chunk.toString();
                        if (httpResponseBuffer.includes('\r\n\r\n')) {
                            headerFinished = true;
                            console.log('[debug] Handshake ready. Flushing input buffer.');
                            
                            const parts = httpResponseBuffer.split('\r\n\r\n');
                            if (parts[1]) {
                                ws.send(JSON.stringify({ type: 'output', data: parts[1] }));
                            }
                            
                            // Flush input buffer
                            if (inputBuffer.length > 0) {
                                miniboxSocket.write(inputBuffer);
                                inputBuffer = "";
                            }
                        }
                    } else {
                        ws.send(JSON.stringify({ type: 'output', data: chunk.toString() }));
                    }
                });

                miniboxSocket.on('close', () => {
                    console.log('[debug] Minibox socket closed');
                    ws.send(JSON.stringify({ type: 'exit' }));
                });

                miniboxSocket.on('error', (err) => {
                    console.error('[debug] Minibox error:', err);
                    ws.send(JSON.stringify({ type: 'output', data: `\r\n[error] Connection failed: ${err.message}\r\n` }));
                });

            } else if (data.type === 'input') {
                if (headerFinished && miniboxSocket && miniboxSocket.writable) {
                    miniboxSocket.write(data.data);
                } else {
                    inputBuffer += data.data;
                }
            }
        });

        ws.on('close', () => {
            if (miniboxSocket) miniboxSocket.destroy();
        });
    }
});

server.listen(port, () => {
    console.log(`Minibox Pro Playground listening at http://localhost:${port}`);
});
