// Cross-check the phone client's incremental SHA-256 (server/frontend/upload.js)
// against Node's crypto implementation. It loads the actual upload.js file,
// extracts the sha256/hex functions it ships, and hashes the same inputs in
// both, including chunked update() calls like the real client does.
//
// Run: node tests/sha256.test.mjs

import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import assert from 'node:assert/strict';

const src = readFileSync(new URL('../server/frontend/upload.js', import.meta.url), 'utf8');

// Extract a top-level function from the client source, skipping commented-out
// or string-literal braces.
function extractFunc(name) {
  const head = `function ${name}(`;
  const start = src.indexOf(head);
  assert.notEqual(start, -1, `function ${name} not found in upload.js`);
  let i = src.indexOf('{', start);
  assert.notEqual(i, -1, `no body for ${name}`);
  let depth = 0;
  while (i < src.length) {
    const ch = src[i];
    if (ch === '/') {
      if (src[i + 1] === '/') {
        const nl = src.indexOf('\n', i);
        assert.notEqual(nl, -1, `unterminated // comment in ${name}`);
        i = nl;
        continue;
      }
      if (src[i + 1] === '*') {
        const end = src.indexOf('*/', i);
        assert.notEqual(end, -1, `unterminated block comment in ${name}`);
        i = end + 2;
        continue;
      }
    }
    if (ch === '"' || ch === "'") {
      const end = src.indexOf(ch, i + 1);
      assert.notEqual(end, -1, `unterminated string in ${name}`);
      i = end + 1; // past the closing quote; end is not scanned again
      continue;
    }
    if (ch === '{') depth++;
    if (ch === '}') {
      depth--;
      if (depth === 0) return src.slice(start, i + 1);
    }
    i++;
  }
  throw new Error(`unterminated ${name}`);
}

const fn = new Function(
  extractFunc('rotr') +
  extractFunc('sha256') +
  extractFunc('hex') +
  '\nreturn { sha256: sha256, hex: hex };'
)();

function qdHex(input) {
  const h = fn.sha256();
  // Feed fragments of varying length, like chunk loads crossing 64-byte blocks.
  for (let off = 0; off < input.length; ) {
    const take = Math.min((off % 7) + 1, input.length - off);
    h.update(input.subarray(off, off + take));
    off += take;
  }
  return fn.hex(h.digest());
}

function oracleHex(input) {
  const h = createHash('sha256');
  h.update(input);
  return h.digest('hex');
}

function check(label, input) {
  const actual = qdHex(input);
  const expected = oracleHex(input);
  assert.equal(actual, expected, `hash mismatch for ${label}`);
}

// Deterministic pseudo-random data (xorshift32), so runs are reproducible.
function randomBytesGen(seed) {
  let s = seed >>> 0;
  return (n) => {
    const out = new Uint8Array(n);
    for (let i = 0; i < n; i++) {
      s ^= s << 13; s >>>= 0;
      s ^= s >> 17;
      s ^= s << 5;  s >>>= 0;
      out[i] = s & 0xff;
    }
    return out;
  };
}
const rnd = randomBytesGen(0x9e3779b9);

check('empty', new Uint8Array(0));
check('abc', new TextEncoder().encode('abc'));
check('three blocks', new TextEncoder().encode('abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq'));
check('53 bytes', rnd(53));
check('55 bytes', rnd(55));
check('56 bytes', rnd(56));
check('63 bytes', rnd(63));
check('64 bytes', rnd(64));
check('65 bytes', rnd(65));
check('112 bytes', rnd(112));
check('111 KiB', rnd(111 * 1024));
check('2 MiB + 1', rnd(2 * 1024 * 1024 + 1));
check('4 MiB exactly', rnd(4 * 1024 * 1024));
check('13 MiB', rnd(13 * 1024 * 1024));
check('33 MiB + 17', rnd(33 * 1024 * 1024 + 17));

console.log('sha256.test.mjs: all downloads-side hash checks passed');