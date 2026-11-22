import { qrPath, QR_MAX_BYTES } from "../qr";

/**
 * A QR code drawn in the browser (no request, no image file): a phone's camera opens the address it holds. Dark
 * modules are always black on white, whatever the theme, because cameras read dark on light. When the address is too
 * long for a code, nothing is drawn and the caller's text address stays the way in.
 */
export function QrCode({ value, label, size = 148 }: { value: string; label: string; size?: number }) {
  const p = qrPath(value);
  if (!p) return null;
  return (
    <svg className="qr" role="img" aria-label={label} width={size} height={size} viewBox={`0 0 ${p.size} ${p.size}`} shapeRendering="crispEdges" data-qr-value={value}>
      <title>{label}</title>
      <rect width={p.size} height={p.size} fill="#fff" />
      <path d={p.d} fill="#000" />
    </svg>
  );
}

/** Whether an address fits in a code. */
export const fitsInQr = (value: string) => new TextEncoder().encode(value).length <= QR_MAX_BYTES;
