/**
 * Vite plugin: scans your project's source code for `client.getDataSource(...)`
 * / `client.getValue(...)` calls, fetches exactly those data sources once at
 * `vite build` time, and bakes the result into the bundle — so those same
 * calls resolve from static data with zero runtime request in production,
 * with no config listing what to prefetch and nothing to keep in sync by
 * hand. `vite` (dev server) is left untouched, so the exact same client
 * code keeps making live requests during development.
 */
import { readFile, readdir } from "node:fs/promises";
import { join, extname } from "node:path";
import { EptaAdminClient } from "./src/index.js";

const SCANNABLE_EXTENSIONS = new Set([".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".vue", ".svelte"]);
const SKIP_DIRS = new Set(["node_modules", ".git", "dist", "build", ".vite", ".next", "coverage"]);

// Matches getDataSource("ws", "ds") / getDataSource('ws', "ds") with any
// quote style, tolerant of whitespace — literal string arguments only,
// since this is static source scanning, not a real JS parser.
const GET_DATA_SOURCE_RE = /getDataSource\s*\(\s*(['"`])([^'"`]+)\1\s*,\s*(['"`])([^'"`]+)\3\s*\)/g;
// Matches getValue("ws/ds/column") / getValue("ws/ds/column/index") —
// only the workspace/dataSource prefix is needed to know what to prefetch.
const GET_VALUE_RE = /getValue\s*\(\s*(['"`])([^'"`]+)\1\s*\)/g;

// Broader "any call, any arguments" versions of the two above, used only to
// flag calls scanning *can't* resolve (e.g. variables) — doesn't handle
// arguments containing nested parens/commas, but that's fine for a warning
// whose job is just to point a human at the right line.
const ANY_GET_DATA_SOURCE_CALL_RE = /getDataSource\s*\(([^)]*)\)/g;
const ANY_GET_VALUE_CALL_RE = /getValue\s*\(([^)]*)\)/g;
const LITERAL_STRING_ARG_RE = /^\s*['"`][^'"`]*['"`]\s*$/;

function lineNumberAt(text, index) {
  let line = 1;
  for (let i = 0; i < index; i++) {
    if (text.charCodeAt(i) === 10) line++;
  }
  return line;
}

async function collectFiles(dir, out) {
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch {
    return; // missing/unreadable dir — nothing to scan there
  }
  for (const entry of entries) {
    if (entry.isDirectory()) {
      if (SKIP_DIRS.has(entry.name)) continue;
      await collectFiles(join(dir, entry.name), out);
    } else if (SCANNABLE_EXTENSIONS.has(extname(entry.name))) {
      out.push(join(dir, entry.name));
    }
  }
}

/** Scans every source file under `scanDir` for SDK calls with literal
 * string arguments, returning the unique {workspace, dataSource} pairs
 * actually referenced in the code, plus a warning for every call scanning
 * found but couldn't resolve (non-literal arguments) — so a developer can
 * fix each one by adding it to `sources` instead of it failing silently. */
async function scanForSources(scanDir) {
  const files = [];
  await collectFiles(scanDir, files);

  const found = new Map(); // "workspace/dataSource" -> {workspace, dataSource}
  const warnings = [];

  for (const file of files) {
    let text;
    try {
      text = await readFile(file, "utf8");
    } catch {
      continue;
    }

    const resolvedCallOffsets = new Set();
    for (const m of text.matchAll(GET_DATA_SOURCE_RE)) {
      const [, , workspace, , dataSource] = m;
      found.set(`${workspace}/${dataSource}`, { workspace, dataSource });
      resolvedCallOffsets.add(m.index);
    }
    for (const m of text.matchAll(GET_VALUE_RE)) {
      const segments = m[2].split("/").filter(Boolean);
      if (segments.length >= 3) {
        const [workspace, dataSource] = segments;
        found.set(`${workspace}/${dataSource}`, { workspace, dataSource });
      }
      resolvedCallOffsets.add(m.index);
    }

    for (const m of text.matchAll(ANY_GET_DATA_SOURCE_CALL_RE)) {
      if (resolvedCallOffsets.has(m.index)) continue;
      const args = m[1].split(",");
      const isLiteral = args.length === 2 && args.every((a) => LITERAL_STRING_ARG_RE.test(a));
      if (!isLiteral) {
        warnings.push(`${file}:${lineNumberAt(text, m.index)} — getDataSource(${m[1].trim()}) has non-literal arguments, can't be scanned; add it via "sources" if it should be prefetched.`);
      }
    }
    for (const m of text.matchAll(ANY_GET_VALUE_CALL_RE)) {
      if (resolvedCallOffsets.has(m.index)) continue;
      if (!LITERAL_STRING_ARG_RE.test(m[1])) {
        warnings.push(`${file}:${lineNumberAt(text, m.index)} — getValue(${m[1].trim()}) has a non-literal argument, can't be scanned; add its {workspace, dataSource} via "sources" if it should be prefetched.`);
      }
    }
  }

  return { sources: [...found.values()], warnings };
}

/**
 * @param {{
 *   baseUrl: string,
 *   apiKeyEnv?: string,
 *   scanDir?: string,
 *   sources?: { workspace: string, dataSource: string }[],
 * }} options
 *   scanDir — directory to scan for SDK calls, relative to Vite's root.
 *             Defaults to "src".
 *   sources — extra {workspace, dataSource} pairs to prefetch on top of
 *             whatever scanning finds — for calls whose arguments aren't
 *             literal strings (e.g. a variable), which scanning can't see.
 */
export function eptaadminPrefetch(options = {}) {
  const { baseUrl, apiKeyEnv = "EPTAADMIN_API_KEY", scanDir = "src", sources: extraSources = [] } = options;
  if (!baseUrl) throw new Error("eptaadminPrefetch: \"baseUrl\" is required");

  return {
    name: "eptaadmin-prefetch",
    async config(config, { command }) {
      // Dev server: leave the injected data empty so every client call in
      // this mode falls through to a real, live fetch — same code path,
      // just not pre-resolved.
      let data = {};

      if (command === "build") {
        const apiKey = process.env[apiKeyEnv];
        if (!apiKey) {
          throw new Error(
            `eptaadmin-prefetch: environment variable "${apiKeyEnv}" is not set — it must hold a personal API key from your EptaAdmin profile page to prefetch data at build time`
          );
        }

        const root = config.root || process.cwd();
        const { sources: scanned, warnings } = await scanForSources(join(root, scanDir));
        for (const w of warnings) console.warn(`[eptaadmin-prefetch] ${w}`);

        const byKey = new Map(scanned.map((s) => [`${s.workspace}/${s.dataSource}`, s]));
        for (const s of extraSources) byKey.set(`${s.workspace}/${s.dataSource}`, s);
        const sources = [...byKey.values()];

        if (sources.length === 0) {
          console.warn(
            `[eptaadmin-prefetch] found no getDataSource()/getValue() calls with literal arguments under "${scanDir}" — nothing to prefetch. ` +
              `If your calls use variables instead of string literals, list them explicitly via the "sources" option.`
          );
        }

        const client = new EptaAdminClient({ apiKey, baseUrl });
        for (const { workspace, dataSource } of sources) {
          const result = await client.getDataSource(workspace, dataSource);
          data[`${workspace}/${dataSource}`] = result;
          console.log(`[eptaadmin-prefetch] ✓ ${workspace}/${dataSource}`);
        }
      }

      return {
        define: {
          __EPTAADMIN_PREFETCH_DATA__: JSON.stringify(data),
        },
      };
    },
  };
}
