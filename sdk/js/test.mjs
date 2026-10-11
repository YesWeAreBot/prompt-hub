import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync, writeFileSync, mkdtempSync, rmSync, mkdirSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { tmpdir } from 'node:os';

import {
  loadPack, loadSingleFile, loadArchive, isArchivePath, readTar,
  treeHash, renderTemplate,
  parseManifestYaml, truncateUtf8, formatNow,
  parseAssetArg, assetRefs, checkAssets, assetClosure, undeclaredAssetVars,
} from './packhub.mjs';
import { gzipSync } from 'node:zlib';

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, '..', '..');
const nekochan = join(repo, 'examples', 'nekochan');

test('manifest 解析：pack.yaml 的 YAML 子集', () => {
  const m = parseManifestYaml(readFileSync(join(nekochan, 'pack.yaml'), 'utf8'));
  assert.equal(m.packId, 'nekochan');
  assert.equal(m.version, '1.0.0');
  assert.equal(m.runtime, 'template');
  assert.equal(m.outputMax, '4000');
  assert.deepEqual(m.requires, ['chat.user.nickname', 'chat.channel.name']);
});

test('tree hash：稳定且与插入顺序无关', () => {
  const a = treeHash({ 'b.txt': Buffer.from('2'), 'a.txt': Buffer.from('1') });
  const b = treeHash({ 'a.txt': Buffer.from('1'), 'b.txt': Buffer.from('2') });
  assert.equal(a, b);
  assert.match(a, /^sha256-[0-9a-f]{64}$/);
  
  assert.notEqual(a, treeHash({ 'a.txt': Buffer.from('1'), 'b.txt': Buffer.from('3') }));
});

test('tree hash：必须按 UTF-8 字节序，而不是 JS 默认排序', () => {
  const supplementary = '\u{20000}'; 
  const bmp = '\uE000';              

  
  const defaultOrder = [supplementary, bmp].sort()[0];
  assert.equal(defaultOrder, supplementary, 'JS 默认排序会把补充平面字符排前面');
  const byteOrder = [supplementary, bmp].sort((a, b) =>
    Buffer.compare(Buffer.from(a, 'utf8'), Buffer.from(b, 'utf8')))[0];
  assert.equal(byteOrder, bmp, 'UTF-8 字节序相反');

  
  const h1 = treeHash({ [supplementary]: Buffer.from('x'), [bmp]: Buffer.from('y') });
  const h2 = treeHash({ [bmp]: Buffer.from('y'), [supplementary]: Buffer.from('x') });
  assert.equal(h1, h2);
});

test('fixtures：与 pack 自带的期望输出一致', () => {
  const pack = loadPack(nekochan);
  const dir = join(nekochan, 'fixtures');
  const files = readdirSync(dir).filter((f) => f.endsWith('.json'));
  assert.ok(files.length > 0, '应当有 fixture');
  for (const f of files) {
    const fx = JSON.parse(readFileSync(join(dir, f), 'utf8'));
    const res = renderTemplate(pack, fx.context, { turn: fx.turn, now: new Date(fx.now) });
    assert.equal(res.body.replace(/\n+$/, ''), fx.expect.replace(/\n+$/, ''), `fixture ${f}`);
  }
});

test('必需字段缺失是契约违例，必须显式抛错', () => {
  const pack = loadPack(nekochan);
  assert.throws(() => renderTemplate(pack, {}, {}), (err) => {
    assert.equal(err.code, 'MISSING_CONTEXT');
    assert.deepEqual(err.missing, ['chat.channel.name', 'chat.user.nickname']);
    return true;
  });
});

test('模板引用未提供的变量必须抛错，不能静默留空', () => {
  const pack = {
    manifest: { ...loadPack(nekochan).manifest, requires: [], entry: 'i.prompt' },
    tree: { 'i.prompt': Buffer.from('你好 {{nope}}') },
  };
  assert.throws(() => renderTemplate(pack, {}, {}), /未提供的变量: nope/);
});

test('截断：按 UTF-8 边界，绝不劈开多字节字符', () => {
  
  const { text, truncated } = truncateUtf8('猫娘AI', 5);
  assert.equal(truncated, true);
  assert.equal(text, '猫'); 
  assert.ok(Buffer.byteLength(text) <= 5);
  
  assert.equal(Buffer.from(text, 'utf8').toString('utf8'), text);
  assert.ok(!text.includes('\uFFFD'));
});

