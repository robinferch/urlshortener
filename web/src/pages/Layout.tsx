import { Suspense, useState } from "react";
import { NavLink, Outlet } from "react-router-dom";
import { BarChart3, Globe, Import, Link2, LogOut, Menu, Moon, Settings, Sun, Users, X } from "lucide-react";
import { useAuth } from "../auth";
import { Button, Spinner, cx } from "../ui";

function ThemeToggle() {
  const [dark, setDark] = useState(document.documentElement.classList.contains("dark"));
  return (
    <Button
      variant="ghost"
      size="sm"
      aria-label="Toggle dark mode"
      onClick={() => {
        const next = !dark;
        document.documentElement.classList.toggle("dark", next);
        localStorage.setItem("theme", next ? "dark" : "light");
        setDark(next);
      }}
    >
      {dark ? <Sun className="size-4" /> : <Moon className="size-4" />}
    </Button>
  );
}

export default function Layout() {
  const { user, logout, config } = useAuth();
  const [open, setOpen] = useState(false);
  const nav = [
    { to: "/", label: "Dashboard", icon: BarChart3, end: true },
    { to: "/links", label: "Links", icon: Link2 },
    { to: "/domains", label: "Domains", icon: Globe },
    ...(user!.isAdmin ? [{ to: "/users", label: "Users", icon: Users }] : []),
    ...(user!.isAdmin ? [{ to: "/import", label: "Import", icon: Import }] : []),
    { to: "/settings", label: "Settings", icon: Settings },
  ];

  const sidebar = (
    <nav className="flex h-full flex-col gap-1 p-4">
      <div className="mb-6 flex items-center gap-2.5 px-2">
        <img src="/icon.svg" alt="" className="size-8" />
        <span className="font-semibold tracking-tight">Shortener</span>
      </div>
      {nav.map((n) => (
        <NavLink
          key={n.to}
          to={n.to}
          end={n.end}
          onClick={() => setOpen(false)}
          className={({ isActive }) =>
            cx(
              "flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-colors",
              isActive
                ? "bg-cyan-50 text-cyan-700 dark:bg-cyan-950/60 dark:text-cyan-300"
                : "text-slate-600 hover:bg-slate-100 hover:text-slate-900 dark:text-slate-400 dark:hover:bg-slate-800 dark:hover:text-slate-100",
            )
          }
        >
          <n.icon className="size-4" />
          {n.label}
        </NavLink>
      ))}
      <div className="mt-auto border-t border-slate-200 pt-4 dark:border-slate-800">
        <div className="flex items-center gap-3 px-2">
          <div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-cyan-600 text-sm font-semibold text-white">
            {(user!.name || user!.email)[0].toUpperCase()}
          </div>
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium">{user!.name || user!.email}</div>
            <div className="truncate text-xs text-slate-500">{user!.isAdmin ? "Instance admin" : user!.email}</div>
          </div>
        </div>
        <div className="mt-3 flex items-center justify-between px-1">
          <ThemeToggle />
          <span className="text-xs text-slate-400">{config.version}</span>
          <Button variant="ghost" size="sm" onClick={logout} aria-label="Sign out" title="Sign out">
            <LogOut className="size-4" />
          </Button>
        </div>
      </div>
    </nav>
  );

  return (
    <div className="min-h-screen lg:pl-60">
      <aside className="fixed inset-y-0 left-0 hidden w-60 border-r border-slate-200 bg-white lg:block dark:border-slate-800 dark:bg-slate-900">
        {sidebar}
      </aside>
      {open && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div className="absolute inset-0 bg-slate-950/50" onClick={() => setOpen(false)} />
          <aside className="absolute inset-y-0 left-0 w-64 bg-white dark:bg-slate-900">
            <Button variant="ghost" size="sm" className="absolute right-2 top-4" onClick={() => setOpen(false)} aria-label="Close menu">
              <X className="size-4" />
            </Button>
            {sidebar}
          </aside>
        </div>
      )}
      <header className="sticky top-0 z-30 flex items-center gap-3 border-b border-slate-200 bg-white/80 px-4 py-3 backdrop-blur lg:hidden dark:border-slate-800 dark:bg-slate-900/80">
        <Button variant="ghost" size="sm" onClick={() => setOpen(true)} aria-label="Open menu">
          <Menu className="size-5" />
        </Button>
        <img src="/icon.svg" alt="" className="size-7" />
        <span className="font-semibold">Shortener</span>
      </header>
      <main className="mx-auto max-w-7xl px-4 py-8 sm:px-6 lg:px-8">
        <Suspense fallback={<Spinner />}>
          <Outlet />
        </Suspense>
      </main>
    </div>
  );
}
