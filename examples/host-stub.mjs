#!/usr/bin/env node

import { createServer } from 'node:http';
import { parseArgs } from 'node:util';
import { readFileSync } from 'node:fs';

const { values: a } = parseArgs({
  options: {
    daemon: { type: 'string', default: 'http://127.0.0.1:18400' },
    token: { type: 'string' },
    'token-file': { type: 'string' },
    pack: { type: 'string', default: 'nekochan@1.0.0' },
    app: { type: 'string', default: 'host-stub' },
    turn: { type: 'string', default: 'ev_stub' },
  },
});

const token = a.token ?? (a['token-file'] ? readFileSync(a['token-file'], 'utf8').trim() : '');
if (!token) {
  console.error('需要 --token 或 --token-file');
  process.exit(2);
}

const sessionData = {
  'chat.channel.name': '辩论队吹水群',
  'chat.user.nickname': '小兔',
  'chat.user.affinity': '86',
};

const host = createServer((req, res) => {
  let body = '';
  req.on('data', (c) => (body += c));
  req.on('end', () => {
    let fields = [];
    try {
      fields = JSON.parse(body || '{}').fields ?? [];
    } catch {}
    const values = {};
    for (const f of fields) {
      if (f in sessionData) values[f] = sessionData[f];
    }
    
    const missing = fields.filter((f) => !(f in values));
    console.error(
      `  [宿主] daemon 来要: ${fields.join(', ') || '（无）'}` +
        (missing.length ? `  没有: ${missing.join(', ')}` : ''),
    );
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ values }));
  });
});
await new Promise((r) => host.listen(0, '127.0.0.1', r));
const endpoint = `http://127.0.0.1:${host.address().port}/ph/context`;
console.error(`  应用 context 端点 ${endpoint}`);

const auth = { 'X-Ph-Token': token };

const initResp = await fetch(`${a.daemon}/v1/initialize`, {
  method: 'POST',
  headers: { ...auth, 'content-type': 'application/json' },
  body: JSON.stringify({
    protocolVersion: '1',
    app: a.app,
    contextEndpoint: endpoint,
    contextToken: 'stub-secret',
    capabilities: ['pull-context', 'validate'],
  }),
});
const init = await initResp.json();
if (!initResp.ok) {
  console.error(`握手失败 ${initResp.status}:`, init);
  process.exit(1);
}
console.error(`  握手成功：${init.daemon}，协商到 ${(init.agreed ?? []).join(', ') || '（无）'}`);
console.error(
  `  档位：${init.tiers.map((t) => t.name + (t.available ? '' : '❌')).join(', ')}` +
    (init.tiers.some((t) => !t.available)
      ? `\n  提示：${init.tiers.filter((t) => !t.available).map((t) => t.hint).join('；')}`
      : ''),
);

const vResp = await fetch(`${a.daemon}/v1/validate?pack=${encodeURIComponent(a.pack)}`, {
  headers: { ...auth, 'X-Ph-App': a.app },
});
const vd = await vResp.json();
console.error(
  `  validate：需要 ${(vd.requires ?? []).join(', ') || '（无）'}` +
    `；依赖 ${(vd.deps ?? []).join(', ') || '（无）'}` +
    `；档位可用 ${vd.runtimeAvailable ? '是' : `否 —— ${vd.runtimeHint}`}`,
);

const q = new URLSearchParams({ pack: a.pack, turn: a.turn });
q.set('ctx.chat.user.nickname', '小兔');
const resp = await fetch(`${a.daemon}/v1/render?${q}`, {
  headers: { ...auth, 'X-Ph-App': a.app },
});
const text = await resp.text();
console.error(
  `  渲染 ${resp.status}  来源=${resp.headers.get('x-ph-source') ?? '-'}` +
    `  tree=${(resp.headers.get('x-tree-hash') ?? '-').slice(0, 22)}…`,
);
if (!resp.ok) {
  console.error('渲染失败:', text);
  host.close();
  process.exit(1);
}
console.log('---- 渲染结果 ----');
console.log(text);
host.close();
