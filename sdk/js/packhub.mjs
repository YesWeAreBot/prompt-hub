import { createHash } from 'node:crypto';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { gunzipSync } from 'node:zlib';

const MANIFEST_NAME = 'pack.yaml';
const CANONICAL_MANIFEST_NAME = 'pack.json';

const IGNORE_SEGMENTS = new Set(['.git', 'node_modules', '.installed']);

function ignored(rel) {
  if (rel.endsWith('.tmp')) return true;
  return rel.split('/').some((seg) => IGNORE_SEGMENTS.has(seg));
}

function sortByBytes(paths) {
  return paths.sort((a, b) => Buffer.compare(Buffer.from(a, 'utf8'), Buffer.from(b, 'utf8')));
}

export function treeHash(tree) {
  const h = createHash('sha256');
  for (const p of sortByBytes(Object.keys(tree))) {
    const sum = createHash('sha256').update(tree[p]).digest('hex');
    h.update(Buffer.from(p, 'utf8'));
    h.update(Buffer.from('\x000644\x00', 'utf8'));
    h.update(Buffer.from(sum + '\n', 'utf8'));
  }
  return 'sha256-' + h.digest('hex');
}

function unquote(s) {
  if (s.length >= 2 && ((s[0] === '"' && s.at(-1) === '"') || (s[0] === "'" && s.at(-1) === "'"))) {
    return s.slice(1, -1);
  }
  return s;
}

export function parseManifestYaml(text) {
  const out = {};
  let currentList = null;
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.replace(/\s+$/, '');
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#')) continue;

    const listItem = line.match(/^\s+-\s*(.*)$/);
    if (listItem && currentList) {
      const v = unquote(listItem[1].trim());
      if (v) currentList.push(v);
      continue;
    }
    const kv = line.match(/^([A-Za-z][A-Za-z0-9_-]*):\s*(.*)$/);
    if (!kv) continue;
    const [, key, rest] = kv;
    const value = rest.trim();
    if (value === '') {
      currentList = [];
      out[key] = currentList;
    } else if (value.startsWith('[')) {
      currentList = null;
      out[key] = value
        .replace(/^\[|\]$/g, '')
        .split(',')
        .map((s) => unquote(s.trim()))
        .filter(Boolean);
    } else {
      currentList = null;
      out[key] = unquote(value);
    }
  }
  return out;
}

function normalizeHeaderKey(k) {
  const key = k.toLowerCase().replace(/[-_\s]/g, '');
  switch (key) {
    case 'packid': case 'id': case 'name': return 'packId';
    case 'version': case 'ver': return 'version';
    case 'profile': return 'profile';
    case 'kind': return 'kind';
    case 'entry': case 'main': return 'entry';
    case 'runtime': return 'runtime';
    case 'exports': return 'exports';
    case 'includes': case 'dependencies': return 'includes';
    case 'requires': case 'context': return 'requires';
    case 'egress': case 'network': return 'egress';
    case 'outputmax': case 'maxchars': return 'outputMax';
    case 'targets': return 'targets';
    case 'license': return 'license';
    default: return '';
  }
}

export function parseHeaderComment(text) {
  if (!/^\s*(?:<\?php\s*)?\/\*/.test(text)) return null;
  const start = text.indexOf('/*');
  const end = text.indexOf('*/');
  if (start < 0 || end < start) throw new Error('头部注释未闭合');
  const fields = {};
  for (const line of text.slice(start + 2, end).split(/\r?\n/)) {
    const t = line.replace(/^\s*\*?\s*/, '').trim();
    const i = t.indexOf(':');
    if (i < 0) continue;
    const key = normalizeHeaderKey(t.slice(0, i).trim());
    if (key) fields[key] = t.slice(i + 1).trim();
  }
  if (!fields.packId && !fields.version) return null;
  return fields;
}

function asList(v) {
  if (Array.isArray(v)) return v.map((s) => String(s).trim()).filter(Boolean);
  if (typeof v === 'string' && v.trim()) return v.split(',').map((s) => s.trim()).filter(Boolean);
  return [];
}

