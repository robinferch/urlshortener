import { useState, type FormEvent } from "react";
import { Globe, Pencil, Plus, Trash2, UserPlus, Users, X } from "lucide-react";
import { api, type Domain, type Member } from "../api";
import { useAuth } from "../auth";
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, PageHeader, Select, Spinner, confirmAction, fmtNum, useApi, useToast } from "../ui";

function DomainForm({ domain, onDone }: { domain?: Domain; onDone: () => void }) {
  const toast = useToast();
  const [host, setHost] = useState(domain?.host ?? "");
  const [scheme, setScheme] = useState(domain?.scheme ?? "https");
  const [fallbackUrl, setFallback] = useState(domain?.fallbackUrl ?? "");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await api(domain ? "PATCH" : "POST", domain ? `/domains/${domain.id}` : "/domains", { host, scheme, fallbackUrl });
      toast(domain ? "Domain updated" : "Domain added");
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
      <Field label="Hostname" hint={domain ? "The hostname can't be changed." : "Point this host's DNS at the server, e.g. go.example.com"}>
        {(id) => <Input id={id} required disabled={!!domain} value={host} onChange={(e) => setHost(e.target.value)} placeholder="go.example.com" autoFocus={!domain} />}
      </Field>
      <Field label="Scheme" hint="Used when building short URLs.">
        {(id) => (
          <Select id={id} value={scheme} onChange={(e) => setScheme(e.target.value)}>
            <option value="https">https</option>
            <option value="http">http</option>
          </Select>
        )}
      </Field>
      <Field label="Fallback URL" hint="Where the domain root and unknown slugs redirect. Empty shows a 404 page.">
        {(id) => <Input id={id} type="url" value={fallbackUrl} onChange={(e) => setFallback(e.target.value)} placeholder="https://example.com" />}
      </Field>
      <div className="flex justify-end">
        <Button type="submit" loading={busy}>
          {domain ? "Save" : "Add domain"}
        </Button>
      </div>
    </form>
  );
}

