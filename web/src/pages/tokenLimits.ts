// Reading what a person types into a token's limits, and saying plainly what is wrong with it.

export type Parsed = { ok: true; values: string[] } | { ok: false; problem: string };

const NAME = /^[a-z0-9][a-z0-9._-]{0,62}$/;

const items = (text: string) => text.split(/[,\s]+/).map((s) => s.trim()).filter(Boolean);

/** Repositories: owner/name, or owner/* for every repository of one owner. */
export function parseRepositories(text: string): Parsed {
  const values: string[] = [];
  for (const v of items(text)) {
    const [owner, name, ...rest] = v.split("/");
    if (rest.length || owner === undefined || name === undefined || !NAME.test(owner) || !(name === "*" || NAME.test(name))) {
      return { ok: false, problem: `"${v}" is not a repository: write owner/name, or owner/* for every repository of an owner.` };
    }
    if (!values.includes(v)) values.push(v);
  }
  if (values.length > 50) return { ok: false, problem: "At most 50 repositories." };
  return { ok: true, values };
}

function ipv4(s: string): boolean {
  const p = s.split(".");
  return p.length === 4 && p.every((x) => /^\d{1,3}$/.test(x) && Number(x) <= 255);
}

function ipv6(s: string): boolean {
  if (!/^[0-9a-fA-F:.]+$/.test(s) || !s.includes(":") || s.split("::").length > 2) return false;
  return s.split(":").length <= 8 && s.split(":").every((g) => g === "" || /^[0-9a-fA-F]{1,4}$/.test(g) || ipv4(g));
}

/** Address ranges: CIDR (203.0.113.0/24), or a single address. */
export function parseRanges(text: string): Parsed {
  const values: string[] = [];
  for (const v of items(text)) {
    const [addr, bits, ...rest] = v.split("/");
    const v4 = ipv4(addr ?? ""), v6 = !v4 && ipv6(addr ?? "");
    const max = v4 ? 32 : 128;
    const badBits = bits !== undefined && (!/^\d{1,3}$/.test(bits) || Number(bits) > max || Number(bits) === 0);
    if (rest.length || (!v4 && !v6) || badBits) {
      return { ok: false, problem: `"${v}" is not an address range: write it like 203.0.113.0/24 or 2001:db8::/32, or one address.` };
    }
    values.push(v);
  }
  if (values.length > 20) return { ok: false, problem: "At most 20 address ranges." };
  return { ok: true, values };
}
