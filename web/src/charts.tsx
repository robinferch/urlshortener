import { Area, AreaChart, Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import type { Bucket, Stats } from "./api";
import { Card, cx, fmtNum } from "./ui";

const regionNames = new Intl.DisplayNames(undefined, { type: "region" });
const flag = (cc: string) => String.fromCodePoint(...[...cc.toUpperCase()].map((c) => 0x1f1a5 + c.charCodeAt(0)));
function countryLabel(cc: string) {
  if (!cc) return "Unknown";
  try {
    return `${flag(cc)} ${regionNames.of(cc.toUpperCase()) ?? cc}`;
  } catch {
    return cc;
  }
}

const tooltipStyle = {
  contentStyle: { borderRadius: 8, border: "1px solid rgb(148 163 184 / 0.3)", fontSize: 12, background: "var(--tt-bg, #fff)" },
  labelStyle: { fontWeight: 600 },
};

export function Kpi({ label, value, sub }: { label: string; value: string | number; sub?: string }) {
  return (
    <Card className="p-5">
      <div className="text-sm text-slate-500 dark:text-slate-400">{label}</div>
      <div className="mt-1 text-2xl font-semibold tabular-nums tracking-tight">{typeof value === "number" ? fmtNum(value) : value}</div>
      {sub && <div className="mt-0.5 text-xs text-slate-400">{sub}</div>}
    </Card>
  );
}

function BarList({ title, items, label = (k) => k || "Unknown" }: { title: string; items: Bucket[]; label?: (k: string) => string }) {
  const max = Math.max(1, ...items.map((i) => i.count));
  return (
    <Card className="p-5">
      <h3 className="mb-4 text-sm font-semibold">{title}</h3>
      {items.length === 0 ? (
        <p className="py-6 text-center text-sm text-slate-400">No data yet</p>
      ) : (
        <ul className="space-y-2">
          {items.map((i) => (
            <li key={i.key} className="relative flex items-center justify-between gap-3 rounded-md px-2 py-1.5 text-sm">
              <div className="absolute inset-y-0 left-0 rounded-md bg-cyan-100/70 dark:bg-cyan-900/30" style={{ width: `${(i.count / max) * 100}%` }} />
              <span className="relative truncate">{label(i.key)}</span>
              <span className="relative tabular-nums text-slate-500">{fmtNum(i.count)}</span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

export function StatsView({ stats, className }: { stats: Stats; className?: string }) {
  const hours = stats.hours.map((count, h) => ({ hour: `${String(h).padStart(2, "0")}:00`, count }));
  return (
    <div className={cx("space-y-6", className)}>
      <Card className="p-5">
        <h3 className="mb-4 text-sm font-semibold">Visits per day</h3>
        <div className="h-64">
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart data={stats.series} margin={{ left: -20, right: 8, top: 4 }}>
              <defs>
                <linearGradient id="fill" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="#0891b2" stopOpacity={0.3} />
                  <stop offset="100%" stopColor="#0891b2" stopOpacity={0} />
                </linearGradient>
              </defs>
              <CartesianGrid strokeDasharray="3 3" stroke="currentColor" className="text-slate-200 dark:text-slate-800" vertical={false} />
              <XAxis dataKey="date" tick={{ fontSize: 11, fill: "#94a3b8" }} tickLine={false} axisLine={false} minTickGap={24}
                tickFormatter={(d: string) => new Date(d + "T00:00:00Z").toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" })} />
              <YAxis allowDecimals={false} tick={{ fontSize: 11, fill: "#94a3b8" }} tickLine={false} axisLine={false} />
              <Tooltip {...tooltipStyle} formatter={(v) => [fmtNum(Number(v)), "Visits"]} />
              <Area type="monotone" dataKey="count" stroke="#0891b2" strokeWidth={2} fill="url(#fill)" />
            </AreaChart>
          </ResponsiveContainer>
        </div>
      </Card>
      <div className="grid gap-6 md:grid-cols-2 xl:grid-cols-3">
        <BarList title="Top referrers" items={stats.referrers} label={(k) => k || "Direct / none"} />
        <BarList title="Countries" items={stats.countries} label={countryLabel} />
        <BarList title="Browsers" items={stats.browsers} />
        <BarList title="Operating systems" items={stats.os} />
        <BarList title="Devices" items={stats.devices} label={(k) => (k ? k[0].toUpperCase() + k.slice(1) : "Unknown")} />
        <Card className="p-5">
          <h3 className="mb-4 text-sm font-semibold">Time of day (UTC)</h3>
          <div className="h-48">
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={hours} margin={{ left: -28, right: 0 }}>
                <XAxis dataKey="hour" tick={{ fontSize: 10, fill: "#94a3b8" }} tickLine={false} axisLine={false} interval={5} />
                <YAxis allowDecimals={false} tick={{ fontSize: 10, fill: "#94a3b8" }} tickLine={false} axisLine={false} />
                <Tooltip {...tooltipStyle} cursor={{ fill: "rgb(8 145 178 / 0.08)" }} formatter={(v) => [fmtNum(Number(v)), "Visits"]} />
                <Bar dataKey="count" fill="#0891b2" radius={[3, 3, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>
          </div>
        </Card>
      </div>
    </div>
  );
}

export const rangeOptions = [
  { days: 7, label: "7 days" },
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
  { days: 365, label: "1 year" },
];
