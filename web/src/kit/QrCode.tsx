import "./qr.css";
import { useMemo } from "react";
import { encodeQR, qrPath } from "./qr";

/** QrCode draws text as a QR code, as inline SVG made in the page: nothing is requested from anywhere. When the text is
 * too long to draw, it draws nothing, and the caller shows the text itself (a setup key) as it always does. */
export function QrCode({ text, label, size = 200 }: { text: string; label: string; size?: number }) {
  const art = useMemo(() => { const m = encodeQR(text); return m ? qrPath(m) : null; }, [text]);
  if (!art) return null;
  return (
    <svg className="qr" viewBox={`0 0 ${art.size} ${art.size}`} role="img" aria-label={label} width={size} height={size} shapeRendering="crispEdges">
      <rect width={art.size} height={art.size} fill="#fff" />
      <path d={art.d} fill="#000" />
    </svg>
  );
}
