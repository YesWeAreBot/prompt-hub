#!/usr/bin/env node

import { loadPack, loadSingleFile, loadArchive, isArchivePath, renderTemplate } from './packhub.mjs';
import { statSync } from 'node:fs';

function usage(code = 2) {
  process.stderr.write(`用法:
  node cli.mjs hash   <pack 路径（目录 / .pack / 单文件）>
  node cli.mjs check  <pack 路径>
  node cli.mjs render <pack 路径> [--turn t] [--ctx k=v]... [--now RFC3339] [--dep id=路径]...
`);
  process.exit(code);
}

function load(path) {
  const st = statSync(path);
  if (st.isDirectory()) return loadPack(path);
  if (isArchivePath(path)) return loadArchive(path);
  return loadSingleFile(path);
}

function main(argv) {
  const [cmd, target, ...rest] = argv;
  if (!cmd || !target) usage();

  if (cmd === 'hash') {
    process.stdout.write(load(target).hash + '\n');
    return;
  }

  if (cmd === 'check') {
    const pack = load(target);
    process.stdout.write(`packId: ${pack.manifest.packId}\n`);
    process.stdout.write(`version: ${pack.manifest.version}\n`);
    process.stdout.write(`profile: ${pack.manifest.profile}\n`);
    process.stdout.write(`kind: ${pack.manifest.kind}\n`);
    process.stdout.write(`entry: ${pack.manifest.entry}\n`);
    process.stdout.write(`runtime: ${pack.manifest.runtime}\n`);
    if (pack.manifest.requires.length) {
      process.stdout.write('requires:\n');
      for (const r of pack.manifest.requires) process.stdout.write(`    - ${r}\n`);
    }
    process.stdout.write(`outputMax: ${pack.manifest.outputMax}\n`);
    process.stdout.write(`---\ntree hash: ${pack.hash}\n文件数:    ${Object.keys(pack.tree).length}\n`);
    return;
  }

  if (cmd === 'render') {
    const ctx = {};
    let turn = '';
    let now;
    const deps = {};
    for (let i = 0; i < rest.length; i++) {
      const a = rest[i];
      if (a === '--turn') turn = rest[++i];
      else if (a === '--now') now = new Date(rest[++i]);
      else if (a === '--dep') {
        
        const spec = rest[++i] ?? '';
        const eq = spec.indexOf('=');
        if (eq <= 0) throw new Error('--dep 需要 id=路径 形式');
        deps[spec.slice(0, eq)] = loadPack(spec.slice(eq + 1)).tree;
      } else if (a === '--ctx') {
        const kv = rest[++i] ?? '';
        const j = kv.indexOf('=');
        if (j < 0) throw new Error('--ctx 需要 k=v 形式');
        ctx[kv.slice(0, j).trim()] = kv.slice(j + 1);
      }
    }
    const pack = load(target);
    const res = renderTemplate(pack, ctx, { turn, now, deps });
    
    process.stdout.write(res.body);
    if (res.warnings.length) {
      process.stderr.write('警告: ' + res.warnings.join(' | ') + '\n');
    }
    return;
  }

  usage();
}

try {
  main(process.argv.slice(2));
} catch (err) {
  process.stderr.write(`错误: ${err.message}\n`);
  process.exit(1);
}
