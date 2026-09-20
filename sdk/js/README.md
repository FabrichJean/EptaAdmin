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

A column of type "image" stores each value as a URL. The SDK automatically rewrites these to absolute, API-key-authenticated URLs (`{baseUrl}/api/v1/workspaces/{slug}/uploads/{file}`) before returning them, so they're directly usable outside a browser session — you never see or depend on the internal browser-app route these are stored against:

```js
const avatars = await client.getValue("acme/clients/avatar");
// ["https://your-eptaadmin-instance.example.com/api/v1/workspaces/acme/uploads/6f2dff985af5d290.png"]
```

## Build-time prefetch (for static/SPA builds)

If you're shipping a static single-page app (Vite, CRA, etc.), you usually don't want the production bundle calling out to your EptaAdmin instance at runtime — that means exposing your API key client-side, an extra network round-trip, and a hard runtime dependency on EptaAdmin staying up. The `eptaadmin-prefetch` CLI (installed alongside the SDK) fetches your data sources once, at **build time**, and writes them to plain JSON files your app imports like any other static asset.

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

The API key itself is read from the environment variable named by `apiKeyEnv` (default `EPTAADMIN_API_KEY`) — never put the key in the config file, since that file is typically committed.

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

Each output file matches `getDataSource()`'s `columns` shape exactly, so your app just does:

```js
import homeData from "./data/home.json";
// homeData.hero_title[0], homeData.testimonial_author, ...
```

The command exits with a non-zero status if any source fails to fetch, so a broken EptaAdmin connection fails your CI build loudly instead of silently shipping stale or missing data.

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
