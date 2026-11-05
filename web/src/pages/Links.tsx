import { useEffect, useState, type FormEvent } from "react";
import { Link as RouterLink, useSearchParams } from "react-router-dom";
import { BarChart3, ChevronLeft, ChevronRight, Globe, Link2, Lock, Pencil, Plus, QrCode, Search, Timer, Trash2, Zap } from "lucide-react";
import { api, type Link } from "../api";
import { useAuth } from "../auth";
import { Badge, Button, Card, CopyButton, Empty, ErrorBox, Input, PageHeader, Select, Spinner, confirmAction, fmtDate, fmtNum, useApi, useToast } from "../ui";
import { LinkFormModal, QRModal } from "./LinkForm";

type Page = { items: Link[]; total: number; page: number; perPage: number };

export function linkStatus(l: Link): { label: string; tone: "red" | "amber" } | null {
  const now = Date.now();
  if (!l.enabled) return { label: "Disabled", tone: "red" };
  if (l.validUntil && new Date(l.validUntil).getTime() < now) return { label: "Expired", tone: "red" };
  if (l.maxVisits && l.limitUsed >= l.maxVisits) return { label: "Limit reached", tone: "red" };
  if (l.validSince && new Date(l.validSince).getTime() > now) return { label: "Scheduled", tone: "amber" };
  return null;
}

function QuickCreate({ onCreated }: { onCreated: () => void }) {
  const { domains } = useAuth();
  const toast = useToast();
  const [url, setUrl] = useState("");
  const [domainId, setDomainId] = useState(domains[0]?.id ?? 0);
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<Link>();
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const l = await api<Link>("POST", "/links", { domainId, targetUrl: url });
      setCreated(l);
      setUrl("");
      await navigator.clipboard?.writeText(l.shortUrl).then(() => toast("Short link copied to clipboard")).catch(() => {});
      onCreated();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card className="mb-6 p-4">
      <form onSubmit={submit} className="flex flex-col gap-3 sm:flex-row">
        <div className="relative flex-1">
          <Zap className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-cyan-600" />
          <Input type="url" required aria-label="URL to shorten" placeholder="Paste a long URL to shorten it instantly" value={url} onChange={(e) => setUrl(e.target.value)} className="pl-9" />
        </div>
        {domains.length > 1 && (
          <Select aria-label="Domain" value={domainId} onChange={(e) => setDomainId(Number(e.target.value))} className="sm:w-48">
            {domains.map((d) => (
              <option key={d.id} value={d.id}>
                {d.host}
              </option>
            ))}
          </Select>
        )}
        <Button type="submit" loading={busy}>
          Shorten
        </Button>
      </form>
      {error && <div className="mt-3"><ErrorBox msg={error} /></div>}
      {created && (
        <div className="mt-3 flex items-center gap-2 rounded-lg bg-cyan-50 px-3 py-2 text-sm dark:bg-cyan-950/50">
          <span className="text-slate-600 dark:text-slate-300">Created:</span>
          <a href={created.shortUrl} target="_blank" rel="noreferrer" className="font-medium text-cyan-700 hover:underline dark:text-cyan-300">
            {created.shortUrl}
          </a>
          <CopyButton text={created.shortUrl} />
        </div>
      )}
    </Card>
  );
}

