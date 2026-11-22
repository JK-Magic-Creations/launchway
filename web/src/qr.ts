/**
 * A small QR Code encoder: byte mode, error correction level M, versions 1 to 10 (up to 213 bytes of UTF-8), which is
 * room for any address a release page or a download link has. No network and no dependency: a phone camera reads the
 * picture drawn from `qrMatrix`. The algorithm is the one the standard (ISO/IEC 18004) describes; qr.test.ts checks the
 * pieces against published values and reads the code back.
 */

/** Error correction codewords per block, and the number of blocks, for level M, versions 1 to 10 (index 0 unused). */
const EC_PER_BLOCK = [0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26];
const BLOCKS = [0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5];
export const MAX_VERSION = 10;

/** The most bytes a code can hold. */
export const QR_MAX_BYTES = 213;

const mul = (x: number, y: number): number => {
  let z = 0;
  for (let i = 7; i >= 0; i--) {
    z = (z << 1) ^ ((z >>> 7) * 0x11d);
    z ^= ((y >>> i) & 1) * x;
  }
  return z;
};

/** The Reed-Solomon remainder of data for `degree` check codewords. */
export function reedSolomon(data: number[], degree: number): number[] {
  const gen: number[] = new Array(degree).fill(0);
  gen[degree - 1] = 1;
  let root = 1;
  for (let i = 0; i < degree; i++) {
    for (let j = 0; j < degree; j++) {
      gen[j] = mul(gen[j], root);
      if (j + 1 < degree) gen[j] ^= gen[j + 1];
    }
    root = mul(root, 2);
  }
  const rem: number[] = new Array(degree).fill(0);
  for (const b of data) {
    const factor = b ^ (rem.shift() as number);
    rem.push(0);
    for (let i = 0; i < degree; i++) rem[i] ^= mul(gen[i], factor);
  }
  return rem;
}

const rawModules = (ver: number): number => {
  let r = (16 * ver + 128) * ver + 64;
  if (ver >= 2) {
    const n = Math.floor(ver / 7) + 2;
    r -= (25 * n - 10) * n - 55;
    if (ver >= 7) r -= 36;
  }
  return r;
};
const dataCodewords = (ver: number): number => Math.floor(rawModules(ver) / 8) - EC_PER_BLOCK[ver] * BLOCKS[ver];

/** The smallest version that holds `bytes` bytes, or 0 if none does. */
export function versionFor(bytes: number): number {
  for (let v = 1; v <= MAX_VERSION; v++) {
    const countBits = v < 10 ? 8 : 16;
    if (4 + countBits + bytes * 8 <= dataCodewords(v) * 8) return v;
  }
  return 0;
}

const alignPositions = (ver: number): number[] => {
  if (ver === 1) return [];
  const n = Math.floor(ver / 7) + 2;
  const step = Math.ceil((ver * 4 + 4) / (n * 2 - 2)) * 2;
  const out = [6];
  for (let pos = ver * 4 + 10; out.length < n; pos -= step) out.splice(1, 0, pos);
  return out;
};

/** The 15 format bits (level M and a mask), with the standard's BCH code and mask pattern applied. */
export function formatBits(mask: number): number {
  const data = (0 << 3) | mask; // level M is 00
  let rem = data;
  for (let i = 0; i < 10; i++) rem = (rem << 1) ^ ((rem >>> 9) * 0x537);
  return ((data << 10) | rem) ^ 0x5412;
}

/** The 18 version bits of a version of 7 or more. */
export function versionBits(ver: number): number {
  let rem = ver;
  for (let i = 0; i < 12; i++) rem = (rem << 1) ^ ((rem >>> 11) * 0x1f25);
  return (ver << 12) | rem;
}

