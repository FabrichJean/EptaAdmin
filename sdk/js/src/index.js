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

export class EptaAdminClient {
  /**
   * @param {{ apiKey: string, baseUrl?: string }} options
   *   apiKey  — a personal API key generated from the EptaAdmin profile page.
   *   baseUrl — the URL of your EptaAdmin instance (default: http://localhost:8080).
   */
  constructor({ apiKey, baseUrl = "http://localhost:8080" } = {}) {
    if (!apiKey) {
      throw new Error("EptaAdminClient requires an apiKey");
    }
    this.apiKey = apiKey;
    this.baseUrl = baseUrl.replace(/\/$/, "");
  }

  async _request(path) {
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
  getDataSource(workspaceSlug, dataSourceSlug) {
    return this._request(
      `/api/v1/workspaces/${encodeURIComponent(workspaceSlug)}/datasources/${encodeURIComponent(dataSourceSlug)}`
    );
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
    let url =
      `/api/v1/workspaces/${encodeURIComponent(workspaceSlug)}` +
      `/datasources/${encodeURIComponent(dataSourceSlug)}` +
      `/columns/${encodeURIComponent(column)}`;
    if (index !== undefined) {
      url += `/${encodeURIComponent(index)}`;
    }
    const body = await this._request(url);
    return index !== undefined ? body.value : body.values;
  }
}
