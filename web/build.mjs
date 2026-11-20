// Frontend build: bundle + minify with esbuild, emit content-hashed assets,
// rewrite the shell, and pre-compress everything the server will send.
// No runtime dependencies, no framework, no dev server.

import { build } from "esbuild";
import { createHash } from "node:crypto";
import { brotliCompressSync, constants, gzipSync } from "node:zlib";
import { mkdir, readFile, rm, writeFile, stat } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = path.dirname(fileURLToPath(import.meta.url));
const dist = path.join(root, "dist");
const assetsDir = path.join(dist, "assets");

await rm(dist, { recursive: true, force: true });
await mkdir(assetsDir, { recursive: true });

const result = await build({
  entryPoints: [path.join(root, "src/app.ts"), path.join(root, "src/style.css")],
  absWorkingDir: root,
  bundle: true,
  format: "esm",
  target: ["es2022"],
  minify: true,
  legalComments: "none",
  logLevel: "warning",
  metafile: true,
  outdir: assetsDir,
  entryNames: "[name].[hash]",
  assetNames: "[name].[hash]",
});

// Map entry point -> emitted hashed name.
const emitted = {};
for (const [file, meta] of Object.entries(result.metafile.outputs)) {
  if (!meta.entryPoint) continue;
  const entry = path.basename(meta.entryPoint);
  emitted[entry] = `/assets/${path.basename(file)}`;
}

const appJs = emitted["app.ts"];
const styleCss = emitted["style.css"];
if (!appJs || !styleCss) {
  throw new Error(`missing build output: ${JSON.stringify(emitted)}`);
}

// Logo: hashed copy, no optimizer needed (already hand-written SVG).
const logoSvg = await readFile(path.join(root, "public/logo.svg"));
const logoHash = createHash("sha256").update(logoSvg).digest("hex").slice(0, 8);
const logoName = `logo.${logoHash}.svg`;
await writeFile(path.join(assetsDir, logoName), logoSvg);
const logoUrl = `/assets/${logoName}`;

// Rewrite the shell.
let html = await readFile(path.join(root, "index.html"), "utf8");
html = html
  .replaceAll("__APP_JS__", appJs)
  .replaceAll("__APP_CSS__", styleCss)
  .replaceAll("__LOGO__", logoUrl);
await writeFile(path.join(dist, "index.html"), html);

// Pre-compress text assets so the Go server can serve them as-is.
const files = [
  path.join(dist, "index.html"),
  path.join(assetsDir, path.basename(appJs)),
  path.join(assetsDir, path.basename(styleCss)),
  path.join(assetsDir, logoName),
];

let total = 0;
let compressed = 0;
for (const file of files) {
  const data = await readFile(file);
  total += data.length;
  await writeFile(`${file}.br`, brotliCompressSync(data, {
    params: { [constants.BROTLI_PARAM_QUALITY]: 11 },
  }));
  await writeFile(`${file}.gz`, gzipSync(data, { level: 9 }));
  compressed += (await stat(`${file}.br`)).size;
}

// Keep the manifest free of build-dependent numbers: it is committed, and
// sizes differ between zlib builds (see the .gz/.br ignore rules).
const manifest = { app: appJs, style: styleCss, logo: logoUrl };
await writeFile(path.join(dist, "manifest.json"), JSON.stringify(manifest, null, 2) + "\n");

console.log(
  `built ${path.relative(process.cwd(), dist)}: ${(total / 1024).toFixed(1)} KiB raw, ` +
    `${(compressed / 1024).toFixed(1)} KiB brotli`,
);
