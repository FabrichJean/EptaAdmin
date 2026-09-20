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
