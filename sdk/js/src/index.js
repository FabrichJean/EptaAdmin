/**
 * EptaAdmin SDK — a minimal client for the EptaAdmin public read-only API.
 * Works in Node.js (18+, for global fetch) and in browsers.
 */

export class EptaAdminError extends Error {
  constructor(message, status) {
    super(message);
    this.name = "EptaAdminError";
    this.status = status;
  }
}

// Image column values are stored as the same URL the browser UI uses
// (/workspaces/{slug}/uploads/{file}), which only accepts a session
// cookie — useless to an external caller like this SDK, which has none.
// Rewrite matching values to the API-key-authenticated equivalent instead
// of just prefixing baseUrl onto the internal route, so callers get a
// working absolute URL without depending on (or seeing) that internal
// browser-route shape.
//
// The key travels as a "?apiKey=" query parameter, not an Authorization
// header, because the whole point of this URL is to be usable directly as
// an <img>/<video> src — a browser's native resource fetch for those never
// attaches custom headers, so a header-only scheme would make the URL this
// returns unusable for that (the single most common reason to want it).
const uploadUrlPattern = /^\/workspaces\/([^/]+)\/uploads\/(.+)$/;

function resolveImageURL(value, baseUrl, apiKey) {
  if (typeof value !== "string") return value;
  const match = value.match(uploadUrlPattern);
  if (!match) return value;
  const url = `${baseUrl}/api/v1/workspaces/${match[1]}/uploads/${match[2]}`;
  return apiKey ? `${url}?apiKey=${encodeURIComponent(apiKey)}` : url;
}

function resolveImageURLs(value, baseUrl, apiKey) {
  return Array.isArray(value)
    ? value.map((v) => resolveImageURL(v, baseUrl, apiKey))
    : resolveImageURL(value, baseUrl, apiKey);
}

// Populated by build tooling (see eptaadmin-sdk/vite) via a bundler `define`
// so the exact same `client.getDataSource()` / `client.getValue()` calls
// resolve from build-time-fetched data in a production bundle, with zero
// runtime request — while resolving live in dev, where that identifier is
// either undefined or defined as `{}`. `typeof` is deliberate: it's the one
// operator that never throws on an identifier no bundler has declared at
// all, so the SDK works unmodified outside of Vite too.
const PREFETCHED = typeof __EPTAADMIN_PREFETCH_DATA__ !== "undefined" ? __EPTAADMIN_PREFETCH_DATA__ : {};

export class EptaAdminClient {
  /**
   * @param {{ apiKey?: string, baseUrl?: string }} options
   *   apiKey  — a personal API key generated from the EptaAdmin profile page.
   *             Only required for calls that actually reach the network —
   *             a call fully served from build-time-prefetched data (see
   *             eptaadmin-sdk/vite) never needs one, so it's fine to leave
   *             unset in a production bundle that only reads prefetched
   *             sources.
   *   baseUrl — the URL of your EptaAdmin instance (default: http://localhost:8080).
   */
  constructor({ apiKey, baseUrl = "http://localhost:8080" } = {}) {
    this.apiKey = apiKey;
    this.baseUrl = baseUrl.replace(/\/$/, "");
  }

  async _request(path) {
    if (!this.apiKey) {
      throw new Error("EptaAdminClient requires an apiKey for this call (it wasn't served from prefetched data)");
    }
    const res = await fetch(this.baseUrl + path, {
      headers: { Authorization: `Bearer ${this.apiKey}` },
    });
    const body = await res.json().catch(() => null);
    if (!res.ok) {
      const message = (body && body.error) || `Request failed with status ${res.status}`;
      throw new EptaAdminError(message, res.status);
    }
    return body;
  }

  /** Lists the workspaces this API key's user can access. */
  listWorkspaces() {
    return this._request("/api/v1/workspaces");
  }

  /** Lists the data sources in a workspace. */
  listDataSources(workspaceSlug) {
    return this._request(`/api/v1/workspaces/${encodeURIComponent(workspaceSlug)}/datasources`);
  }

  /**
   * Fetches a data source's content. EptaAdmin stores each column as an
   * independent list of values (no assumed row-to-row correspondence
   * between columns), so the response mirrors that: `columns` is a plain
   * object of `{ [columnKey]: value[] }`.
   */
  async getDataSource(workspaceSlug, dataSourceSlug) {
    const cached = PREFETCHED[`${workspaceSlug}/${dataSourceSlug}`];
    const body = cached
      ? { name: cached.name, slug: cached.slug, columns: { ...cached.columns } }
      : await this._request(
          `/api/v1/workspaces/${encodeURIComponent(workspaceSlug)}/datasources/${encodeURIComponent(dataSourceSlug)}`
        );
    for (const key of Object.keys(body.columns || {})) {
      body.columns[key] = resolveImageURLs(body.columns[key], this.baseUrl, this.apiKey);
    }
    return body;
  }

  /**
   * One-parameter shortcut to a single column or a single value, addressed
   * by a slash-separated path instead of separate arguments:
   *   - `"wsSlug/dsSlug/column"`       -> that column's full value[] list.
   *   - `"wsSlug/dsSlug/column/index"` -> exactly one value at that index.
   *
   * @param {string} path
   */
  async getValue(path) {
    const segments = String(path).split("/").filter(Boolean);
    if (segments.length !== 3 && segments.length !== 4) {
      throw new Error(
        `getValue expects "workspace/dataSource/column" or "workspace/dataSource/column/index", got "${path}"`
      );
    }
    const [workspaceSlug, dataSourceSlug, column, index] = segments;

    const cached = PREFETCHED[`${workspaceSlug}/${dataSourceSlug}`];
    if (cached) {
      const values = (cached.columns || {})[column];
      if (values === undefined) {
        throw new EptaAdminError(`column "${column}" not found`, 404);
      }
      const result = index !== undefined ? values[Number(index)] : values;
      if (index !== undefined && result === undefined) {
        throw new EptaAdminError(`index ${index} out of range for column "${column}"`, 404);
      }
      return resolveImageURLs(result, this.baseUrl, this.apiKey);
    }

    let url =
      `/api/v1/workspaces/${encodeURIComponent(workspaceSlug)}` +
      `/datasources/${encodeURIComponent(dataSourceSlug)}` +
      `/columns/${encodeURIComponent(column)}`;
    if (index !== undefined) {
      url += `/${encodeURIComponent(index)}`;
    }
    const body = await this._request(url);
    return resolveImageURLs(index !== undefined ? body.value : body.values, this.baseUrl, this.apiKey);
  }
}
