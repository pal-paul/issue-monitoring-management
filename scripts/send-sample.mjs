import { readFile } from 'node:fs/promises';
import { createInterface } from 'node:readline/promises';
import { Writable } from 'node:stream';

const endpoint = new URL(process.argv[2] || 'http://127.0.0.1:8080');
if (endpoint.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(endpoint.hostname)) {
  throw new Error('This authenticated sample sender is for loopback development only.');
}
const username = process.argv[3];
if (!username || !process.stdin.isTTY) throw new Error('Run interactively: node scripts/send-sample.mjs http://127.0.0.1:8080 YOUR_USERNAME');
const session = await (await fetch(new URL('/api/session', endpoint))).json();
if (!session.local || session.setupRequired) throw new Error('Complete first-run setup on a local-mode instance first.');
const silent = new Writable({ write(_chunk, _encoding, done) { done(); } });
const prompt = createInterface({ input: process.stdin, output: silent, terminal: true });
process.stdout.write('Password: ');
let password;
try { password = await prompt.question(''); } finally { prompt.close(); process.stdout.write('\n'); }
const login = await fetch(new URL('/api/login', endpoint), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password }) });
password = '';
if (!login.ok) throw new Error('Sign-in failed');
const { csrf } = await login.json();
const cookie = login.headers.get('set-cookie').split(';')[0];
const payload = JSON.parse(await readFile(new URL('../examples/monitoring-alert.json', import.meta.url), 'utf8'));
payload.incident.started_at = Math.floor(Date.now() / 1000) - 300;
payload.incident.incident_id = `local-test-${Date.now()}`;
const response = await fetch(new URL('/api/dev/messages', endpoint), {
  method: 'POST', headers: { 'Content-Type': 'application/json', Cookie: cookie, 'X-CSRF-Token': csrf },
  body: JSON.stringify({
    subscription: 'projects/local-example/subscriptions/monitoring-alerts',
    message: { messageId: `local-${Date.now()}`, data: Buffer.from(JSON.stringify(payload)).toString('base64') }
  })
});
console.log(response.status, await response.text());
await fetch(new URL('/api/logout', endpoint), { method: 'POST', headers: { 'Content-Type': 'application/json', Cookie: cookie, 'X-CSRF-Token': csrf }, body: '{}' });
if (!response.ok) process.exitCode = 1;