const MASKS: Array<(x: number, y: number) => boolean> = [
  (x, y) => (x + y) % 2 === 0,
  (_x, y) => y % 2 === 0,
  (x) => x % 3 === 0,
  (x, y) => (x + y) % 3 === 0,
  (x, y) => (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0,
  (x, y) => ((x * y) % 2) + ((x * y) % 3) === 0,
  (x, y) => (((x * y) % 2) + ((x * y) % 3)) % 2 === 0,
  (x, y) => (((x + y) % 2) + ((x * y) % 3)) % 2 === 0,
];

/** The codewords of a code: the data with its length and padding, split into blocks, each with its check codewords, interleaved. */
export function codewords(bytes: Uint8Array, ver: number): number[] {
  const bits: number[] = [];
  const put = (v: number, n: number) => { for (let i = n - 1; i >= 0; i--) bits.push((v >>> i) & 1); };
  put(0b0100, 4);
  put(bytes.length, ver < 10 ? 8 : 16);
  for (const b of bytes) put(b, 8);
  const cap = dataCodewords(ver) * 8;
  put(0, Math.min(4, cap - bits.length));
  put(0, (8 - (bits.length % 8)) % 8);
  for (let pad = 0xec; bits.length < cap; pad ^= 0xec ^ 0x11) put(pad, 8);
  const data: number[] = [];
  for (let i = 0; i < bits.length; i += 8) data.push(parseInt(bits.slice(i, i + 8).join(""), 2));

  const nb = BLOCKS[ver], eccLen = EC_PER_BLOCK[ver];
  const raw = Math.floor(rawModules(ver) / 8);
  const nShort = nb - (raw % nb), shortLen = Math.floor(raw / nb);
  const blocks: number[][] = [];
  for (let i = 0, k = 0; i < nb; i++) {
    const dat = data.slice(k, k + shortLen - eccLen + (i < nShort ? 0 : 1));
    k += dat.length;
    const ecc = reedSolomon(dat, eccLen);
    if (i < nShort) dat.push(0);
    blocks.push(dat.concat(ecc));
  }
  const out: number[] = [];
  for (let i = 0; i < blocks[0].length; i++) {
    blocks.forEach((b, j) => { if (i !== shortLen - eccLen || j >= nShort) out.push(b[i]); });
  }
  return out;
}

export interface QrMatrix {
  version: number;
  size: number;
  mask: number;
  /** modules[y][x] is true for a dark module. */
  modules: boolean[][];
}

/** The picture of a code for the text, or null when it is longer than 213 bytes. */
export function qrMatrix(text: string): QrMatrix | null {
  const bytes = new TextEncoder().encode(text);
  const ver = versionFor(bytes.length);
  if (bytes.length === 0 || ver === 0) return null;
  const size = ver * 4 + 17;
  const mod: boolean[][] = Array.from({ length: size }, () => new Array(size).fill(false));
  const fn: boolean[][] = Array.from({ length: size }, () => new Array(size).fill(false));
  const set = (x: number, y: number, dark: boolean) => { mod[y][x] = dark; fn[y][x] = true; };

  for (let i = 0; i < size; i++) { set(6, i, i % 2 === 0); set(i, 6, i % 2 === 0); }
  const finder = (cx: number, cy: number) => {
    for (let dy = -4; dy <= 4; dy++) for (let dx = -4; dx <= 4; dx++) {
      const d = Math.max(Math.abs(dx), Math.abs(dy)), x = cx + dx, y = cy + dy;
      if (x >= 0 && x < size && y >= 0 && y < size) set(x, y, d !== 2 && d !== 4);
    }
  };
  finder(3, 3); finder(size - 4, 3); finder(3, size - 4);
  const ap = alignPositions(ver);
  ap.forEach((cy, i) => ap.forEach((cx, j) => {
    if ((i === 0 && j === 0) || (i === 0 && j === ap.length - 1) || (i === ap.length - 1 && j === 0)) return;
    for (let dy = -2; dy <= 2; dy++) for (let dx = -2; dx <= 2; dx++) set(cx + dx, cy + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
  }));
  const drawFormat = (mask: number) => {
    const bits = formatBits(mask), b = (i: number) => ((bits >>> i) & 1) !== 0;
    for (let i = 0; i <= 5; i++) set(8, i, b(i));
    set(8, 7, b(6)); set(8, 8, b(7)); set(7, 8, b(8));
    for (let i = 9; i < 15; i++) set(14 - i, 8, b(i));
    for (let i = 0; i < 8; i++) set(size - 1 - i, 8, b(i));
    for (let i = 8; i < 15; i++) set(8, size - 15 + i, b(i));
    set(8, size - 8, true);
  };
  drawFormat(0);
  if (ver >= 7) {
    const bits = versionBits(ver);
    for (let i = 0; i < 18; i++) {
      const dark = ((bits >>> i) & 1) !== 0, a = size - 11 + (i % 3), b = Math.floor(i / 3);
      set(a, b, dark); set(b, a, dark);
    }
  }

  const cw = codewords(bytes, ver);
  let k = 0;
  for (let right = size - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5;
    for (let vert = 0; vert < size; vert++) for (let j = 0; j < 2; j++) {
      const x = right - j, up = ((right + 1) & 2) === 0, y = up ? size - 1 - vert : vert;
      if (!fn[y][x] && k < cw.length * 8) { mod[y][x] = ((cw[k >>> 3] >>> (7 - (k & 7))) & 1) !== 0; k++; }
    }
  }

  let best = 0, bestPenalty = Infinity, bestMod: boolean[][] = mod;
  for (let m = 0; m < 8; m++) {
    const t = mod.map((row) => row.slice());
    for (let y = 0; y < size; y++) for (let x = 0; x < size; x++) if (!fn[y][x] && MASKS[m](x, y)) t[y][x] = !t[y][x];
    const saved = t.map((row) => row.slice());
    // The format bits depend on the mask: draw them into the trial.
    const bits = formatBits(m), b = (i: number) => ((bits >>> i) & 1) !== 0;
    for (let i = 0; i <= 5; i++) t[i][8] = b(i);
    t[7][8] = b(6); t[8][8] = b(7); t[8][7] = b(8);
    for (let i = 9; i < 15; i++) t[8][14 - i] = b(i);
    for (let i = 0; i < 8; i++) t[8][size - 1 - i] = b(i);
    for (let i = 8; i < 15; i++) t[size - 15 + i][8] = b(i);
    t[size - 8][8] = true;
    const p = penalty(t);
    if (p < bestPenalty) { best = m; bestPenalty = p; bestMod = t; }
    void saved;
  }
  return { version: ver, size, mask: best, modules: bestMod };
}

/** The standard's four penalty rules; the mask with the lowest total is used. */
function penalty(m: boolean[][]): number {
  const n = m.length;
  let p = 0;
  const line = (get: (i: number) => boolean) => {
    let run = 1;
    for (let i = 1; i < n; i++) {
      if (get(i) === get(i - 1)) { run++; if (run === 5) p += 3; else if (run > 5) p += 1; } else run = 1;
    }
    // finder-like patterns 1011101 with four light modules on either side
    for (let i = 0; i + 7 <= n; i++) {
      if (get(i) && !get(i + 1) && get(i + 2) && get(i + 3) && get(i + 4) && !get(i + 5) && get(i + 6)) {
        const before = i >= 4 && !get(i - 1) && !get(i - 2) && !get(i - 3) && !get(i - 4);
        const after = i + 11 <= n && !get(i + 7) && !get(i + 8) && !get(i + 9) && !get(i + 10);
        if (before || after) p += 40;
      }
    }
  };
  for (let y = 0; y < n; y++) line((i) => m[y][i]);
  for (let x = 0; x < n; x++) line((i) => m[i][x]);
  for (let y = 0; y < n - 1; y++) for (let x = 0; x < n - 1; x++) {
    if (m[y][x] === m[y][x + 1] && m[y][x] === m[y + 1][x] && m[y][x] === m[y + 1][x + 1]) p += 3;
  }
  let dark = 0;
  for (const row of m) for (const v of row) if (v) dark++;
  p += Math.floor(Math.abs((dark * 20) / (n * n) - 10)) * 10;
  return p;
}

/** The picture as one SVG path (a one-module quiet zone of four modules around it), for inline use. */
export function qrPath(text: string): { size: number; d: string } | null {
  const q = qrMatrix(text);
  if (!q) return null;
  const quiet = 4;
  let d = "";
  q.modules.forEach((row, y) => {
    let x = 0;
    while (x < q.size) {
      if (!row[x]) { x++; continue; }
      let e = x;
      while (e < q.size && row[e]) e++;
      d += `M${x + quiet} ${y + quiet}h${e - x}v1h${-(e - x)}z`;
      x = e;
    }
  });
  return { size: q.size + quiet * 2, d };
}
