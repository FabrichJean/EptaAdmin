export interface Workspace {
  name: string;
  slug: string;
  role: string;
}

export interface DataSourceSummary {
  name: string;
  slug: string;
}

export interface DataSourceContent {
  name: string;
  slug: string;
  /** Each column is an independent list of values — index i of one column has no assumed relation to index i of another. */
  columns: Record<string, unknown[]>;
}

export declare class EptaAdminError extends Error {
  status: number;
  constructor(message: string, status: number);
}

export interface EptaAdminClientOptions {
  /** A personal API key generated from the EptaAdmin profile page. */
  apiKey: string;
  /** The URL of your EptaAdmin instance. Defaults to http://localhost:8080. */
  baseUrl?: string;
}

export declare class EptaAdminClient {
  constructor(options: EptaAdminClientOptions);
  listWorkspaces(): Promise<Workspace[]>;
  listDataSources(workspaceSlug: string): Promise<DataSourceSummary[]>;
  getDataSource(workspaceSlug: string, dataSourceSlug: string): Promise<DataSourceContent>;
  /**
   * One-parameter shortcut addressed by a slash-separated path:
   *   - "wsSlug/dsSlug/column"       -> that column's full value[] list.
   *   - "wsSlug/dsSlug/column/index" -> exactly one value at that index.
   */
  getValue(path: string): Promise<unknown>;
}
