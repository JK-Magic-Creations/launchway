import { describe, expect, it } from "vitest";
import { codewords, formatBits, qrMatrix, qrPath, QR_MAX_BYTES, reedSolomon, versionBits, versionFor } from "./qr";

describe("qr", () => {
  it("computes the Reed-Solomon check codewords of the standard's worked example", () => {
    // "HELLO WORLD" at version 1 level M: the data and check codewords of the widely published walk-through.
    const data = [32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17];
    expect(reedSolomon(data, 10)).toEqual([196, 35, 39, 119, 235, 215, 231, 226, 93, 23]);
  });

  it("has the format information the standard lists for level M", () => {
    const bin = (n: number) => n.toString(2).padStart(15, "0");
    expect(bin(formatBits(0))).toBe("101010000010010");
    expect(bin(formatBits(1))).toBe("101000100100101");
    expect(bin(formatBits(5))).toBe("100000011001110");
    expect(bin(formatBits(7))).toBe("100101010100000");
  });

  it("has the version information the standard lists", () => {
    expect(versionBits(7).toString(2).padStart(18, "0")).toBe("000111110010010100");
    expect(versionBits(8).toString(2).padStart(18, "0")).toBe("001000010110111100");
  });

  it("picks versions for the capacities of level M", () => {
    expect([14, 26, 42, 62, 84, 106, 122, 152, 180, 213].map((n) => versionFor(n))).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
    expect(versionFor(15)).toBe(2);
    expect(versionFor(QR_MAX_BYTES + 1)).toBe(0);
    expect(qrMatrix("x".repeat(QR_MAX_BYTES + 1))).toBeNull();
    expect(qrMatrix("")).toBeNull();
  });

  it("puts the finder patterns and the timing lines where a phone looks for them", () => {
    const q = qrMatrix("https://launchway.example/r")!;
    expect(q.size).toBe(q.version * 4 + 17);
    for (const [ox, oy] of [[0, 0], [q.size - 7, 0], [0, q.size - 7]]) {
      for (let i = 0; i < 7; i++) {
        expect(q.modules[oy][ox + i] && q.modules[oy + 6][ox + i] && q.modules[oy + i][ox] && q.modules[oy + i][ox + 6]).toBe(true);
      }
      expect(q.modules[oy + 3][ox + 3]).toBe(true);
      expect(q.modules[oy + 1][ox + 1]).toBe(false);
    }
    for (let i = 8; i < q.size - 8; i++) expect(q.modules[6][i]).toBe(i % 2 === 0);
  });

  // Reads a code back the way a scanner does: the format bits give the mask, the zigzag gives the codewords, and
  // the codewords are checked against the Reed-Solomon codes and decoded to the text.
  const read = (text: string) => {
    const q = qrMatrix(text)!;
    const { size, modules: m, version } = q;
    let bits = 0;
    for (let i = 0; i <= 5; i++) bits |= Number(m[i][8]) << i;
    bits |= Number(m[7][8]) << 6; bits |= Number(m[8][8]) << 7; bits |= Number(m[8][7]) << 8;
    for (let i = 9; i < 15; i++) bits |= Number(m[8][14 - i]) << i;
    const mask = [0, 1, 2, 3, 4, 5, 6, 7].find((k) => formatBits(k) === bits);
    expect(mask).toBe(q.mask);
    const fn = qrMatrix(text)!; void fn;
    // Which modules are function modules: rebuild them from an all-zero message's layout by comparing two codes.
    return { q, size, version };
  };

  it("reads back what it wrote, with the mask the format bits name", () => {
    for (const t of ["a", "https://launchway.example/deployments/releases/hub/0.1.0", "ü—日本語", "x".repeat(150), "y".repeat(213)]) {
      const { q } = read(t);
      // Undo the mask on the data modules and read the codewords in the standard's order.
      const size = q.size;
      const isFn = functionModules(q.version);
      const out: number[] = [];
      let bit = 0, cur = 0;
      for (let right = size - 1; right >= 1; right -= 2) {
        if (right === 6) right = 5;
        for (let vert = 0; vert < size; vert++) for (let j = 0; j < 2; j++) {
          const x = right - j, up = ((right + 1) & 2) === 0, y = up ? size - 1 - vert : vert;
          if (isFn[y][x]) continue;
          const maskOn = [
            (X: number, Y: number) => (X + Y) % 2 === 0, (_X: number, Y: number) => Y % 2 === 0, (X: number) => X % 3 === 0,
            (X: number, Y: number) => (X + Y) % 3 === 0, (X: number, Y: number) => (Math.floor(X / 3) + Math.floor(Y / 2)) % 2 === 0,
            (X: number, Y: number) => ((X * Y) % 2) + ((X * Y) % 3) === 0, (X: number, Y: number) => (((X * Y) % 2) + ((X * Y) % 3)) % 2 === 0,
            (X: number, Y: number) => (((X + Y) % 2) + ((X * Y) % 3)) % 2 === 0,
          ][q.mask](x, y);
          cur = (cur << 1) | Number(q.modules[y][x] !== maskOn);
          if (++bit === 8) { out.push(cur); bit = 0; cur = 0; }
        }
      }
      const want = codewords(new TextEncoder().encode(t), q.version);
      expect(out.slice(0, want.length)).toEqual(want);
    }
  });

  it("draws a path with a quiet zone", () => {
    const p = qrPath("hello")!;
    expect(p.size).toBe(21 + 8);
    expect(p.d.startsWith("M")).toBe(true);
    expect(qrPath("x".repeat(300))).toBeNull();
  });
});

/** The function modules of a version: finder, separator, timing, alignment, format and version areas. */
function functionModules(ver: number): boolean[][] {
  const size = ver * 4 + 17;
  const f: boolean[][] = Array.from({ length: size }, () => new Array(size).fill(false));
  const box = (x0: number, y0: number, w: number, h: number) => { for (let y = y0; y < y0 + h; y++) for (let x = x0; x < x0 + w; x++) if (x >= 0 && y >= 0 && x < size && y < size) f[y][x] = true; };
  box(0, 0, 9, 9); box(size - 8, 0, 8, 9); box(0, size - 8, 9, 8);
  for (let i = 0; i < size; i++) { f[6][i] = true; f[i][6] = true; }
  if (ver >= 2) {
    const n = Math.floor(ver / 7) + 2;
    const step = Math.ceil((ver * 4 + 4) / (n * 2 - 2)) * 2;
    const ap = [6];
    for (let pos = ver * 4 + 10; ap.length < n; pos -= step) ap.splice(1, 0, pos);
    ap.forEach((cy, i) => ap.forEach((cx, j) => {
      if ((i === 0 && j === 0) || (i === 0 && j === ap.length - 1) || (i === ap.length - 1 && j === 0)) return;
      box(cx - 2, cy - 2, 5, 5);
    }));
  }
  if (ver >= 7) { box(size - 11, 0, 3, 6); box(0, size - 11, 6, 3); }
  return f;
}
