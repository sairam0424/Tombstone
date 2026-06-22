// MurmurHash3 32-bit (x86) — matches murmurhash npm package v3 output
// Pure TypeScript, no Node.js APIs, compatible with Cloudflare Workers
export function murmur3_32(str: string, seed = 0): number {
  let h1 = seed >>> 0;
  const c1 = 0xcc9e2d51;
  const c2 = 0x1b873593;

  // Convert string to UTF-16 code units (JS native, no TextEncoder needed)
  const len = str.length;
  let i = 0;

  // Process 4-byte chunks
  while (i <= len - 4) {
    let k =
      (str.charCodeAt(i) & 0xff) |
      ((str.charCodeAt(i + 1) & 0xff) << 8) |
      ((str.charCodeAt(i + 2) & 0xff) << 16) |
      ((str.charCodeAt(i + 3) & 0xff) << 24);
    k = Math.imul(k, c1);
    k = ((k << 15) | (k >>> 17)) >>> 0;
    k = Math.imul(k, c2);
    h1 ^= k;
    h1 = ((h1 << 13) | (h1 >>> 19)) >>> 0;
    h1 = (Math.imul(h1, 5) + 0xe6546b64) >>> 0;
    i += 4;
  }

  // Handle remaining bytes
  let k = 0;
  switch (len - i) {
    case 3: k ^= (str.charCodeAt(i + 2) & 0xff) << 16; // falls through
    case 2: k ^= (str.charCodeAt(i + 1) & 0xff) << 8;  // falls through
    case 1:
      k ^= str.charCodeAt(i) & 0xff;
      k = Math.imul(k, c1);
      k = ((k << 15) | (k >>> 17)) >>> 0;
      k = Math.imul(k, c2);
      h1 ^= k;
  }

  // Finalization mix
  h1 ^= len;
  h1 ^= h1 >>> 16;
  h1 = Math.imul(h1, 0x85ebca6b);
  h1 ^= h1 >>> 13;
  h1 = Math.imul(h1, 0xc2b2ae35);
  h1 ^= h1 >>> 16;
  return h1 >>> 0; // unsigned 32-bit
}