test('内置变量：now 用 UTC，格式与 Go 对齐', () => {
  
  
  assert.equal(formatNow(new Date(Date.UTC(2026, 9, 10, 21, 30, 5))), '2026-10-10 21:30:05');
  
  assert.equal(formatNow(new Date('2026-10-10T21:30:05Z')), '2026-10-10 21:30:05');
  assert.equal(formatNow(new Date('2026-10-11T05:30:05+08:00')), '2026-10-10 21:30:05');
});

const libDeps = () => ({ 'cats-role': loadPack(join(repo, 'examples', 'cats-role')).tree });

test('资产指令：写法判定', () => {
  assert.equal(parseAssetArg('chat.user.nickname').isAsset, false);
  assert.equal(parseAssetArg('assetx').isAsset, false);
  
  for (const bad of ['asset', 'asset a/b.md', "asset 'a/b.md"]) {
    const r = parseAssetArg(bad);
    assert.equal(r.isAsset, true, bad);
    assert.ok(r.error, `${bad} 应当有错误说明`);
  }
  assert.equal(parseAssetArg("asset 'a/b.md'").ref, 'a/b.md');
  assert.equal(parseAssetArg('asset "a/b.md"').ref, 'a/b.md');
});

test('资产递归：资产里的变量会展开，资产可以再引用资产', () => {
  const dir = mkdtempSync(join(tmpdir(), 'ph-asset-'));
  try {
    writeFileSync(join(dir, 'pack.yaml'),
      'packId: demo\nversion: 1.0.0\nentry: index.prompt\nruntime: template\nrequires:\n  - chat.user.nickname\n');
    writeFileSync(join(dir, 'index.prompt'), "{{asset 'lib/p.md'}}你好\n");
    const pack = loadPack(dir);
    const deps = {
      lib: {
        'p.md': Buffer.from("一层：{{chat.user.nickname}}\n{{asset 'lib/q.md'}}"),
        'q.md': Buffer.from('二层\n'),
      },
    };
    const res = renderTemplate(pack, { 'chat.user.nickname': '小兔' }, { deps });
    assert.equal(res.body, '一层：小兔\n二层\n你好\n');
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('资产递归：环与指数膨胀都必须被拦住', () => {
  const mk = (files) => {
    const dir = mkdtempSync(join(tmpdir(), 'ph-recur-'));
    writeFileSync(join(dir, 'pack.yaml'),
      'packId: demo\nversion: 1.0.0\nentry: index.prompt\nruntime: template\nincludes:\n  - lib@1.0.0\n');
    writeFileSync(join(dir, 'index.prompt'), "{{asset 'lib/a.md'}}");
    return { pack: loadPack(dir), deps: { lib: files } };
  };

  
  let t = mk({ 'a.md': Buffer.from("自己 {{asset 'lib/a.md'}}") });
  assert.throws(() => renderTemplate(t.pack, {}, { deps: t.deps }), /循环引用/);

  
  t = mk({ 'a.md': Buffer.from("A {{asset 'lib/b.md'}}"), 'b.md': Buffer.from("B {{asset 'lib/a.md'}}") });
  assert.throws(() => renderTemplate(t.pack, {}, { deps: t.deps }), (err) => {
    assert.match(err.message, /循环引用/);
    assert.match(err.message, /→/);
    return true;
  });

  
  
  const big = 'x'.repeat(100 << 10);
  t = mk({
    'a.md': Buffer.from("{{asset 'lib/b.md'}}{{asset 'lib/b.md'}}"),
    'b.md': Buffer.from("{{asset 'lib/c.md'}}{{asset 'lib/c.md'}}"),
    'c.md': Buffer.from("{{asset 'lib/d.md'}}{{asset 'lib/d.md'}}"),
    'd.md': Buffer.from("{{asset 'lib/e.md'}}{{asset 'lib/e.md'}}"),
    'e.md': Buffer.from(big),
  });
  assert.throws(() => renderTemplate(t.pack, {}, { deps: t.deps }), /总量上限/);
});

test('资产闭包：递归进去的字段也算，且要能查出「没声明」', () => {
  const deps = {
    lib: {
      'p.md': Buffer.from("{{asset 'lib/q.md'}}"),
      'q.md': Buffer.from('用了 {{chat.user.nickname}}'),
    },
  };
  const { assets, vars } = assetClosure("{{asset 'lib/p.md'}}", null, deps);
  assert.deepEqual(assets, ['lib/p.md', 'lib/q.md']);
  assert.deepEqual(vars, ['chat.user.nickname']);
  
  assert.deepEqual(assetClosure("{{turn}} {{asset 'lib/q.md'}}", null, deps).vars, ['chat.user.nickname']);

  assert.deepEqual(undeclaredAssetVars("{{asset 'lib/p.md'}}", null, ['chat.user.nickname'], deps), []);
  assert.deepEqual(undeclaredAssetVars("{{asset 'lib/p.md'}}", null, [], deps), ['chat.user.nickname']);
});

const ownTree = () => ({
  'index.prompt': Buffer.from("{{asset './partials/head.md'}}正文"),
  'partials/head.md': Buffer.from('【头】{{chat.user.nickname}}\n'),
  'partials/body.md': Buffer.from("【体】{{asset './partials/head.md'}}"),
  'selfloop.md': Buffer.from("{{asset './selfloop.md'}}"),
});

function packWithOwnTree(dir, prompt) {
  writeFileSync(join(dir, 'pack.yaml'),
    'packId: own\nversion: 1.0.0\nentry: index.prompt\nruntime: template\nrequires:\n  - chat.user.nickname\n');
  writeFileSync(join(dir, 'index.prompt'), prompt);
  for (const [rel, content] of Object.entries(ownTree())) {
    if (rel === 'index.prompt') continue;
    const abs = join(dir, rel);
    mkdirSync(dirname(abs), { recursive: true });
    writeFileSync(abs, content);
  }
  return loadPack(dir);
}

test('自己的文件：`./` 既不需要 includes 也不需要依赖', () => {
  const dir = mkdtempSync(join(tmpdir(), 'ph-own-'));
  try {
    const pack = packWithOwnTree(dir, "{{asset './partials/head.md'}}");
    const res = renderTemplate(pack, { 'chat.user.nickname': '小兔' }, {});
    assert.equal(res.body, '【头】小兔\n');
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('自己的文件之间也能递归', () => {
  const dir = mkdtempSync(join(tmpdir(), 'ph-own2-'));
  try {
    const pack = packWithOwnTree(dir, "{{asset './partials/body.md'}}");
    const res = renderTemplate(pack, { 'chat.user.nickname': '小兔' }, {});
    assert.equal(res.body, '【体】【头】小兔\n');
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('自己的文件：错误信息与上界', () => {
  const dir = mkdtempSync(join(tmpdir(), 'ph-own3-'));
  
  const ctx = { 'chat.user.nickname': '小兔' };
  try {
    
    let pack = packWithOwnTree(dir, "{{asset './nope.md'}}");
    assert.throws(() => renderTemplate(pack, ctx, {}), (err) => {
      assert.match(err.message, /本 pack/);
      assert.match(err.message, /partials\/head\.md/);
      return true;
    });
    
    pack = packWithOwnTree(dir, "{{asset './selfloop.md'}}");
    assert.throws(() => renderTemplate(pack, ctx, {}), /循环引用/);
    
    for (const bad of ['../secret', '/etc/passwd', './../x', './']) {
      const p = packWithOwnTree(dir, `{{asset '${bad}'}}`);
      assert.throws(() => renderTemplate(p, ctx, {}), undefined, `${bad} 应被拒`);
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('checkAssets：自己的文件即使没有依赖也能完整校验', () => {
  const own = ownTree();
  assert.doesNotThrow(() => checkAssets("{{asset './partials/head.md'}}", own, null, null));
  assert.throws(() => checkAssets("{{asset './nope.md'}}", own, null, null), /本 pack/);
  assert.doesNotThrow(() => checkAssets("{{asset './partials/body.md'}}", own, null, null));
});

test('资产：未提供的变量要说清来自哪一层', () => {
  const dir = mkdtempSync(join(tmpdir(), 'ph-src-'));
  try {
    writeFileSync(join(dir, 'pack.yaml'),
      'packId: demo\nversion: 1.0.0\nentry: index.prompt\nruntime: template\nincludes:\n  - lib@1.0.0\n');
    writeFileSync(join(dir, 'index.prompt'), "{{asset 'lib/p.md'}}");
    const pack = loadPack(dir);
    const deps = {
      lib: { 'p.md': Buffer.from("{{asset 'lib/q.md'}}"), 'q.md': Buffer.from('{{chat.nope}}') },
    };
    assert.throws(() => renderTemplate(pack, {}, { deps }), (err) => {
      assert.match(err.message, /chat\.nope/);
      assert.match(err.message, /lib\/q\.md/, '应说明来自哪个资产');
      return true;
    });
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('资产引用：错误分支要说清怎么修', () => {
  const dir = mkdtempSync(join(tmpdir(), 'ph-asset2-'));
  try {
    const write = (body) => {
      writeFileSync(join(dir, 'pack.yaml'),
        'packId: demo\nversion: 1.0.0\nentry: index.prompt\nruntime: template\nincludes:\n  - lib@1.0.0\n');
      writeFileSync(join(dir, 'index.prompt'), body);
      return loadPack(dir);
    };
    const deps = { lib: { 'p.md': Buffer.from('x'), 'bin.dat': Buffer.from([0, 1]) } };

    assert.throws(() => renderTemplate(write("{{asset 'other/p.md'}}"), {}, { deps }), /includes/);
    assert.throws(() => renderTemplate(write("{{asset 'lib/nope.md'}}"), {}, { deps }), /p\.md/);
    assert.throws(() => renderTemplate(write("{{asset 'lib/bin.dat'}}"), {}, { deps }), /二进制/);
    assert.throws(() => renderTemplate(write("{{asset 'lib/../secret'}}"), {}, { deps }), /不合法/);
    assert.throws(() => renderTemplate(write('{{asset}}'), {}, { deps }), /路径/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('checkAssets：与 Go 的 CheckAssets 同样严', () => {
  const body = "{{asset 'lib/p.md'}}";
  const deps = { lib: { 'p.md': Buffer.from('x') } };
  assert.doesNotThrow(() => checkAssets(body, null, ['lib@1.0.0'], deps));
  assert.throws(() => checkAssets(body, null, [], deps), /includes/);
  assert.throws(() => checkAssets(body, null, ['lib@1.0.0'], { lib: {} }), /没有 p\.md/);
  
  assert.doesNotThrow(() => checkAssets(body, null, ['lib@1.0.0'], null));
  assert.throws(() => checkAssets(body, null, [], null), /includes/);
  assert.throws(() => checkAssets('{{asset lib/p.md}}', null, ['lib@1.0.0'], deps), /引号/);
  assert.deepEqual(
    assetRefs("{{asset 'lib/p.md'}} {{chat.x}} {{asset 'lib/q.md'}}"),
    ['lib/p.md', 'lib/q.md'],
  );
});

test('fixtures：examples/nekochan-asset 内联 cats-role 的人设文案', () => {
  const pack = loadPack(join(repo, 'examples', 'nekochan-asset'));
  const fx = JSON.parse(
    readFileSync(join(repo, 'examples', 'nekochan-asset', 'fixtures', 'basic.json'), 'utf8'),
  );
  const res = renderTemplate(pack, fx.context, {
    turn: fx.turn, now: new Date(fx.now), deps: libDeps(),
  });
  assert.equal(res.body, fx.expect);
});

function tarEntry(name, content, type = '0') {
  const body = Buffer.isBuffer(content) ? content : Buffer.from(content);
  const h = Buffer.alloc(512);
  h.write(name, 0, 100, 'utf8');
  h.write('0000644\0', 100, 8, 'binary');
  h.write('0000000\0', 108, 8, 'binary');
  h.write('0000000\0', 116, 8, 'binary');
  h.write(`${body.length.toString(8).padStart(11, '0')}\0`, 124, 12, 'binary');
  h.write('00000000000\0', 136, 12, 'binary');
  h.write('        ', 148, 8, 'binary'); 
  h.write(type, 156, 1, 'binary');
  h.write('ustar\0', 257, 6, 'binary');
  h.write('00', 263, 2, 'binary');
  let sum = 0;
  for (const b of h) sum += b;
  h.write(`${sum.toString(8).padStart(6, '0')}\0 `, 148, 8, 'binary');
  const pad = Buffer.alloc((512 - (body.length % 512)) % 512);
  return Buffer.concat([h, body, pad]);
}

const tarBytes = (...entries) =>
  Buffer.concat([...entries, Buffer.alloc(1024)]); 

test('归档：从 .pack 读出的身份与从目录读完全一致', () => {
  const dir = mkdtempSync(join(tmpdir(), 'ph-arc-'));
  try {
    writeFileSync(join(dir, 'pack.yaml'),
      'packId: arc\nversion: 1.0.0\nentry: index.prompt\nruntime: template\nrequires:\n  - chat.user.nickname\n');
    writeFileSync(join(dir, 'index.prompt'), '你好 {{chat.user.nickname}}\n');

    const fromDir = loadPack(dir);
    
    const parts = Object.keys(fromDir.tree).sort().map((n) => tarEntry(n, fromDir.tree[n]));
    const file = join(dir, 'demo.pack');
    writeFileSync(file, gzipSync(tarBytes(...parts)));

    const fromArc = loadArchive(file);
    assert.equal(fromArc.form, 'archive');
    assert.equal(fromArc.hash, fromDir.hash, '归档与目录的身份必须一致');
    assert.equal(fromArc.manifest.packId, 'arc');
    assert.deepEqual(Object.keys(fromArc.tree).sort(), Object.keys(fromDir.tree).sort());

    
    const ctx = { 'chat.user.nickname': '小兔' };
    assert.equal(
      renderTemplate(fromArc, ctx, {}).body,
      renderTemplate(fromDir, ctx, {}).body,
    );
    
    assert.equal(isArchivePath('a/b/demo.pack'), true);
    assert.equal(isArchivePath('a/b'), false);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('归档：病毒式路径、重复条目、损坏内容都要拒', () => {
  
  const raw = (...e) => tarBytes(...e);
  const gz = (...e) => gzipSync(tarBytes(...e));

  
  assert.throws(() => readTar(raw(tarEntry('../evil', 'x'))), /越界/);
  assert.throws(() => readTar(raw(tarEntry('/etc/passwd', 'x'))), /绝对路径/);
  assert.throws(() => readTar(raw(tarEntry('./../x', 'x'))), /越界/);
  
  assert.throws(() => readTar(raw(tarEntry('a', '1'), tarEntry('a', '2'))), /重复/);
  
  assert.throws(() => readTar(Buffer.alloc(1024)), /没有任何文件/);
  
  const tampered = tarEntry('a', 'x');
  tampered[0] = 0x41;
  assert.throws(() => readTar(raw(tampered)), /checksum/);
  
  assert.throws(() => readTar(raw(tarEntry('a', 'x', '2'))), /不支持的类型/);

  const dir = mkdtempSync(join(tmpdir(), 'ph-arc2-'));
  try {
    
    const bad = join(dir, 'bad.pack');
    writeFileSync(bad, Buffer.from('这不是 gzip'));
    assert.throws(() => loadArchive(bad), /gzip/);
    
    const noManifest = join(dir, 'nom.pack');
    writeFileSync(noManifest, gz(tarEntry('index.prompt', 'x')));
    assert.throws(() => loadArchive(noManifest), /pack\.yaml/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('单文件 pack：头部注释即 manifest', () => {  const dir = mkdtempSync(join(tmpdir(), 'ph-single-'));
  try {
    const file = join(dir, 'neko.prompt');
    writeFileSync(file, `/* Pack-Id: single-demo
 * Version: 2.0.0
 * Runtime: template
 * Requires: chat.user.nickname
 */
你好 {{chat.user.nickname}}
`);
    const pack = loadSingleFile(file);
    assert.equal(pack.manifest.packId, 'single-demo');
    assert.equal(pack.manifest.version, '2.0.0');
    assert.equal(pack.manifest.entry, 'neko.prompt');
    assert.equal(pack.manifest.runtime, 'template');
    assert.equal(pack.form, 'single-file');

    const res = renderTemplate(pack, { 'chat.user.nickname': '小兔' }, {});
    
    assert.ok(!res.body.includes('Pack-Id'), `头部注释漏进了正文：${JSON.stringify(res.body)}`);
    assert.equal(res.body.trim(), '你好 小兔');
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
