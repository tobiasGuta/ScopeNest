import { deflateSync } from "node:zlib";
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";

const sizes = [16, 32, 48, 128];
const output = resolve("extension/assets");
mkdirSync(output, { recursive: true });

const crcTable = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k += 1) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});

function crc32(buffer) {
  let c = 0xffffffff;
  for (const byte of buffer) c = crcTable[(c ^ byte) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function chunk(type, data) {
  const name = Buffer.from(type);
  const result = Buffer.alloc(12 + data.length);
  result.writeUInt32BE(data.length, 0);
  name.copy(result, 4);
  data.copy(result, 8);
  result.writeUInt32BE(crc32(Buffer.concat([name, data])), 8 + data.length);
  return result;
}

function roundedRect(x, y, width, height, radius, px, py) {
  const cx = Math.max(x + radius, Math.min(px, x + width - radius));
  const cy = Math.max(y + radius, Math.min(py, y + height - radius));
  return (px - cx) ** 2 + (py - cy) ** 2 <= radius ** 2;
}

function icon(size) {
  const data = Buffer.alloc((size * 4 + 1) * size);
  const scale = size / 128;
  function paint(x, y, color) {
    const offset = y * (size * 4 + 1) + 1 + x * 4;
    data.set(color, offset);
  }
  function withinBox(px, py, x, y, w, h, r) {
    return px >= x && px < x + w && py >= y && py < y + h && roundedRect(x, y, w, h, r, px, py);
  }
  for (let y = 0; y < size; y += 1) {
    for (let x = 0; x < size; x += 1) {
      const px = (x + 0.5) / scale;
      const py = (y + 0.5) / scale;
      let color = [0, 0, 0, 0];
      if (withinBox(px, py, 8, 8, 112, 112, 28)) {
        const t = (px + py - 20) / 200;
        color = [Math.round(114 - 56 * t), Math.round(92 - 53 * t), Math.round(255 - 68 * t), 255];
      }
      const outer = withinBox(px, py, 25, 27, 78, 68, 11);
      const outerInner = withinBox(px, py, 32, 34, 64, 54, 5);
      if (outer && !outerInner) color = [255, 255, 255, 255];
      if (py >= 43 && py <= 50 && px >= 27 && px <= 101) color = [255, 255, 255, 255];
      const middle = withinBox(px, py, 42, 54, 46, 40, 9);
      const middleInner = withinBox(px, py, 48, 60, 34, 31, 4);
      if (middle && !middleInner) color = [191, 247, 232, 255];
      if (withinBox(px, py, 57, 66, 29, 27, 6)) color = [255, 255, 255, 255];
      if (withinBox(px, py, 62, 71, 19, 17, 2)) color = [31, 23, 95, 255];
      paint(x, y, color);
    }
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0); ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; ihdr[9] = 6;
  return Buffer.concat([
    Buffer.from("89504e470d0a1a0a", "hex"),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(data, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

for (const size of sizes) writeFileSync(resolve(output, `icon-${size}.png`), icon(size));
console.log(`Generated ${sizes.length} ScopeNest icons in ${dirname(resolve(output, "icon-16.png"))}`);
