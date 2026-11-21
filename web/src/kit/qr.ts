// A small QR code encoder: byte mode, error correction level M, versions 1 to 10 (up to 213 bytes), which holds
// an authenticator setup address with room to spare. Written for Launchway (nothing is fetched and nothing
// leaves the page: the secret is drawn here, as inline SVG). Checked against a QR decoder in qr.test.ts.

const EC_M: Record<number, { ec: number; blocks: [number, number][] }> = {
  // per version: error correction codewords per block, and [number of blocks, data codewords per block] groups
  1: { ec: 10, blocks: [[1, 16]] }, 2: { ec: 16, blocks: [[1, 28]] }, 3: { ec: 26, blocks: [[1, 44]] },
  4: { ec: 18, blocks: [[2, 32]] }, 5: { ec: 24, blocks: [[2, 43]] }, 6: { ec: 16, blocks: [[4, 27]] },
  7: { ec: 18, blocks: [[4, 31]] }, 8: { ec: 22, blocks: [[2, 38], [2, 39]] }, 9: { ec: 22, blocks: [[3, 36], [2, 37]] },
  10: { ec: 26, blocks: [[4, 43], [1, 44]] },
};
const ALIGN: Record<number, number[]> = { 1: [], 2: [6, 18], 3: [6, 22], 4: [6, 26], 5: [6, 30], 6: [6, 34], 7: [6, 22, 38], 8: [6, 24, 42], 9: [6, 26, 46], 10: [6, 28, 50] };

const EXP = new Uint8Array(512);
const LOG = new Uint8Array(256);
(() => {
  let x = 1;
  for (let i = 0; i < 255; i++) { EXP[i] = x; LOG[x] = i; x <<= 1; if (x & 0x100) x ^= 0x11d; }
  for (let i = 255; i < 512; i++) EXP[i] = EXP[i - 255];
})();
const mul = (a: number, b: number) => (a === 0 || b === 0 ? 0 : EXP[LOG[a] + LOG[b]]);

function rsGenerator(degree: number): number[] {
  let g = [1];
  for (let i = 0; i < degree; i++) {
    const next = new Array(g.length + 1).fill(0);
    for (let j = 0; j < g.length; j++) { next[j] ^= g[j]; next[j + 1] ^= mul(g[j], EXP[i]); }
    g = next;
  }
  return g;
}

function rsRemainder(data: number[], ec: number): number[] {
  const g = rsGenerator(ec);
  const r = new Array(ec).fill(0);
  for (const b of data) {
    const f = b ^ r[0];
    r.shift(); r.push(0);
    if (f !== 0) for (let i = 0; i < ec; i++) r[i] ^= mul(g[i + 1], f);
  }
  return r;
}

function bchFormat(data: number): number {
  let d = data << 10;
  for (let i = 14; i >= 10; i--) if (d & (1 << i)) d ^= 0x537 << (i - 10);
  return ((data << 10) | d) ^ 0x5412;
}
function bchVersion(v: number): number {
  let d = v << 12;
  for (let i = 17; i >= 12; i--) if (d & (1 << i)) d ^= 0x1f25 << (i - 12);
  return (v << 12) | d;
}

