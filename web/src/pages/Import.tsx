import { useEffect, useState, type FormEvent } from "react";
import { CheckCircle2, Loader2, XCircle } from "lucide-react";
import { api, type ImportJob } from "../api";
import { useAuth } from "../auth";
import { Button, Card, ErrorBox, Field, Input, PageHeader, Select, Toggle, fmtDateTime, fmtNum, useApi, useToast } from "../ui";

export default function Import() {
  const { domains } = useAuth();
  const toast = useToast();
  const manageable = domains; // instance admins see every domain
  const [url, setUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [domainId, setDomainId] = useState(manageable[0]?.id ?? 0);
  const [importVisits, setImportVisits] = useState(true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const jobs = useApi<ImportJob[]>("/import/jobs");
  const running = jobs.data?.some((j) => j.status === "running");

  // Poll while an import is running.
  useEffect(() => {
    if (!running) return;
    const t = setInterval(jobs.reload, 1500);
    return () => clearInterval(t);
  }, [running, jobs.reload]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api("POST", "/import/shlink", { url, apiKey, domainId, importVisits });
      toast("Import started");
      setApiKey("");
      jobs.reload();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <PageHeader title="Import" description="Bring your short links and their visit history over from Shlink." />
      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <Card className="p-6">
          <h2 className="mb-1 font-semibold">Import from Shlink</h2>
          <p className="mb-5 text-sm text-slate-500">
            Create an API key in Shlink with <code className="rounded bg-slate-100 px-1 dark:bg-slate-800">shlink api-key:generate</code>. Links on a Shlink
            domain that also exists here keep that domain; all others go to the target domain. Slugs that already exist are skipped, so re-running is safe.
          </p>
          <form onSubmit={submit} className="space-y-4">
            <ErrorBox msg={error} />
            <Field label="Shlink server URL">
              {(id) => <Input id={id} type="url" required placeholder="https://s.example.com" value={url} onChange={(e) => setUrl(e.target.value)} />}
            </Field>
            <Field label="API key">
              {(id) => <Input id={id} type="password" required autoComplete="off" value={apiKey} onChange={(e) => setApiKey(e.target.value)} />}
            </Field>
            <Field label="Target domain">
              {(id) => (
                <Select id={id} value={domainId} onChange={(e) => setDomainId(Number(e.target.value))}>
                  {manageable.map((d) => (
                    <option key={d.id} value={d.id}>
                      {d.host}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            <Toggle checked={importVisits} onChange={setImportVisits} label="Import visit history (slower for large instances)" />
            <div className="flex justify-end">
              <Button type="submit" loading={busy} disabled={!domainId}>
                Start import
              </Button>
            </div>
          </form>
        </Card>
        <Card className="p-6">
          <h2 className="mb-4 font-semibold">Recent imports</h2>
          {jobs.data?.length === 0 && <p className="text-sm text-slate-500">No imports yet.</p>}
          <ul className="space-y-3">
            {jobs.data?.map((j) => (
              <li key={j.id} className="rounded-lg border border-slate-200 p-3 text-sm dark:border-slate-800">
                <div className="flex items-center gap-2 font-medium">
                  {j.status === "running" && <Loader2 className="size-4 animate-spin text-cyan-600" />}
                  {j.status === "done" && <CheckCircle2 className="size-4 text-emerald-600" />}
                  {j.status === "failed" && <XCircle className="size-4 text-red-600" />}
                  <span className="truncate">{j.source}</span>
                </div>
                <div className="mt-1 text-slate-500">
                  {fmtNum(j.imported)} imported · {fmtNum(j.skipped)} skipped · {fmtNum(j.visits)} visits
                  {j.total > 0 && ` · ${Math.round(((j.imported + j.skipped) / j.total) * 100)}% of ${fmtNum(j.total)}`}
                </div>
                {j.status === "running" && j.total > 0 && (
                  <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
                    <div className="h-full bg-cyan-600 transition-all" style={{ width: `${((j.imported + j.skipped) / j.total) * 100}%` }} />
                  </div>
                )}
                {j.unsupported > 0 && (
                  <div className="mt-1 text-amber-700 dark:text-amber-400">
                    {fmtNum(j.unsupported)} link{j.unsupported === 1 ? " has" : "s have"} Shlink redirect rules other than iOS/Android targeting. Those rules were not imported; visitors get the default destination.
                  </div>
                )}
                {j.error && <div className="mt-1 text-red-600">{j.error}</div>}
                <div className="mt-1 text-xs text-slate-400">{fmtDateTime(j.createdAt)}</div>
              </li>
            ))}
          </ul>
        </Card>
      </div>
    </>
  );
}
