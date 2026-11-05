import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { api, type AppConfig, type DomainRole, type User } from "./api";
import { Spinner } from "./ui";

interface AuthState {
  config: AppConfig;
  user: User | null;
  domains: DomainRole[];
  refresh: () => Promise<void>;
  logout: () => Promise<void>;
  /** True if the user may manage domain id (instance admin or domain admin). */
  canManage: (domainId: number) => boolean;
}

const Ctx = createContext<AuthState>(null!);
export const useAuth = () => useContext(Ctx);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [config, setConfig] = useState<AppConfig>();
  const [me, setMe] = useState<{ user: User; domains: DomainRole[] } | null>();

  const refresh = useCallback(async () => {
    const [c, m] = await Promise.all([
      api<AppConfig>("GET", "/config"),
      api<{ user: User; domains: DomainRole[] }>("GET", "/me").catch(() => null),
    ]);
    setConfig(c);
    setMe(m);
  }, []);

  useEffect(() => {
    refresh();
    const onUnauth = () => setMe(null);
    window.addEventListener("unauthorized", onUnauth);
    return () => window.removeEventListener("unauthorized", onUnauth);
  }, [refresh]);

  if (!config || me === undefined) return <Spinner />;

  const value: AuthState = {
    config,
    user: me?.user ?? null,
    domains: me?.domains ?? [],
    refresh,
    logout: async () => {
      await api("POST", "/auth/logout");
      setMe(null);
    },
    canManage: (id) => !!me && (me.user.isAdmin || me.domains.some((d) => d.id === id && d.role === "admin")),
  };
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