/** encode returns the QR code's modules (true is dark) as rows, or null when the text is too long. */
export function encodeQR(text: string): boolean[][] | null {
  const bytes = Array.from(new TextEncoder().encode(text));
  let version = 0;
  for (let v = 1; v <= 10; v++) {
    const total = EC_M[v].blocks.reduce((s, [n, d]) => s + n * d, 0);
    const bits = 4 + (v < 10 ? 8 : 16) + bytes.length * 8;
    if (bits <= total * 8) { version = v; break; }
  }
  if (!version) return null;
  const { ec, blocks } = EC_M[version];
  const total = blocks.reduce((s, [n, d]) => s + n * d, 0);

  // the bit stream: mode 0100, the length, the bytes, a terminator, padding
  const bits: number[] = [];
  const put = (value: number, n: number) => { for (let i = n - 1; i >= 0; i--) bits.push((value >> i) & 1); };
  put(4, 4); put(bytes.length, version < 10 ? 8 : 16);
  for (const b of bytes) put(b, 8);
  put(0, Math.min(4, total * 8 - bits.length));
  while (bits.length % 8) bits.push(0);
  const data: number[] = [];
  for (let i = 0; i < bits.length; i += 8) data.push(parseInt(bits.slice(i, i + 8).join(""), 2));
  for (let pad = 0xec; data.length < total; pad ^= 0xec ^ 0x11) data.push(pad);

  // blocks, their error correction, interleaved
  const dataBlocks: number[][] = [];
  let at = 0;
  for (const [n, d] of blocks) for (let i = 0; i < n; i++) { dataBlocks.push(data.slice(at, at + d)); at += d; }
  const ecBlocks = dataBlocks.map((b) => rsRemainder(b, ec));
  const out: number[] = [];
  const maxData = Math.max(...dataBlocks.map((b) => b.length));
  for (let i = 0; i < maxData; i++) for (const b of dataBlocks) if (i < b.length) out.push(b[i]);
  for (let i = 0; i < ec; i++) for (const b of ecBlocks) out.push(b[i]);

  const size = 17 + 4 * version;
  const mod: (boolean | null)[][] = Array.from({ length: size }, () => new Array(size).fill(null));
  const fixed: boolean[][] = Array.from({ length: size }, () => new Array(size).fill(false));
  const set = (r: number, c: number, dark: boolean) => { if (r >= 0 && r < size && c >= 0 && c < size) { mod[r][c] = dark; fixed[r][c] = true; } };

  const finder = (r0: number, c0: number) => {
    for (let r = -1; r <= 7; r++) for (let c = -1; c <= 7; c++) {
      const inside = r >= 0 && r <= 6 && c >= 0 && c <= 6;
      const dark = inside && (r === 0 || r === 6 || c === 0 || c === 6 || (r >= 2 && r <= 4 && c >= 2 && c <= 4));
      set(r0 + r, c0 + c, dark);
    }
  };
  finder(0, 0); finder(0, size - 7); finder(size - 7, 0);
  const pos = ALIGN[version];
  for (const r of pos) for (const c of pos) {
    if ((r === 6 && c === 6) || (r === 6 && c === pos[pos.length - 1]) || (r === pos[pos.length - 1] && c === 6)) continue;
    for (let dr = -2; dr <= 2; dr++) for (let dc = -2; dc <= 2; dc++) set(r + dr, c + dc, Math.max(Math.abs(dr), Math.abs(dc)) !== 1);
  }
  for (let i = 8; i < size - 8; i++) { set(6, i, i % 2 === 0); set(i, 6, i % 2 === 0); }
  set(size - 8, 8, true); // the always-dark module
  const reserve = (r: number, c: number) => { if (!fixed[r][c]) { fixed[r][c] = true; mod[r][c] = false; } };
  for (let i = 0; i < 9; i++) { reserve(8, i); reserve(i, 8); }
  for (let i = 0; i < 8; i++) { reserve(8, size - 1 - i); reserve(size - 1 - i, 8); }
  if (version >= 7) {
    const info = bchVersion(version);
    for (let i = 0; i < 18; i++) {
      const dark = ((info >> i) & 1) === 1;
      const a = Math.floor(i / 3), b = (i % 3) + size - 11;
      set(a, b, dark); set(b, a, dark);
    }
  }

  // the data, in the zigzag
  const stream: boolean[] = [];
  for (const byte of out) for (let i = 7; i >= 0; i--) stream.push(((byte >> i) & 1) === 1);
  const place = (mask: number): boolean[][] => {
    const m = mod.map((row) => row.slice());
    let k = 0, up = true;
    for (let col = size - 1; col > 0; col -= 2) {
      if (col === 6) col--;
      for (let i = 0; i < size; i++) {
        const r = up ? size - 1 - i : i;
        for (const c of [col, col - 1]) {
          if (fixed[r][c]) continue;
          let dark = k < stream.length ? stream[k] : false;
          k++;
          const flip = [(r + c) % 2 === 0, r % 2 === 0, c % 3 === 0, (r + c) % 3 === 0, (Math.floor(r / 2) + Math.floor(c / 3)) % 2 === 0,
            ((r * c) % 2) + ((r * c) % 3) === 0, (((r * c) % 2) + ((r * c) % 3)) % 2 === 0, (((r + c) % 2) + ((r * c) % 3)) % 2 === 0][mask];
          if (flip) dark = !dark;
          m[r][c] = dark;
        }
      }
      up = !up;
    }
    const f = bchFormat((0 << 3) | mask); // error correction level M is 00
    // the format bits are placed with explicit coordinates, below
    placeFormat(m, f, size);
    return m as boolean[][];
  };

  let best: boolean[][] | null = null, bestScore = Infinity;
  for (let mask = 0; mask < 8; mask++) {
    const m = place(mask);
    const s = penalty(m);
    if (s < bestScore) { best = m; bestScore = s; }
  }
  return best;
}

