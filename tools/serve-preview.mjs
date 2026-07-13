import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, resolve, sep } from "node:path";

const root = resolve(import.meta.dirname, "..");
const port = Number.parseInt(process.env.PORT || "4173", 10);
const types = { ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".svg": "image/svg+xml", ".png": "image/png" };

createServer(async (request, response) => {
  try {
    const url = new URL(request.url, `http://127.0.0.1:${port}`);
    const relative = decodeURIComponent(url.pathname).replace(/^\/+/, "");
    const path = resolve(root, relative || "extension/src/ui/index.html");
    if (path !== root && !path.startsWith(root + sep)) throw new Error("outside preview root");
    let content = await readFile(path);
    if (path.endsWith(`${sep}extension${sep}src${sep}ui${sep}index.html`) && url.searchParams.has("preview")) {
      content = Buffer.from(content.toString("utf8").replace('<script type="module" src="app.js"></script>', '<script type="module" src="/tools/mock-chrome.js"></script><script type="module" src="app.js"></script>'));
    }
    response.writeHead(200, { "Content-Type": types[extname(path)] || "application/octet-stream", "Cache-Control": "no-store" });
    response.end(content);
  } catch {
    response.writeHead(404, { "Content-Type": "text/plain; charset=utf-8" }); response.end("Not found");
  }
}).listen(port, "127.0.0.1", () => console.log(`ScopeNest preview: http://127.0.0.1:${port}/extension/src/ui/index.html?preview=1`));