export function normalizeManifest(raw) {
  return {
    packId: raw.packId ?? '',
    version: raw.version ?? '',
    profile: raw.profile || 'chat/1',
    kind: raw.kind || 'content',
    entry: raw.entry || '',
    runtime: raw.runtime || 'template',
    exports: asList(raw.exports),
    includes: asList(raw.includes),
    requires: asList(raw.requires),
    egress: asList(raw.egress),
    outputMax: Number.parseInt(raw.outputMax ?? '', 10) || 8000,
    targets: asList(raw.targets),
    license: raw.license || '',
  };
}

function readTree(dir) {
  const tree = {};
  const walk = (abs, rel) => {
    for (const entry of readdirSync(abs, { withFileTypes: true })) {
      const childRel = rel ? `${rel}/${entry.name}` : entry.name;
      if (ignored(childRel)) continue;
      const childAbs = join(abs, entry.name);
      if (entry.isDirectory()) walk(childAbs, childRel);
      else if (entry.isFile()) tree[childRel] = readFileSync(childAbs);
    }
  };
  walk(dir, '');
  return tree;
}

export function parseManifestText(text) {
  const t = text.trimStart();
  if (t.startsWith('{')) return JSON.parse(t);
  return parseManifestYaml(text);
}

export function canonicalManifestJson(m) {
  const order = ['packId', 'version', 'profile', 'kind', 'entry', 'runtime',
    'exports', 'includes', 'requires', 'egress', 'outputMax', 'targets', 'license'];
  const always = new Set(['packId', 'version', 'profile']);
  const out = {};
  for (const k of order) {
    const v = m[k];
    if (always.has(k)) { out[k] = v ?? ''; continue; }
    if (Array.isArray(v)) { if (v.length) out[k] = v; continue; }
    if (typeof v === 'number') { if (v !== 0) out[k] = v; continue; }
    if (v === undefined || v === null || v === '') continue;
    out[k] = v;
  }
  return JSON.stringify(out);
}

export function loadPack(dir) {
  let text;
  try {
    text = readFileSync(join(dir, MANIFEST_NAME), 'utf8');
  } catch {
    text = readFileSync(join(dir, CANONICAL_MANIFEST_NAME), 'utf8');
  }
  const manifest = normalizeManifest(parseManifestText(text));
  const tree = readTree(dir);
  return { manifest, tree, hash: treeHash(tree), form: 'dir' };
}

export function loadSingleFile(path) {
  const buf = readFileSync(path);
  const text = buf.toString('utf8');
  const fields = parseHeaderComment(text);
  if (!fields) {
    throw new Error('单文件 pack 必须在头部注释中声明 manifest；或改用 pack.yaml 目录形态');
  }
  const base = path.split(/[\\/]/).pop();
  const raw = { ...fields, entry: !fields.entry || fields.entry === 'self' ? base : fields.entry };
  if (raw.entry !== base) {
    throw new Error(`单文件 pack 的 entry 只能是自身（${base}），收到 ${raw.entry}`);
  }
  const manifest = normalizeManifest(raw);
  
  
  const tree = {
    [CANONICAL_MANIFEST_NAME]: Buffer.from(canonicalManifestJson(manifest), 'utf8'),
    [base]: buf,
  };
  return {
    manifest, tree, hash: treeHash(tree), form: 'single-file',
    hashPortable: true,
  };
}

const ARCHIVE_EXT = '.pack';
const MAX_ARCHIVE_ENTRY = 64 << 20;

function validArchivePath(p) {
  if (p === '' || p === '.') throw new Error('归档含空路径');
  if (p.startsWith('/')) throw new Error(`归档含绝对路径: ${p}`);
  if (p === '..' || p.startsWith('../') || p.includes('/../')) {
    throw new Error(`归档含越界路径: ${p}`);
  }
  if (p.includes('\0')) throw new Error('归档路径含 NUL');
  return p;
}

function readCString(buf, start, len) {
  const end = buf.indexOf(0, start);
  const stop = end >= 0 && end < start + len ? end : start + len;
  return buf.toString('utf8', start, stop);
}

