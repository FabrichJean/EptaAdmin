#!/usr/bin/env node
/**
 * eptaadmin-prefetch — fetches EptaAdmin data sources at BUILD time and
 * writes them to static JSON files, so a production SPA build never calls
 * out to your EptaAdmin instance at runtime. Import the generated file(s)
 * like any other static asset instead of calling EptaAdminClient in the
 * shipped bundle.
 *
 * Usage:
 *   npx eptaadmin-prefetch [--config eptaadmin.config.json]
 *
 * Typical setup: add a "prebuild" script to package.json —
 *   "prebuild": "eptaadmin-prefetch"
 * — so it always runs right before "build".
 *
 * Config file (JSON), resolved relative to the current working directory:
 * {
 *   "baseUrl": "https://admin.example.com",
 *   "apiKeyEnv": "EPTAADMIN_API_KEY",   // env var holding the API key — never put the key itself in this file
 *   "sources": [
 *     { "workspace": "acme", "dataSource": "home", "out": "src/data/home.json" }
 *   ]
 * }
 *
 * Each output file is the same shape as EptaAdminClient#getDataSource()'s
 * "columns" object: { "columnKey": [value, ...] }.
 */

import { readFile, writeFile, mkdir } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { EptaAdminClient } from "../src/index.js";

function parseArgs(argv) {
  const args = { config: "eptaadmin.config.json" };
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === "--config" && argv[i + 1]) {
      args.config = argv[++i];
    }
  }
  return args;
}

async function loadConfig(path) {
  let raw;
  try {
    raw = await readFile(path, "utf8");
  } catch (err) {
    throw new Error(`could not read config file "${path}": ${err.message}`);
  }
  let config;
  try {
    config = JSON.parse(raw);
  } catch (err) {
    throw new Error(`config file "${path}" is not valid JSON: ${err.message}`);
  }
  if (!config.baseUrl) throw new Error(`config is missing "baseUrl"`);
  if (!Array.isArray(config.sources) || config.sources.length === 0) {
    throw new Error(`config must list at least one entry under "sources"`);
  }
  return config;
}

async function main() {
  const { config: configPath } = parseArgs(process.argv.slice(2));
  const config = await loadConfig(configPath);

  const apiKeyEnv = config.apiKeyEnv || "EPTAADMIN_API_KEY";
  const apiKey = process.env[apiKeyEnv];
  if (!apiKey) {
    throw new Error(`environment variable "${apiKeyEnv}" is not set (or empty) — it must hold a personal API key from your EptaAdmin profile page`);
  }

  const client = new EptaAdminClient({ apiKey, baseUrl: config.baseUrl });

  let failures = 0;
  for (const source of config.sources) {
    const { workspace, dataSource, out } = source;
    if (!workspace || !dataSource || !out) {
      console.error(`✗ skipping invalid source entry (needs "workspace", "dataSource" and "out"): ${JSON.stringify(source)}`);
      failures++;
      continue;
    }
    try {
      const data = await client.getDataSource(workspace, dataSource);
      const outPath = resolve(process.cwd(), out);
      await mkdir(dirname(outPath), { recursive: true });
      await writeFile(outPath, JSON.stringify(data.columns, null, 2) + "\n", "utf8");
      console.log(`✓ ${workspace}/${dataSource} → ${out}`);
    } catch (err) {
      console.error(`✗ ${workspace}/${dataSource}: ${err.message}`);
      failures++;
    }
  }

  if (failures > 0) {
    console.error(`\neptaadmin-prefetch: ${failures} source(s) failed — failing the build rather than shipping stale or missing data.`);
    process.exit(1);
  }
}

main().catch((err) => {
  console.error(`eptaadmin-prefetch: ${err.message}`);
  process.exit(1);
});
