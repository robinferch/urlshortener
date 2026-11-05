import { useState, type FormEvent } from "react";
import { KeyRound, Trash2 } from "lucide-react";
import { api, type ApiKey } from "../api";
import { useAuth } from "../auth";
import { Button, Card, CopyButton, ErrorBox, Field, Input, PageHeader, confirmAction, fmtDateTime, useApi, useToast } from "../ui";

function Profile() {
  const { user, refresh, config } = useAuth();
  const toast = useToast();
  const [name, setName] = useState(user!.name);
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api("PATCH", "/me", { name, currentPassword: current, newPassword: next });
      toast(next ? "Profile and password updated" : "Profile updated");
      setCurrent("");
      setNext("");
      refresh();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card className="p-6">
      <h2 className="mb-4 font-semibold">Profile</h2>
      <form onSubmit={submit} className="space-y-4">
        <ErrorBox msg={error} />
        <Field label="Email">{(id) => <Input id={id} value={user!.email} disabled />}</Field>
        <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} autoComplete="name" />}</Field>
        {config.localAuth && !(user!.sso && !user!.hasPassword) && (
          <>
            {user!.hasPassword && (
              <Field label="Current password">
                {(id) => <Input id={id} type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />}
              </Field>
            )}
            <Field label={user!.hasPassword ? "New password" : "Set a password"} hint="Changing it signs out your other sessions.">
              {(id) => <Input id={id} type="password" autoComplete="new-password" minLength={8} value={next} onChange={(e) => setNext(e.target.value)} />}
            </Field>
          </>
        )}
        <div className="flex justify-end">
          <Button type="submit" loading={busy}>
            Save
          </Button>
        </div>
      </form>
    </Card>
  );
}

function ApiKeys() {
  const toast = useToast();
  const { data, reload } = useApi<ApiKey[]>("/me/api-keys");
  const [name, setName] = useState("");
  const [created, setCreated] = useState("");

  async function create(e: FormEvent) {
    e.preventDefault();
    try {
      const r = await api<{ key: string }>("POST", "/me/api-keys", { name });
      setCreated(r.key);
      setName("");
      reload();
    } catch (err) {
      toast((err as Error).message, "err");
    }
  }

  async function remove(k: ApiKey) {
    if (!confirmAction(`Revoke API key "${k.name}"? Apps using it stop working immediately.`)) return;
    await api("DELETE", `/me/api-keys/${k.id}`);
    reload();
  }

  return (
    <Card className="p-6">
      <h2 className="mb-1 font-semibold">API keys</h2>
      <p className="mb-4 text-sm text-slate-500">
        Use the REST API with <code className="rounded bg-slate-100 px-1 dark:bg-slate-800">Authorization: Bearer &lt;key&gt;</code>. Keys have your permissions.
      </p>
      {created && (
        <div className="mb-4 rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm dark:border-amber-800 dark:bg-amber-950/40">
          <div className="mb-1 font-medium">Copy your new key now. It won't be shown again.</div>
          <div className="flex items-center gap-2">
            <code className="min-w-0 flex-1 truncate">{created}</code>
            <CopyButton text={created} />
          </div>
        </div>
      )}
      <form onSubmit={create} className="mb-4 flex gap-2">
        <Input required aria-label="Key name" placeholder="Key name, e.g. CI pipeline" value={name} onChange={(e) => setName(e.target.value)} />
        <Button type="submit">
          <KeyRound className="size-4" /> Create
        </Button>
      </form>
      <ul className="divide-y divide-slate-100 dark:divide-slate-800">
        {data?.map((k) => (
          <li key={k.id} className="flex items-center gap-3 py-2.5 text-sm">
            <div className="min-w-0 flex-1">
              <div className="font-medium">{k.name}</div>
              <div className="text-xs text-slate-500">
                <code>{k.prefix}…</code> · created {fmtDateTime(k.createdAt)} · last used {fmtDateTime(k.lastUsedAt)}
              </div>
            </div>
            <Button variant="ghost" size="sm" onClick={() => remove(k)} aria-label={`Revoke ${k.name}`}>
              <Trash2 className="size-4 text-red-600" />
            </Button>
          </li>
        ))}
      </ul>
    </Card>
  );
}

export default function Settings() {
  return (
    <>
      <PageHeader title="Settings" />
      <div className="grid gap-6 lg:grid-cols-2">
        <Profile />
        <ApiKeys />
      </div>
    </>
  );
}
