// UUIDv7 request ids, the same format internal/protocol.NewID produces: 48-bit big-endian
// millisecond timestamp, version nibble 7, variant bits 10, then random.

const toHex = (bytes: Uint8Array): string => {
  let s = "";
  for (const b of bytes) s += b.toString(16).padStart(2, "0");
  return s;
};

/** A UUIDv7 string: time-ordered so daemon logs and store records sort by creation. */
export function newID(now: number = Date.now()): string {
  const rand = new Uint8Array(10);
  crypto.getRandomValues(rand);
  const millis = BigInt(now);

  const bytes = new Uint8Array(16);
  const dv = new DataView(bytes.buffer);
  dv.setUint16(0, Number((millis >> 32n) & 0xffffn));
  dv.setUint32(2, Number(millis & 0xffffffffn));
  bytes[6] = (rand[0] & 0x0f) | 0x70;
  bytes[7] = rand[1];
  bytes[8] = (rand[2] & 0x3f) | 0x80;
  for (let i = 3; i < 10; i++) bytes[i + 6] = rand[i];

  const h = toHex(bytes);
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}
