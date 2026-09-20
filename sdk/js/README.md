# eptaadmin-sdk

A minimal JavaScript/TypeScript client for reading data out of your EptaAdmin workspaces from your own project — a script, a backend, a frontend build step, anywhere Node.js 18+ or a browser's `fetch` is available.

This SDK is currently **read-only**: it fetches workspaces, data sources, and their values. It does not create, update, or delete data.

## Install

```sh
npm install eptaadmin-sdk
```

The package is published on the [npm registry](https://www.npmjs.com/package/eptaadmin-sdk) and versioned automatically from this repository (see `.github/workflows/publish-sdk.yml`) — every change under `sdk/js/` merged to `main` bumps the version and publishes a new release.

If you'd rather work against an unreleased change, point at the local folder instead:

```json
{
  "dependencies": {
    "eptaadmin-sdk": "file:../path/to/EptaAdmin/sdk/js"
  }
}
```

## Getting an API key

1. Log into your EptaAdmin instance.
2. Go to **Mon profil** (top-right avatar menu).
3. In the **Clés API** section, give the key a name and click **Générer une clé**.
4. Copy the key immediately — it's shown once and cannot be recovered afterwards. If you lose it, revoke it and generate a new one.

The key inherits exactly the same permissions as your account: it can only read workspaces and data sources you're already a member of.

## Quick start

```js
import { EptaAdminClient } from "eptaadmin-sdk";

const client = new EptaAdminClient({
  apiKey: process.env.EPTAADMIN_API_KEY,
  baseUrl: "https://your-eptaadmin-instance.example.com", // defaults to http://localhost:8080
});

const workspaces = await client.listWorkspaces();
// [{ name: "Acme", slug: "acme", role: "owner" }]

const dataSources = await client.listDataSources("acme");
// [{ name: "Clients", slug: "clients" }]

const clients = await client.getDataSource("acme", "clients");
// { name: "Clients", slug: "clients", columns: { nom: ["Jean Dupont"], email: ["jean@example.com"] } }

// Shortcut when you already know exactly what you want, addressed as a
// single slash-separated path instead of separate arguments:
const names = await client.getValue("acme/clients/nom");
// ["Jean Dupont", "Marie Curie"]

const firstName = await client.getValue("acme/clients/nom/0");
// "Jean Dupont"
```

## Data model: independent columns

EptaAdmin stores each column of a data source as its own independent list of values — there's no assumed row-to-row correspondence between columns. `getDataSource()` returns that shape directly:

```js
{
  "columns": {
    "nom": ["Jean Dupont", "Marie Curie"],
    "email": ["jean@example.com", "marie@example.com"]
  }
}
```

If your project needs row-aligned records, build them yourself from the columns you know are meant to line up (e.g. by index), since EptaAdmin itself doesn't guarantee that alignment.

## Image values

A column of type "image" stores each value as a URL. The server rewrites these to absolute, **signed** URLs before returning them (`{baseUrl}/api/v1/workspaces/{slug}/uploads/{file}?sig=...`) — safe to drop straight into an `<img>`/`<video>` src, since the signature (not your personal API key) is what authorizes the request. A leaked link only ever exposes that one file, never your account's broader read access — but unlike a short-lived token, it doesn't expire on its own (revoking it means rotating the server's signing secret, which invalidates every signed link at once):

```js
const avatars = await client.getValue("acme/clients/avatar");
// ["https://your-eptaadmin-instance.example.com/api/v1/workspaces/acme/uploads/6f2dff985af5d290.png?sig=..."]
```

## Build-time prefetch (for static/SPA builds)

If you're shipping a static single-page app, you usually don't want the production bundle calling out to your EptaAdmin instance at runtime — that means exposing your API key client-side, an extra network round-trip, and a hard runtime dependency on EptaAdmin staying up. There are two ways to prefetch data at **build time** instead; pick based on your bundler.

### Option A — Vite plugin (recommended for Vite): identical code in dev and prod

Your app code always calls `client.getDataSource(...)` / `client.getValue(...)` — never anything special-cased per environment, and **you don't list what to prefetch**: the plugin scans your source code for those calls (literal-string arguments only) and prefetches exactly what it finds. The plugin makes those calls resolve from build-time-fetched data with zero network request in the production bundle, while `vite` (the dev server) leaves them as real, live requests. Same code, same config, either way.

```js
// vite.config.js
import { eptaadminPrefetch } from "eptaadmin-sdk/vite";

export default {
  plugins: [
    eptaadminPrefetch({
      baseUrl: "https://admin.example.com",
      apiKeyEnv: "EPTAADMIN_API_KEY", // env var read only at build time, never bundled
      // scanDir: "src",             // defaults to "src" — where to look for SDK calls
    }),
  ],
};
```

```js
// anywhere in your app — identical in dev and prod, nothing to register anywhere
import { EptaAdminClient } from "eptaadmin-sdk";

const client = new EptaAdminClient({
  // In dev this needs a real key (e.g. from import.meta.env.VITE_EPTAADMIN_API_KEY)
  // so the live fallback request can authenticate. In a prod build that only
  // reads prefetched sources, this can be left undefined entirely — the
  // apiKey is never read unless a call actually misses the prefetch cache.
  apiKey: import.meta.env.VITE_EPTAADMIN_API_KEY,
  baseUrl: "https://admin.example.com",
});

const home = await client.getDataSource("acme", "home");
const title = await client.getValue("acme/home/hero_title/0");
```

Run `EPTAADMIN_API_KEY=eak_your_key npm run build` — the plugin finds both calls above by scanning `src/`, fetches `acme/home` once, and bakes the result into the bundle, throwing (failing the build) if the key is missing or a fetch fails. `vite` / `vite dev` are untouched, so nothing changes about your normal dev workflow.

Scanning only sees **literal string arguments** — `getDataSource("acme", "home")`, not `getDataSource(ws, ds)` with variables. This is an inherent limit of static analysis, not something a smarter regex can fix — a scanner can't know a variable's value without running the code. Rather than silently prefetching nothing for those calls, the plugin **flags every one it finds but can't resolve**, with the exact file and line, at build time:

```
[eptaadmin-prefetch] src/pages/Home.jsx:42 — getDataSource(ws, ds) has non-literal arguments, can't be scanned; add it via "sources" if it should be prefetched.
```

Add exactly the pairs it points out via `sources`:

```js
eptaadminPrefetch({
  baseUrl: "https://admin.example.com",
  sources: [{ workspace: "acme", dataSource: "home" }], // merged with whatever scanning finds
});
```

### Option B — CLI (any bundler): static JSON file you import yourself

If you're not on Vite, or you'd rather have an explicit static JSON file, the `eptaadmin-prefetch` CLI (installed alongside the SDK) does the fetching and writes plain JSON files instead of hooking into a bundler.

Add a config file (`eptaadmin.config.json`, resolved from your current working directory):

```json
{
  "baseUrl": "https://admin.example.com",
  "apiKeyEnv": "EPTAADMIN_API_KEY",
  "sources": [
    { "workspace": "acme", "dataSource": "home", "out": "src/data/home.json" }
  ]
}
```

Run it before your build, e.g. as a `prebuild` script in `package.json`:

```json
{
  "scripts": {
    "prebuild": "eptaadmin-prefetch",
    "build": "vite build"
  }
}
```

```sh
EPTAADMIN_API_KEY=eak_your_key npm run build
```

Each output file matches `getDataSource()`'s `columns` shape exactly:

```js
import homeData from "./data/home.json";
// homeData.hero_title[0], homeData.testimonial_author, ...
```

Unlike Option A, this means writing a plain `import` instead of an `EptaAdminClient` call, so dev and prod aren't using identical code — the tradeoff for working with any build tool, not just Vite.

Both options exit non-zero (Option A: the build fails outright; Option B: the CLI process fails) if a source can't be fetched or the API key is missing, so a broken EptaAdmin connection fails your build loudly instead of silently shipping stale or missing data.

## Error handling

Failed requests reject with an `EptaAdminError` carrying the HTTP status and the server's error message:

```js
import { EptaAdminClient, EptaAdminError } from "eptaadmin-sdk";

try {
  await client.getDataSource("acme", "does-not-exist");
} catch (err) {
  if (err instanceof EptaAdminError) {
    console.error(err.status, err.message); // 404 "Source de données introuvable."
  }
}
```

## API reference

- `new EptaAdminClient({ apiKey, baseUrl? })`
- `client.listWorkspaces(): Promise<{ name, slug, role }[]>`
- `client.listDataSources(workspaceSlug): Promise<{ name, slug }[]>`
- `client.getDataSource(workspaceSlug, dataSourceSlug): Promise<{ name, slug, columns }>`
- `client.getValue(path): Promise<unknown>` — `path` is `"wsSlug/dsSlug/column"` (returns that column's full array) or `"wsSlug/dsSlug/column/index"` (returns one value)
