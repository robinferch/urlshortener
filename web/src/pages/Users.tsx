import { useState, type FormEvent } from "react";
import { KeyRound, Plus, Search, ShieldCheck, ShieldOff, Trash2, UserCheck, UserX } from "lucide-react";
import { api, type User } from "../api";
import { useAuth } from "../auth";
import { Badge, Button, Card, ErrorBox, Field, Input, Modal, PageHeader, Spinner, Toggle, confirmAction, fmtDateTime, useApi, useToast } from "../ui";

function CreateUser({ onDone }: { onDone: () => void }) {
  const toast = useToast();
  const [f, setF] = useState({ email: "", name: "", password: "", isAdmin: false });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await api("POST", "/users", { ...f, password: f.password || undefined });
      toast("User created");
      onDone();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <form onSubmit={submit} className="space-y-4">
      <ErrorBox msg={error} />
      <Field label="Email">{(id) => <Input id={id} type="email" required value={f.email} onChange={(e) => setF({ ...f, email: e.target.value })} autoFocus />}</Field>
      <Field label="Name">{(id) => <Input id={id} value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} />}</Field>
      <Field label="Password" hint="Leave empty for SSO-only accounts.">
        {(id) => <Input id={id} type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} />}
      </Field>
      <Toggle checked={f.isAdmin} onChange={(v) => setF({ ...f, isAdmin: v })} label="Instance admin" />
      <div className="flex justify-end">
        <Button type="submit" loading={busy}>
          Create user
        </Button>
      </div>
    </form>
  );
}

export default function Users() {
  const { user: me } = useAuth();
  const toast = useToast();
  const [q, setQ] = useState("");
  const { data, error, loading, reload } = useApi<User[]>(`/users?q=${encodeURIComponent(q)}`);
  const [creating, setCreating] = useState(false);

  async function patch(u: User, body: Partial<User> & { password?: string }, msg: string) {
    try {
      await api("PATCH", `/users/${u.id}`, body);
      toast(msg);
      reload();
    } catch (e) {
      toast((e as Error).message, "err");
    }
  }

  async function resetPassword(u: User) {
    const pw = window.prompt(`New password for ${u.email} (min. 8 characters):`);
    if (pw) patch(u, { password: pw }, "Password updated. The user was signed out everywhere.");
  }

  async function remove(u: User) {
    if (!confirmAction(`Delete ${u.email}? Their links are kept but lose their owner.`)) return;
    try {
      await api("DELETE", `/users/${u.id}`);
      toast("User deleted");
      reload();
    } catch (e) {
      toast((e as Error).message, "err");
    }
  }

  return (
    <>
      <PageHeader
        title="Users"
        description="Instance admins manage everything. Domain roles are assigned on the Domains page."
        actions={
          <Button onClick={() => setCreating(true)}>
            <Plus className="size-4" /> New user
          </Button>
        }
      />
      <div className="relative mb-4">
        <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-slate-400" />
        <Input aria-label="Search users" placeholder="Search by email or name" value={q} onChange={(e) => setQ(e.target.value)} className="pl-9" />
      </div>
      <ErrorBox msg={error} />
      <Card className="overflow-x-auto">
        {loading && !data ? (
          <Spinner />
        ) : (
          <table className="w-full text-sm">
            <thead className="border-b border-slate-200 text-left text-xs uppercase tracking-wide text-slate-500 dark:border-slate-800">
              <tr>
                <th className="px-4 py-3 font-medium">User</th>
                <th className="px-4 py-3 font-medium">Sign-in</th>
                <th className="hidden px-4 py-3 font-medium md:table-cell">Last login</th>
                <th className="px-4 py-3" />
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {data?.map((u) => (
                <tr key={u.id} className={u.disabled ? "opacity-50" : ""}>
                  <td className="px-4 py-3">
                    <div className="flex items-center gap-2 font-medium">
                      {u.name || u.email}
                      {u.isAdmin && <Badge tone="cyan">Instance admin</Badge>}
                      {u.disabled && <Badge tone="red">Disabled</Badge>}
                    </div>
                    {u.name && <div className="text-xs text-slate-500">{u.email}</div>}
                  </td>
                  <td className="px-4 py-3">
                    <div className="flex gap-1">
                      {u.hasPassword && <Badge>Password</Badge>}
                      {u.sso && <Badge>SSO</Badge>}
                      {!u.hasPassword && !u.sso && <Badge tone="amber">Pending SSO</Badge>}
                    </div>
                  </td>
                  <td className="hidden px-4 py-3 text-slate-500 md:table-cell">{fmtDateTime(u.lastLoginAt)}</td>
                  <td className="px-4 py-3">
                    {u.id !== me!.id && (
                      <div className="flex justify-end gap-1">
                        <Button variant="ghost" size="sm" title={u.isAdmin ? "Revoke admin" : "Make admin"} aria-label={u.isAdmin ? "Revoke admin" : "Make admin"}
                          onClick={() => patch(u, { isAdmin: !u.isAdmin }, u.isAdmin ? "Admin rights revoked" : "User is now an instance admin")}>
                          {u.isAdmin ? <ShieldOff className="size-4" /> : <ShieldCheck className="size-4" />}
                        </Button>
                        <Button variant="ghost" size="sm" title="Set password" aria-label="Set password" onClick={() => resetPassword(u)}>
                          <KeyRound className="size-4" />
                        </Button>
                        <Button variant="ghost" size="sm" title={u.disabled ? "Enable" : "Disable"} aria-label={u.disabled ? "Enable" : "Disable"}
                          onClick={() => patch(u, { disabled: !u.disabled }, u.disabled ? "User enabled" : "User disabled and signed out")}>
                          {u.disabled ? <UserCheck className="size-4" /> : <UserX className="size-4" />}
                        </Button>
                        <Button variant="ghost" size="sm" title="Delete" aria-label="Delete" onClick={() => remove(u)}>
                          <Trash2 className="size-4 text-red-600" />
                        </Button>
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
      <Modal open={creating} onClose={() => setCreating(false)} title="New user">
        <CreateUser onDone={() => (setCreating(false), reload())} />
      </Modal>
    </>
  );
}