export default function Links() {
  const { domains } = useAuth();
  const toast = useToast();
  const [params, setParams] = useSearchParams();
  const [q, setQ] = useState(params.get("q") ?? "");
  const page = Number(params.get("page") ?? 1);
  const domainId = params.get("domainId") ?? "";
  const tag = params.get("tag") ?? "";
  const sort = params.get("sort") ?? "created";
  const qs = new URLSearchParams({ page: String(page), perPage: "25", q: params.get("q") ?? "", domainId, tag, sort });
  const { data, error, loading, reload } = useApi<Page>(`/links?${qs}`);
  const [editing, setEditing] = useState<Link | null>(null);
  const [creating, setCreating] = useState(false);
  const [qr, setQr] = useState<Link | null>(null);

  const update = (k: string, v: string) => {
    const p = new URLSearchParams(params);
    if (v) p.set(k, v);
    else p.delete(k);
    if (k !== "page") p.delete("page");
    setParams(p, { replace: true });
  };

  // Debounce search input into the URL.
  useEffect(() => {
    const t = setTimeout(() => q !== (params.get("q") ?? "") && update("q", q), 300);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q]);

  async function remove(l: Link) {
    if (!confirmAction(`Delete ${l.shortUrl}? Its analytics will be deleted too.`)) return;
    try {
      await api("DELETE", `/links/${l.id}`);
      toast("Link deleted");
      reload();
    } catch (e) {
      toast((e as Error).message, "err");
    }
  }

  if (domains.length === 0) {
    return (
      <>
        <PageHeader title="Links" />
        <Card>
          <Empty icon={<Globe className="size-5" />} title="No domains available">
            You need to be a member of a domain to create links.
          </Empty>
        </Card>
      </>
    );
  }

  const pages = data ? Math.max(1, Math.ceil(data.total / data.perPage)) : 1;

  return (
    <>
      <PageHeader
        title="Links"
        description={data ? `${fmtNum(data.total)} link${data.total === 1 ? "" : "s"}` : undefined}
        actions={
          <Button onClick={() => setCreating(true)}>
            <Plus className="size-4" /> New link
          </Button>
        }
      />
      <QuickCreate onCreated={reload} />

      <div className="mb-4 flex flex-col gap-3 sm:flex-row">
        <div className="relative flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-slate-400" />
          <Input aria-label="Search links" placeholder="Search slug, destination or title" value={q} onChange={(e) => setQ(e.target.value)} className="pl-9" />
        </div>
        {domains.length > 1 && (
          <Select aria-label="Filter by domain" value={domainId} onChange={(e) => update("domainId", e.target.value)} className="sm:w-48">
            <option value="">All domains</option>
            {domains.map((d) => (
              <option key={d.id} value={d.id}>
                {d.host}
              </option>
            ))}
          </Select>
        )}
        <Select aria-label="Sort" value={sort} onChange={(e) => update("sort", e.target.value)} className="sm:w-40">
          <option value="created">Newest first</option>
          <option value="visits">Most visited</option>
          <option value="slug">Slug A–Z</option>
        </Select>
      </div>
      {tag && (
        <div className="mb-4 flex items-center gap-2 text-sm">
          Filtered by tag <Badge tone="cyan">#{tag}</Badge>
          <button className="text-cyan-700 underline dark:text-cyan-400" onClick={() => update("tag", "")}>
            clear
          </button>
        </div>
      )}

      <ErrorBox msg={error} />
      <Card>
        {loading && !data ? (
          <Spinner />
        ) : data?.items.length === 0 ? (
          <Empty icon={<Link2 className="size-5" />} title={params.get("q") || tag ? "No matching links" : "No links yet"}>
            {params.get("q") || tag ? "Try a different search." : "Paste a URL above to create your first short link."}
          </Empty>
        ) : (
          <ul className="divide-y divide-slate-100 dark:divide-slate-800">
            {data?.items.map((l) => {
              const status = linkStatus(l);
              return (
                <li key={l.id} className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <a href={l.shortUrl} target="_blank" rel="noreferrer" className="truncate font-medium text-cyan-700 hover:underline dark:text-cyan-400">
                        {l.domain}/{l.slug}
                      </a>
                      <CopyButton text={l.shortUrl} label="Copy short link" />
                      {l.hasPassword && (
                        <Badge>
                          <Lock className="size-3" /> Protected
                        </Badge>
                      )}
                      {(l.validUntil || l.maxVisits) && !status && (
                        <Badge tone="amber">
                          <Timer className="size-3" /> Limited
                        </Badge>
                      )}
                      {status && <Badge tone={status.tone}>{status.label}</Badge>}
                    </div>
                    {l.title && <div className="truncate text-sm font-medium">{l.title}</div>}
                    <div className="truncate text-sm text-slate-500" title={l.targetUrl}>
                      {l.targetUrl}
                    </div>
                    <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-xs text-slate-400">
                      {fmtDate(l.createdAt)}
                      {l.creatorEmail && <span>· {l.creatorEmail}</span>}
                      {l.tags.map((t) => (
                        <button key={t} onClick={() => update("tag", t)}>
                          <Badge tone="cyan">#{t}</Badge>
                        </button>
                      ))}
                    </div>
                  </div>
                  <div className="flex items-center gap-1 sm:pl-4">
                    <RouterLink to={`/links/${l.id}`} className="mr-2 flex items-center gap-1.5 rounded-lg px-2 py-1 text-sm tabular-nums text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800" title="Analytics">
                      <BarChart3 className="size-4" />
                      {fmtNum(l.visits)}
                    </RouterLink>
                    <Button variant="ghost" size="sm" onClick={() => setQr(l)} aria-label="QR code" title="QR code">
                      <QrCode className="size-4" />
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => setEditing(l)} aria-label="Edit" title="Edit">
                      <Pencil className="size-4" />
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => remove(l)} aria-label="Delete" title="Delete">
                      <Trash2 className="size-4 text-red-600" />
                    </Button>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </Card>

      {pages > 1 && (
        <div className="mt-4 flex items-center justify-between text-sm">
          <span className="text-slate-500">
            Page {page} of {pages}
          </span>
          <div className="flex gap-2">
            <Button variant="secondary" size="sm" disabled={page <= 1} onClick={() => update("page", String(page - 1))}>
              <ChevronLeft className="size-4" /> Prev
            </Button>
            <Button variant="secondary" size="sm" disabled={page >= pages} onClick={() => update("page", String(page + 1))}>
              Next <ChevronRight className="size-4" />
            </Button>
          </div>
        </div>
      )}

      <LinkFormModal open={creating} onClose={() => setCreating(false)} onSaved={() => (setCreating(false), reload())} />
      <LinkFormModal open={!!editing} link={editing ?? undefined} onClose={() => setEditing(null)} onSaved={() => (setEditing(null), reload())} />
      <QRModal link={qr} onClose={() => setQr(null)} />
    </>
  );
}