function MembersPanel({ domain }: { domain: Domain }) {
  const toast = useToast();
  const { user } = useAuth();
  const { data, error, reload } = useApi<Member[]>(`/domains/${domain.id}/members`);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState("user");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  async function add(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      const r = await api<{ created: boolean }>("PUT", `/domains/${domain.id}/members`, { email, role, password: password || undefined });
      toast(r.created ? `Account created for ${email}` : "Member saved");
      setEmail("");
      setPassword("");
      reload();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function change(m: Member, newRole: string) {
    await api("PUT", `/domains/${domain.id}/members`, { email: m.email, role: newRole }).catch((e) => toast(e.message, "err"));
    reload();
  }

  async function remove(m: Member) {
    if (!confirmAction(`Remove ${m.email} from ${domain.host}?`)) return;
    await api("DELETE", `/domains/${domain.id}/members/${m.userId}`).catch((e) => toast(e.message, "err"));
    reload();
  }

  return (
    <div className="space-y-5">
      <ErrorBox msg={error} />
      {!data ? (
        <Spinner />
      ) : data.length === 0 ? (
        <p className="text-sm text-slate-500">No members yet. Instance admins always have access.</p>
      ) : (
        <ul className="divide-y divide-slate-100 rounded-lg border border-slate-200 dark:divide-slate-800 dark:border-slate-800">
          {data.map((m) => (
            <li key={m.userId} className="flex items-center gap-3 px-3 py-2">
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">{m.name || m.email}</div>
                {m.name && <div className="truncate text-xs text-slate-500">{m.email}</div>}
              </div>
              <Select aria-label={`Role of ${m.email}`} value={m.role} onChange={(e) => change(m, e.target.value)} className="w-36 py-1">
                <option value="user">Domain user</option>
                <option value="admin">Domain admin</option>
              </Select>
              <Button variant="ghost" size="sm" onClick={() => remove(m)} aria-label={`Remove ${m.email}`}>
                <X className="size-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
      <form onSubmit={add} className="space-y-3 rounded-lg bg-slate-50 p-4 dark:bg-slate-950/50">
        <h3 className="flex items-center gap-2 text-sm font-semibold">
          <UserPlus className="size-4" /> Add member
        </h3>
        <ErrorBox msg={err} />
        <div className="grid gap-3 sm:grid-cols-[1fr_auto]">
          <Input type="email" required aria-label="Email" placeholder="person@example.com" value={email} onChange={(e) => setEmail(e.target.value)} />
          <Select aria-label="Role" value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="user">Domain user</option>
            <option value="admin">Domain admin</option>
          </Select>
        </div>
        {user!.isAdmin && (
          <Input type="password" aria-label="Initial password" autoComplete="new-password" placeholder="Initial password (only for new local accounts)" value={password} onChange={(e) => setPassword(e.target.value)} />
        )}
        <p className="text-xs text-slate-500">
          Unknown emails get a new account that signs in via SSO with the same verified email.
          {user!.isAdmin ? " Set a password to create a local account instead." : " Ask an instance admin for local (password) accounts."}
        </p>
        <div className="flex justify-end">
          <Button type="submit" size="sm" loading={busy}>
            Add
          </Button>
        </div>
      </form>
    </div>
  );
}

export default function Domains() {
  const { user, refresh } = useAuth();
  const toast = useToast();
  const { data, error, loading, reload } = useApi<Domain[]>("/domains");
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<Domain | null>(null);
  const [members, setMembers] = useState<Domain | null>(null);
  const done = () => {
    setAdding(false);
    setEditing(null);
    reload();
    refresh();
  };

  async function remove(d: Domain) {
    if (!confirmAction(`Delete ${d.host}? All ${d.links} links and their analytics will be permanently deleted.`)) return;
    try {
      await api("DELETE", `/domains/${d.id}`);
      toast("Domain deleted");
      done();
    } catch (e) {
      toast((e as Error).message, "err");
    }
  }

  return (
    <>
      <PageHeader
        title="Domains"
        description="Each domain serves its own set of short links."
        actions={
          user!.isAdmin && (
            <Button onClick={() => setAdding(true)}>
              <Plus className="size-4" /> Add domain
            </Button>
          )
        }
      />
      <ErrorBox msg={error} />
      {loading && !data ? (
        <Spinner />
      ) : data?.length === 0 ? (
        <Card>
          <Empty icon={<Globe className="size-5" />} title="No domains">
            {user!.isAdmin ? "Add a domain to start creating short links." : "You are not a member of any domain yet."}
          </Empty>
        </Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {data?.map((d) => (
            <Card key={d.id} className="flex flex-col p-5">
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <Globe className="size-4 shrink-0 text-cyan-600" />
                    <span className="truncate font-semibold">{d.host}</span>
                  </div>
                  <div className="mt-1 text-sm text-slate-500">{fmtNum(d.links)} links</div>
                </div>
                <Badge tone={d.role === "admin" ? "cyan" : "slate"}>{d.role === "admin" ? "Admin" : "User"}</Badge>
              </div>
              <div className="mt-3 truncate text-xs text-slate-500">Fallback: {d.fallbackUrl || "404 page"}</div>
              {d.role === "admin" && (
                <div className="mt-4 flex gap-1 border-t border-slate-100 pt-3 dark:border-slate-800">
                  <Button variant="ghost" size="sm" onClick={() => setMembers(d)}>
                    <Users className="size-4" /> Members
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => setEditing(d)}>
                    <Pencil className="size-4" /> Edit
                  </Button>
                  {user!.isAdmin && (
                    <Button variant="ghost" size="sm" className="ml-auto" onClick={() => remove(d)} aria-label={`Delete ${d.host}`}>
                      <Trash2 className="size-4 text-red-600" />
                    </Button>
                  )}
                </div>
              )}
            </Card>
          ))}
        </div>
      )}
      <Modal open={adding} onClose={() => setAdding(false)} title="Add domain">
        <DomainForm onDone={done} />
      </Modal>
      <Modal open={!!editing} onClose={() => setEditing(null)} title={`Edit ${editing?.host}`}>
        {editing && <DomainForm key={editing.id} domain={editing} onDone={done} />}
      </Modal>
      <Modal open={!!members} onClose={() => setMembers(null)} title={`Members of ${members?.host}`} wide>
        {members && <MembersPanel key={members.id} domain={members} />}
      </Modal>
    </>
  );
}
