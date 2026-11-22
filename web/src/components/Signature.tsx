import { useEffect, useId, useState } from "react";
import { signingApi, type SignatureResult, type SignatureState } from "../signingApi";

const LABEL: Record<SignatureState, string> = {
  verified: "Verified", unknown_key: "Unknown key", bad_signature: "Bad signature", unsigned: "Unsigned", key_revoked: "Key revoked", key_expired: "Key expired", unsupported: "Not checked",
};

/** What the server knows of the signatures of some commits or annotated tags of a hosted repository, by object name. Nothing is
 * shown while it is asking, or when the server does not have verified commits (the answer is then not an error to show). */
export function useSignatures(repo: string, shas: string[]): Record<string, SignatureResult> {
  const [got, setGot] = useState<Record<string, SignatureResult>>({});
  const key = shas.join(",");
  useEffect(() => {
    let alive = true;
    const list = key ? key.split(",").slice(0, 100) : [];
    if (list.length === 0) return;
    signingApi.signatures(repo, list).then((items) => { if (alive) setGot(Object.fromEntries(items.map((r) => [r.object, r]))); }).catch(() => {});
    return () => { alive = false; };
  }, [repo, key]);
  return got;
}

/** The mark beside a commit or tag, and, on a click, a plain explanation of what it means. Unsigned objects show nothing in a list
 * (`quiet`), so that the signed ones stand out. */
export function SignatureBadge({ result, quiet }: { result?: SignatureResult; quiet?: boolean }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  if (!result || (quiet && result.state === "unsigned")) return null;
  const tone = result.state === "verified" ? "ok" : result.state === "unsigned" || result.state === "unsupported" || result.state === "unknown_key" ? "warn" : "bad";
  return (
    <span className="sig">
      <button type="button" className={`sig-badge sig-${tone}`} aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}>
        {LABEL[result.state]}
      </button>
      {open && (
        <span id={id} className="sig-why small" role="note">
          {result.explanation}
          {result.state === "verified" && result.signer && !result.installation_key && (
            <> {result.signer_is_author ? "The signer is the author." : result.signer_is_committer ? "The signer is the committer." : "The signer is neither the author nor the committer."}</>
          )}
        </span>
      )}
    </span>
  );
}
