// deploy/integrado/alert-sink.mjs
// Canal de alertas SIMULADO: recebe o POST do alertService do CRM
// (ALERT_WEBHOOK_URL) e grava uma linha JSON por alerta. Loopback apenas.
//   node alert-sink.mjs <porta> <arquivo.jsonl>
import http from 'node:http';
import fs from 'node:fs';
const [port, file] = process.argv.slice(2);
const out = fs.createWriteStream(file, { flags: 'a' });
http.createServer((req, res) => {
  let body = '';
  req.on('data', (c) => { body += c; });
  req.on('end', () => {
    let j = {};
    try { j = JSON.parse(body); } catch { j = { raw: body.slice(0, 200) }; }
    out.write(JSON.stringify({ at: new Date().toISOString(), type: j.type, level: j.level, message: j.message }) + '\n');
    res.writeHead(200).end('ok');
  });
}).listen(Number(port), '127.0.0.1', () => console.log(`alert-sink em 127.0.0.1:${port}`));