function placeFormat(m: (boolean | null)[][], f: number, size: number) {
  const bit = (i: number) => ((f >> i) & 1) === 1;
  // first copy: around the top left finder
  const a: [number, number][] = [[0, 8], [1, 8], [2, 8], [3, 8], [4, 8], [5, 8], [7, 8], [8, 8], [8, 7], [8, 5], [8, 4], [8, 3], [8, 2], [8, 1], [8, 0]];
  a.forEach(([r, c], i) => { m[r][c] = bit(i); });
  // second copy: beside the other two finders
  for (let i = 0; i < 8; i++) m[8][size - 1 - i] = bit(i);
  for (let i = 8; i < 15; i++) m[size - 15 + i][8] = bit(i);
  m[size - 8][8] = true;
}

function penalty(m: boolean[][]): number {
  const n = m.length;
  let score = 0;
  const runs = (get: (i: number, j: number) => boolean) => {
    for (let i = 0; i < n; i++) {
      let run = 1;
      for (let j = 1; j < n; j++) {
        if (get(i, j) === get(i, j - 1)) run++; else { if (run >= 5) score += run - 2; run = 1; }
      }
      if (run >= 5) score += run - 2;
    }
  };
  runs((i, j) => m[i][j]); runs((i, j) => m[j][i]);
  for (let i = 0; i < n - 1; i++) for (let j = 0; j < n - 1; j++) {
    const v = m[i][j];
    if (v === m[i][j + 1] && v === m[i + 1][j] && v === m[i + 1][j + 1]) score += 3;
  }
  const pat = [true, false, true, true, true, false, true, false, false, false, false];
  const rev = [...pat].reverse();
  const finders = (get: (i: number, j: number) => boolean) => {
    for (let i = 0; i < n; i++) for (let j = 0; j + 11 <= n; j++) {
      let a = true, b = true;
      for (let k = 0; k < 11; k++) { if (get(i, j + k) !== pat[k]) a = false; if (get(i, j + k) !== rev[k]) b = false; }
      if (a) score += 40;
      if (b) score += 40;
    }
  };
  finders((i, j) => m[i][j]); finders((i, j) => m[j][i]);
  let dark = 0;
  for (const row of m) for (const v of row) if (v) dark++;
  score += Math.floor(Math.abs((dark * 100) / (n * n) - 50) / 5) * 10;
  return score;
}

/** qrPath draws the code as one SVG path (one rectangle per dark module, joined along rows), with a quiet zone of four modules. */
export function qrPath(m: boolean[][]): { d: string; size: number } {
  const q = 4;
  let d = "";
  m.forEach((row, r) => {
    let c = 0;
    while (c < row.length) {
      if (!row[c]) { c++; continue; }
      let end = c;
      while (end < row.length && row[end]) end++;
      d += `M${c + q} ${r + q}h${end - c}v1h${c - end}z`;
      c = end;
    }
  });
  return { d, size: m.length + 2 * q };
}
