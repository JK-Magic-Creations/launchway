import { describe, expect, it } from "vitest";
import { encodeQR, qrPath } from "./qr";

// The matrix for "hi" was decoded back to "hi" by an independent QR decoder (OpenCV), as were codes of versions 5, 6, 9 and 10
// (an authenticator address, 100, 180 and 213 bytes): a change to the encoder that breaks decoding changes this matrix.
const HI = ["111111100111101111111","100000100110101000001","101110101101101011101","101110101100101011101","101110101001101011101","100000101100101000001","111111101010101111111","000000001011100000000","101111100000101111100","011101010010100100001","001100110101010011110","111010000100000110100","111010100001010010101","000000001001111001001","111111100010101100010","100000101111111001001","101110101000100100100","101110101110100100100","101110101001010011100","100000100110000110100","111111101011010011110"];

describe("encodeQR", () => {
  it("makes the checked code for a short text", () => {
    const m = encodeQR("hi")!;
    expect(m.map((r) => r.map((v) => (v ? "1" : "0")).join(""))).toEqual(HI);
  });
  it("has the fixed patterns of a QR code at every size", () => {
    for (const len of [2, 30, 60, 100, 170, 213]) {
      const m = encodeQR("a".repeat(len))!;
      const n = m.length;
      expect((n - 17) % 4).toBe(0);
      for (const [r0, c0] of [[0, 0], [0, n - 7], [n - 7, 0]]) {
        expect(m[r0][c0] && m[r0][c0 + 6] && m[r0 + 6][c0] && m[r0 + 6][c0 + 6]).toBe(true); // the corners of a finder pattern
        expect(m[r0 + 3][c0 + 3]).toBe(true);
        expect(m[r0 + 1][c0 + 1]).toBe(false);
      }
      for (let i = 8; i < n - 8; i++) expect(m[6][i]).toBe(i % 2 === 0); // the timing pattern
      expect(m[n - 8][8]).toBe(true);
    }
  });
  it("refuses a text that does not fit rather than drawing a wrong code", () => {
    expect(encodeQR("a".repeat(214))).toBeNull();
  });
  it("is the same every time", () => {
    expect(encodeQR("otpauth://totp/Launchway:ada?secret=ABC")).toEqual(encodeQR("otpauth://totp/Launchway:ada?secret=ABC"));
  });
});

describe("qrPath", () => {
  it("draws one path with a quiet zone of four modules", () => {
    const m = encodeQR("hi")!;
    const p = qrPath(m);
    expect(p.size).toBe(m.length + 8);
    expect(p.d.startsWith("M4 4")).toBe(true);
  });
});
