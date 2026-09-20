import type { Plugin } from "vite";

export interface EptaadminPrefetchSource {
  workspace: string;
  dataSource: string;
}

export interface EptaadminPrefetchOptions {
  /** The URL of your EptaAdmin instance. */
  baseUrl: string;
  /** Env var holding the API key used at build time. Defaults to "EPTAADMIN_API_KEY". */
  apiKeyEnv?: string;
  /** Directory (relative to Vite's root) scanned for getDataSource()/getValue() calls. Defaults to "src". */
  scanDir?: string;
  /**
   * Extra data sources to prefetch on top of whatever scanning finds —
   * for calls whose arguments aren't literal strings (e.g. a variable),
   * which static scanning can't see.
   */
  sources?: EptaadminPrefetchSource[];
}

/**
 * A Vite plugin that scans your source code for `getDataSource()` /
 * `getValue()` calls, fetches exactly those data sources once at
 * `vite build` time, and injects them so those same calls resolve from
 * that static data — with zero runtime request in the production bundle.
 * Has no effect during `vite` (dev server): the same client code keeps
 * making live requests there.
 */
export declare function eptaadminPrefetch(options: EptaadminPrefetchOptions): Plugin;
