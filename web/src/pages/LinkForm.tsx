import { useState, type FormEvent } from "react";
import { ChevronDown } from "lucide-react";
import { api, type Link } from "../api";
import { useAuth } from "../auth";
import { Button, ErrorBox, Field, Input, Modal, Select, Toggle, cx, useToast } from "../ui";

const toLocal = (iso: string | null) => {
  if (!iso) return "";
  const d = new Date(iso);
  return new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
};
const fromLocal = (v: string) => (v ? new Date(v).toISOString() : null);

export function LinkFormModal({ link, open, onClose, onSaved }: { link?: Link; open: boolean; onClose: () => void; onSaved: (l: Link) => void }) {
  return (
    <Modal open={open} onClose={onClose} title={link ? "Edit link" : "New link"} wide>
      <LinkForm key={link?.id ?? "new"} link={link} onCancel={onClose} onSaved={onSaved} />
    </Modal>
  );
}

function LinkForm({ link, onCancel, onSaved }: { link?: Link; onCancel: () => void; onSaved: (l: Link) => void }) {
  const { domains } = useAuth();
  const toast = useToast();
  const [f, setF] = useState({
    domainId: link?.domainId ?? domains[0]?.id ?? 0,
    targetUrl: link?.targetUrl ?? "",
    slug: link?.slug ?? "",
    title: link?.title ?? "",
    tags: link?.tags.join(", ") ?? "",
    iosUrl: link?.iosUrl ?? "",
    androidUrl: link?.androidUrl ?? "",
    validSince: toLocal(link?.validSince ?? null),
    validUntil: toLocal(link?.validUntil ?? null),
    maxVisits: link?.maxVisits?.toString() ?? "",
    forwardQuery: link?.forwardQuery ?? true,
    enabled: link?.enabled ?? true,
    password: "",
    removePassword: false,
  });
  const hasAdvanced = !!link && (!!link.iosUrl || !!link.androidUrl || !!link.validSince || !!link.validUntil || !!link.maxVisits || link.hasPassword);
  const [advanced, setAdvanced] = useState(hasAdvanced);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const set = <K extends keyof typeof f>(k: K, v: (typeof f)[K]) => setF((p) => ({ ...p, [k]: v }));
  const host = domains.find((d) => d.id === f.domainId);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const body = {
      domainId: f.domainId,
      targetUrl: f.targetUrl,
      slug: f.slug,
      title: f.title,
      tags: f.tags.split(",").map((t) => t.trim()).filter(Boolean),
      iosUrl: f.iosUrl,
      androidUrl: f.androidUrl,
      validSince: fromLocal(f.validSince),
      validUntil: fromLocal(f.validUntil),
      maxVisits: f.maxVisits ? Number(f.maxVisits) : null,
      forwardQuery: f.forwardQuery,
      enabled: f.enabled,
      // undefined keeps the current password, "" removes it.
      password: f.removePassword ? "" : f.password || undefined,
    };
    try {
      const saved = await api<Link>(link ? "PUT" : "POST", link ? `/links/${link.id}` : "/links", body);
      toast(link ? "Link updated" : "Link created");
      onSaved(saved);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <ErrorBox msg={error} />
      <Field label="Destination URL">
        {(id) => <Input id={id} type="url" required placeholder="https://example.com/a/very/long/path" value={f.targetUrl} onChange={(e) => set("targetUrl", e.target.value)} autoFocus />}
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Domain">
          {(id) => (
            <Select id={id} value={f.domainId} onChange={(e) => set("domainId", Number(e.target.value))}>
              {domains.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.host}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="Custom slug" hint={host ? `${host.host}/${f.slug || "…"}` : undefined}>
          {(id) => <Input id={id} placeholder="Leave empty for a random one" value={f.slug} onChange={(e) => set("slug", e.target.value)} />}
        </Field>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Title">{(id) => <Input id={id} value={f.title} onChange={(e) => set("title", e.target.value)} placeholder="Optional" />}</Field>
        <Field label="Tags" hint="Comma separated">
          {(id) => <Input id={id} value={f.tags} onChange={(e) => set("tags", e.target.value)} placeholder="marketing, q4" />}
        </Field>
      </div>
      <div className="flex flex-wrap gap-6">
        <Toggle checked={f.enabled} onChange={(v) => set("enabled", v)} label="Enabled" />
        <Toggle checked={f.forwardQuery} onChange={(v) => set("forwardQuery", v)} label="Forward query parameters" />
      </div>

      <button type="button" onClick={() => setAdvanced(!advanced)} className="flex items-center gap-1 text-sm font-medium text-cyan-700 dark:text-cyan-400">
        <ChevronDown className={cx("size-4 transition-transform", advanced && "rotate-180")} />
        Advanced: expiry, password, device targeting
      </button>
      {advanced && (
        <div className="space-y-4 rounded-xl border border-slate-200 bg-slate-50 p-4 dark:border-slate-800 dark:bg-slate-950/50">
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label="Active from">{(id) => <Input id={id} type="datetime-local" value={f.validSince} onChange={(e) => set("validSince", e.target.value)} />}</Field>
            <Field label="Expires at">{(id) => <Input id={id} type="datetime-local" value={f.validUntil} onChange={(e) => set("validUntil", e.target.value)} />}</Field>
            <Field label="Max visits" hint="Link-preview bots (Slack, WhatsApp…) count too">{(id) => <Input id={id} type="number" min={1} value={f.maxVisits} onChange={(e) => set("maxVisits", e.target.value)} />}</Field>
          </div>
          <Field label={link?.hasPassword ? "New password" : "Password protection"} hint={link?.hasPassword ? "This link is protected. Leave empty to keep the current password." : "Visitors must enter this password."}>
            {(id) => <Input id={id} type="password" autoComplete="new-password" value={f.password} disabled={f.removePassword} onChange={(e) => set("password", e.target.value)} />}
          </Field>
          {link?.hasPassword && <Toggle checked={f.removePassword} onChange={(v) => set("removePassword", v)} label="Remove password" />}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="iOS destination" hint="Used for iPhone/iPad visitors">
              {(id) => <Input id={id} type="url" value={f.iosUrl} onChange={(e) => set("iosUrl", e.target.value)} placeholder="https://apps.apple.com/…" />}
            </Field>
            <Field label="Android destination" hint="Used for Android visitors">
              {(id) => <Input id={id} type="url" value={f.androidUrl} onChange={(e) => set("androidUrl", e.target.value)} placeholder="https://play.google.com/…" />}
            </Field>
          </div>
        </div>
      )}
      <div className="flex justify-end gap-2 pt-2">
        <Button type="button" variant="secondary" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" loading={busy}>
          {link ? "Save changes" : "Create link"}
        </Button>
      </div>
    </form>
  );
}

export function QRModal({ link, onClose }: { link: Link | null; onClose: () => void }) {
  return (
    <Modal open={!!link} onClose={onClose} title="QR code">
      {link && (
        <div className="flex flex-col items-center gap-4">
          <img src={`/api/links/${link.id}/qr?size=512`} alt={`QR code for ${link.shortUrl}`} className="size-64 rounded-lg border border-slate-200 bg-white p-2 dark:border-slate-700" />
          <code className="text-sm">{link.shortUrl}</code>
          <a href={`/api/links/${link.id}/qr?size=1024&download`} className="inline-flex h-10 items-center rounded-lg bg-cyan-600 px-4 text-sm font-medium text-white hover:bg-cyan-700">
            Download PNG
          </a>
        </div>
      )}
    </Modal>
  );
}