function readOctal(buf, start, len) {
  if (buf[start] & 0x80) {
    throw new Error('归档用了 base-256 数值编码（超大条目），本实现不支持');
  }
  const s = readCString(buf, start, len).trim();
  if (s === '') return 0;
  if (!/^[0-7]+$/.test(s)) throw new Error(`归档字段不是合法八进制: ${JSON.stringify(s)}`);
  return parseInt(s, 8);
}

function tarChecksumOK(hdr) {
  let sum = 0;
  for (let i = 0; i < 512; i++) {
    sum += i >= 148 && i < 156 ? 0x20 : hdr[i];
  }
  return sum === readOctal(hdr, 148, 8);
}

function parsePaxRecords(body) {
  const out = {};
  let off = 0;
  while (off < body.length) {
    const sp = body.indexOf(0x20, off);
    if (sp < 0) break;
    const len = parseInt(body.toString('ascii', off, sp), 10);
    if (!Number.isFinite(len) || len <= 0 || off + len > body.length) break;
    const rec = body.toString('utf8', sp + 1, off + len - 1); 
    const eq = rec.indexOf('=');
    if (eq > 0) out[rec.slice(0, eq)] = rec.slice(eq + 1);
    off += len;
  }
  return out;
}

export function readTar(buf) {
  const tree = {};
  let off = 0;
  let pax = null;
  let sawEnd = false;
  while (off + 512 <= buf.length) {
    const hdr = buf.subarray(off, off + 512);
    off += 512;
    
    if (hdr.every((b) => b === 0)) {
      sawEnd = true;
      break;
    }
    if (!tarChecksumOK(hdr)) throw new Error('归档的 tar 头 checksum 不符（文件损坏或被改过）');
    const size = readOctal(hdr, 124, 12);
    if (size > MAX_ARCHIVE_ENTRY) {
      throw new Error(`归档条目声明 ${size} 字节，超过上限 ${MAX_ARCHIVE_ENTRY}`);
    }
    const body = buf.subarray(off, off + size);
    off += Math.ceil(size / 512) * 512;
    const type = String.fromCharCode(hdr[156] || 0x30);

    if (type === 'x' || type === 'g') {
      pax = parsePaxRecords(body);
      continue;
    }
    if (type === '5') continue; 
    if (type !== '0' && type !== '\0') {
      throw new Error(`归档含不支持的类型 ${JSON.stringify(type)}: ${readCString(hdr, 0, 100)}`);
    }

    let name = readCString(hdr, 0, 100);
    const prefix = readCString(hdr, 345, 155);
    if (prefix) name = `${prefix}/${name}`;
    if (pax?.path) name = pax.path;
    pax = null;

    const clean = validArchivePath(name.replace(/^\.\//, ''));
    if (tree[clean] !== undefined) throw new Error(`归档含重复路径: ${clean}`);
    if (body.length !== size) {
      throw new Error(`条目 ${clean} 长度不符（声明 ${size}，实际 ${body.length}）`);
    }
    tree[clean] = Buffer.from(body);
  }
  if (!sawEnd && off < buf.length) {
    
    throw new Error('归档没有正常结束（缺少结束标记）');
  }
  if (Object.keys(tree).length === 0) throw new Error('归档里没有任何文件');
  return tree;
}

export function loadArchive(path) {
  const raw = readFileSync(path);
  let tar;
  try {
    tar = gunzipSync(raw);
  } catch (err) {
    throw new Error(`不是合法的 gzip 归档: ${err.message}`);
  }
  const tree = readTar(tar);
  const mraw = tree[MANIFEST_NAME] ?? tree[CANONICAL_MANIFEST_NAME];
  if (!mraw) {
    throw new Error(`归档里既没有 ${MANIFEST_NAME} 也没有 ${CANONICAL_MANIFEST_NAME}`);
  }
  const manifest = normalizeManifest(parseManifestText(mraw.toString('utf8')));
  return { manifest, tree, hash: treeHash(tree), form: 'archive' };
}

export function isArchivePath(p) {
  return String(p).toLowerCase().endsWith(ARCHIVE_EXT);
}

const VAR_RE = /\{\{\s*([^{}]+?)\s*\}\}/g;

export const ASSET_MAX_BYTES = 256 << 10;
export const ASSET_MAX_DEPTH = 8;
export const ASSET_MAX_EXPANDED_BYTES = 1 << 20;

const ASSET_ARG_RE = /^asset(?:\s+(.*))?$/;

const BUILTIN_VARS = new Set(['packId', 'version', 'turn', 'now', 'nowUnix']);

export function isBuiltinVar(name) {
  return BUILTIN_VARS.has(name);
}

export function parseAssetArg(inner) {
  const m = ASSET_ARG_RE.exec(inner);
  if (!m) return { isAsset: false };
  const arg = (m[1] ?? '').trim();
  if (arg === '') {
    return { isAsset: true, error: "asset 需要一个路径，例如 {{asset 'cats-role/persona.md'}}" };
  }
  if (arg.length < 2 || (arg[0] !== "'" && arg[0] !== '"') || arg.at(-1) !== arg[0]) {
    return {
      isAsset: true,
      error: `asset 的路径要加引号，例如 {{asset 'cats-role/persona.md'}}，收到 "${inner}"`,
    };
  }
  return { isAsset: true, ref: arg.slice(1, -1) };
}

function parseAssetTarget(ref) {
  if (ref.startsWith('./')) {
    const path = ref.slice(2);
    if (path === '') throw new Error(`\`./\` 后面要跟路径，收到 "${ref}"`);
    checkAssetPath(path);
    return { own: true, id: '', path, key: `./${path}` };
  }
  if (ref.startsWith('/') || ref.startsWith('../')) {
    throw new Error(`资产引用 "${ref}" 不能用绝对路径或 \`../\`；引用自己的文件请写成 ./<路径>`);
  }
  const i = ref.indexOf('/');
  if (i <= 0 || i === ref.length - 1) {
    throw new Error(`资产引用要写成 <packId>/<路径>（依赖）或 ./<路径>（自己），收到 "${ref}"`);
  }
  const id = ref.slice(0, i);
  const path = ref.slice(i + 1);
  checkAssetPath(path);
  return { own: false, id, path, key: `${id}/${path}` };
}

function checkAssetPath(path) {
  for (const seg of path.split('/')) {
    if (seg === '..' || seg === '.' || seg === '') {
      throw new Error(`资产路径 "${path}" 不合法`);
    }
  }
}

function newExpander(lookup, own, deps, unresolvedDeps = false) {
  return {
    lookup,
    own: own ?? {},
    deps: deps ?? {},
    unresolvedDeps,
    stack: [],
    assets: [],
    parts: [],
    bytes: 0,
    unknown: [],
  };
}

function chainWith(stack, next) {
  return [...stack, next].join(' → ');
}

function appendText(e, s) {
  const n = Buffer.byteLength(s, 'utf8');
  if (e.bytes + n > ASSET_MAX_EXPANDED_BYTES) {
    throw new Error(
      `展开后超过总量上限 ${ASSET_MAX_EXPANDED_BYTES} 字节` +
        `（递归展开必须有总量上界，否则一个很小的 pack 就能指数级膨胀）`,
    );
  }
  e.parts.push(s);
  e.bytes += n;
}

function expandInto(e, text) {
  const re = new RegExp(VAR_RE.source, 'g');
  let last = 0;
  let m;
  while ((m = re.exec(text)) !== null) {
    appendText(e, text.slice(last, m.index));
    expandNode(e, m[0], m[1].trim());
    last = m.index + m[0].length;
  }
  appendText(e, text.slice(last));
}

function expandNode(e, whole, inner) {
  const a = parseAssetArg(inner);
  if (!a.isAsset) {
    const v = e.lookup(inner);
    if (v === undefined) {
      e.unknown.push({ name: inner, where: e.stack.length ? e.stack.at(-1) : '' });
      
      appendText(e, whole);
      return;
    }
    appendText(e, String(v));
    return;
  }
  if (a.error) throw new Error(a.error);
  expandAsset(e, a.ref);
}

function expandAsset(e, ref) {
  const t = parseAssetTarget(ref);
  const key = t.key;
  if (e.stack.length >= ASSET_MAX_DEPTH) {
    throw new Error(`资产嵌套超过 ${ASSET_MAX_DEPTH} 层: ${chainWith(e.stack, key)}`);
  }
  if (e.stack.includes(key)) {
    throw new Error(`资产循环引用: ${chainWith(e.stack, key)}`);
  }

  let tree;
  let where;
  if (t.own) {
    tree = e.own;
    where = '本 pack';
  } else {
    tree = e.deps[t.id];
    if (!tree) {
      if (e.unresolvedDeps) {
        e.assets.push(key);
        return;
      }
      throw new Error(
        `资产 ${key} 来自 pack ${t.id}，但本 pack 没有 include 它` +
          `（在 pack.yaml 的 includes 里加上 ${t.id}@<精确版本>；引用自己的文件请用 ./）`,
      );
    }
    where = `pack ${t.id}`;
  }
  const content = tree[t.path];
  if (content === undefined) {
    throw new Error(`${where} 里没有 ${t.path}；它有：${Object.keys(tree).sort().join(', ')}`);
  }
  const buf = Buffer.isBuffer(content) ? content : Buffer.from(String(content), 'utf8');
  if (buf.length > ASSET_MAX_BYTES) {
    throw new Error(`资产 ${key} 有 ${buf.length} 字节，超过单文件上限 ${ASSET_MAX_BYTES}`);
  }
  if (buf.includes(0)) {
    throw new Error(`资产 ${key} 含 NUL 字节，看起来是二进制文件；只能内联文本`);
  }
  e.assets.push(key);
  e.stack.push(key);
  try {
    expandInto(e, buf.toString('utf8'));
  } finally {
    e.stack.pop();
  }
}

export function formatUnknown(unknown) {
  const seen = new Set();
  const parts = [];
  for (const u of unknown) {
    const k = `${u.name}|${u.where}`;
    if (seen.has(k)) continue;
    seen.add(k);
    parts.push(u.where ? `${u.name}（来自 ${u.where}）` : u.name);
  }
  parts.sort();
  return `模板引用了未提供的变量: ${parts.join(', ')}`;
}

export function assetClosure(text, own, deps) {
  const seen = new Set();
  const e = newExpander((name) => {
    seen.add(name);
    return '';
  }, own, deps);
  expandInto(e, text);
  return {
    assets: e.assets,
    vars: [...seen].filter((n) => !BUILTIN_VARS.has(n)).sort(),
  };
}

export function undeclaredAssetVars(text, own, requires, deps) {
  if (!deps) return [];
  const { vars } = assetClosure(text, own, deps);
  const declared = new Set(requires ?? []);
  return vars.filter((v) => !declared.has(v));
}

export function assetRefs(body) {
  const out = [];
  for (const m of body.matchAll(VAR_RE)) {
    const a = parseAssetArg(m[1].trim());
    if (a.isAsset && !a.error) out.push(a.ref);
  }
  return out;
}

export function checkAssets(body, own, includes, deps) {
  const declared = new Set();
  for (const inc of includes ?? []) {
    const at = String(inc).lastIndexOf('@');
    if (at > 0) declared.add(String(inc).slice(0, at));
  }
  const seen = new Set();
  for (const m of body.matchAll(VAR_RE)) {
    const a = parseAssetArg(m[1].trim());
    if (!a.isAsset) continue;
    if (a.error) throw new Error(a.error);
    if (seen.has(a.ref)) continue;
    seen.add(a.ref);
    const t = parseAssetTarget(a.ref);
    if (t.own) continue; 
    if (!declared.has(t.id)) {
      throw new Error(`资产 "${a.ref}" 来自 pack ${t.id}，但它不在 includes 里 —— 加上 ${t.id}@<精确版本>`);
    }
    if (!deps) continue;
    const tree = deps[t.id];
    if (!tree) throw new Error(`依赖 ${t.id} 没有解析出来`);
    if (tree[t.path] === undefined) {
      throw new Error(`pack ${t.id} 里没有 ${t.path}；它有：${Object.keys(tree).sort().join(', ')}`);
    }
  }

  
  
  const seen2 = new Set();
  const e = newExpander((name) => {
    seen2.add(name);
    return '';
  }, own, deps, !deps);
  expandInto(e, body);
  for (const key of e.assets) {
    const t = parseAssetTarget(key);
    if (t.own) continue;
    if (!declared.has(t.id)) {
      throw new Error(`资产闭包里用到了 pack ${t.id} 的 ${key}，但它不在 includes 里 —— 加上 ${t.id}@<精确版本>`);
    }
  }
  return true;
}

export function stripHeaderComment(content) {
  const text = content.toString('utf8');
  if (!/^\s*(?:<\?php\s*)?\/\*/.test(text)) return { text, stripped: false };
  const start = text.indexOf('/*');
  const end = text.indexOf('*/');
  if (start < 0 || end < start) return { text, stripped: false };

  const known = new Set(['packId', 'version', 'runtime', 'entry', 'requires', 'profile']);
  let looksLikeManifest = false;
  for (const line of text.slice(start + 2, end).split(/\r?\n/)) {
    const t = line.replace(/^\s*\*?\s*/, '').trim();
    const i = t.indexOf(':');
    if (i < 0) continue;
    if (known.has(normalizeHeaderKey(t.slice(0, i).trim()))) looksLikeManifest = true;
  }
  if (!looksLikeManifest) return { text, stripped: false };
  return { text: text.slice(end + 2).replace(/^[\r\n]+/, ''), stripped: true };
}

function two(n) { return String(n).padStart(2, '0'); }

export function formatNow(d) {
  return `${d.getUTCFullYear()}-${two(d.getUTCMonth() + 1)}-${two(d.getUTCDate())} ` +
    `${two(d.getUTCHours())}:${two(d.getUTCMinutes())}:${two(d.getUTCSeconds())}`;
}

export function truncateUtf8(text, max) {
  const buf = Buffer.from(text, 'utf8');
  if (buf.length <= max) return { text, truncated: false };
  let end = max;
  
  while (end > 0 && (buf[end] & 0xc0) === 0x80) end--;
  return { text: buf.subarray(0, end).toString('utf8'), truncated: true };
}

export function renderTemplate(pack, context = {}, opts = {}) {
  const { manifest, tree } = pack;
  if (manifest.runtime !== 'template') {
    throw new Error(`这个 SDK 只渲染 template 档，收到 ${manifest.runtime}`);
  }
  
  const missing = manifest.requires.filter((k) => !(k in context));
  if (missing.length) {
    const err = new Error(`缺少 pack 声明的必需 context 字段: ${missing.slice().sort().join(', ')}`);
    err.code = 'MISSING_CONTEXT';
    err.missing = missing.slice().sort();
    throw err;
  }
  const entryBuf = tree[manifest.entry];
  if (entryBuf === undefined) throw new Error(`entry ${manifest.entry} 不在 pack 里`);

  const now = opts.now ?? new Date();
  const vars = {
    packId: manifest.packId,
    version: manifest.version,
    turn: opts.turn ?? '',
    now: formatNow(now),
    nowUnix: String(Math.floor(now.getTime() / 1000)),
    ...context,
  };

  const { text: entryText } = stripHeaderComment(entryBuf);
  const deps = opts.deps ?? {};
  
  
  const e = newExpander((k) => (k in vars ? String(vars[k]) : undefined), tree, deps);
  expandInto(e, entryText);
  if (e.unknown.length) {
    throw new Error(formatUnknown(e.unknown));
  }
  const body = e.parts.join('');

  const warnings = [];
  const { text, truncated } = truncateUtf8(body, manifest.outputMax);
  if (truncated) warnings.push(`输出超过 outputMax=${manifest.outputMax}，已截断`);
  return { body: text, warnings };
}
