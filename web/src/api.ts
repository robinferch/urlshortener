export type Role = "admin" | "user";

export interface User {
  id: number;
  email: string;
  name: string;
  isAdmin: boolean;
  disabled: boolean;
  hasPassword: boolean;
  sso: boolean;
  createdAt: string;
  lastLoginAt: string | null;
}

export interface DomainRole {
  id: number;
  host: string;
  scheme: string;
  role: Role;
}

export interface Domain extends DomainRole {
  fallbackUrl: string;
  createdAt: string;
  links: number;
}

export interface Member {
  userId: number;
  email: string;
  name: string;
  role: Role;
}

export interface Link {
  id: number;
  domainId: number;
  domain: string;
  slug: string;
  shortUrl: string;
  targetUrl: string;
  iosUrl: string;
  androidUrl: string;
  title: string;
  tags: string[];
  hasPassword: boolean;
  validSince: string | null;
  validUntil: string | null;
  maxVisits: number | null;
  forwardQuery: boolean;
  enabled: boolean;
  visits: number;
  limitUsed: number;
  createdBy: number | null;
  creatorEmail: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface Bucket {
  key: string;
  count: number;
}

export interface Stats {
  days: number;
  visits: number;
  bots: number;
  links: number;
  allTime: number;
  series: { date: string; count: number }[];
  hours: number[];
  referrers: Bucket[];
  countries: Bucket[];
  browsers: Bucket[];
  os: Bucket[];
  devices: Bucket[];
  topLinks?: { id: number; shortUrl: string; title: string; targetUrl: string; count: number }[];
}

export interface ApiKey {
  id: number;
  name: string;
  prefix: string;
  createdAt: string;
  lastUsedAt: string | null;
}

export interface ImportJob {
  id: number;
  source: string;
  status: "running" | "done" | "failed";
  total: number;
  imported: number;
  skipped: number;
  visits: number;
  unsupported: number;
  error: string;
  createdAt: string;
  finishedAt: string | null;
}

export interface AppConfig {
  version: string;
  localAuth: boolean;
  oidc: boolean;
  oidcName: string;
  setupRequired: boolean;
}

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

export async function api<T = void>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch("/api" + path, {
    method,
    credentials: "same-origin",
    // X-Requested-With is the server's CSRF guard for cookie-authenticated writes.
    headers: { "X-Requested-With": "fetch", ...(body !== undefined ? { "Content-Type": "application/json" } : {}) },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    let msg = res.statusText;
    try {
      msg = (await res.json()).error ?? msg;
    } catch {
      /* non-JSON error body */
    }
    if (res.status === 401 && !path.startsWith("/auth/")) window.dispatchEvent(new Event("unauthorized"));
    throw new ApiError(res.status, msg);
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}
