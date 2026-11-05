import { useState, type FormEvent } from "react";
import { KeyRound } from "lucide-react";
import { api } from "../api";
import { useAuth } from "../auth";
import { Button, Card, ErrorBox, Field, Input } from "../ui";

export default function Login() {
  const { config, refresh } = useAuth();
  const setup = config.setupRequired;
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState(new URLSearchParams(location.search).get("error") ?? "");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api("POST", setup ? "/setup" : "/auth/login", { email, password, name });
      history.replaceState(null, "", "/");
      await refresh();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-gradient-to-br from-cyan-50 via-slate-50 to-slate-100 px-4 dark:from-slate-950 dark:via-slate-950 dark:to-cyan-950/30">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center text-center">
          <img src="/icon.svg" alt="" className="mb-4 size-12 drop-shadow" />
          <h1 className="text-2xl font-semibold tracking-tight">{setup ? "Welcome! Create the admin account" : "Sign in"}</h1>
          <p className="mt-1 text-sm text-slate-500">
            {setup ? "This account becomes the instance administrator." : "Short links, analytics and domains in one place."}
          </p>
        </div>
        <Card className="p-6">
          <div className="space-y-4">
            <ErrorBox msg={error} />
            {config.localAuth && (
              <form onSubmit={submit} className="space-y-4">
                {setup && (
                  <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} autoComplete="name" />}</Field>
                )}
                <Field label="Email">
                  {(id) => <Input id={id} type="email" required value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="email" autoFocus />}
                </Field>
                <Field label="Password" hint={setup ? "At least 8 characters." : undefined}>
                  {(id) => (
                    <Input
                      id={id}
                      type="password"
                      required
                      minLength={setup ? 8 : undefined}
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                      autoComplete={setup ? "new-password" : "current-password"}
                    />
                  )}
                </Field>
                <Button type="submit" className="w-full" loading={busy}>
                  {setup ? "Create admin account" : "Sign in"}
                </Button>
              </form>
            )}
            {config.oidc && !setup && (
              <>
                {config.localAuth && (
                  <div className="flex items-center gap-3 text-xs uppercase text-slate-400">
                    <div className="h-px flex-1 bg-slate-200 dark:bg-slate-800" />
                    or
                    <div className="h-px flex-1 bg-slate-200 dark:bg-slate-800" />
                  </div>
                )}
                <a
                  href="/api/auth/oidc/login"
                  className="flex h-10 w-full items-center justify-center gap-2 rounded-lg border border-slate-300 bg-white text-sm font-medium text-slate-700 shadow-sm hover:bg-slate-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-cyan-600 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200 dark:hover:bg-slate-800"
                >
                  <KeyRound className="size-4" />
                  Continue with {config.oidcName}
                </a>
              </>
            )}
          </div>
        </Card>
      </div>
    </div>
  );
}
