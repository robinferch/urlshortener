import { useState } from "react";
import { Link as RouterLink } from "react-router-dom";
import { BarChart3, Globe, Plus } from "lucide-react";
import type { Stats } from "../api";
import { useAuth } from "../auth";
import { Kpi, StatsView, rangeOptions } from "../charts";
import { Card, Empty, ErrorBox, PageHeader, Select, Spinner, fmtNum, useApi } from "../ui";

export default function Dashboard() {
  const { domains, user } = useAuth();
  const [days, setDays] = useState(30);
  const [domainId, setDomainId] = useState(0);
  const { data, error, loading } = useApi<Stats>(`/stats?days=${days}&domainId=${domainId}`);

  if (domains.length === 0) {
    return (
      <>
        <PageHeader title="Dashboard" />
        <Card>
          <Empty icon={<Globe className="size-5" />} title="No domains yet">
            {user!.isAdmin ? (
              <>
                Add your first short domain on the <RouterLink className="text-cyan-600 underline" to="/domains">Domains</RouterLink> page.
              </>
            ) : (
              "Ask an administrator to add you to a domain."
            )}
          </Empty>
        </Card>
      </>
    );
  }

  return (
    <>
      <PageHeader
        title="Dashboard"
        description="Human visits across the links you can see. Bot traffic is counted separately."
        actions={
          <>
            <Select aria-label="Domain" value={domainId} onChange={(e) => setDomainId(Number(e.target.value))} className="w-auto!">
              <option value={0}>All domains</option>
              {domains.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.host}
                </option>
              ))}
            </Select>
            <Select aria-label="Time range" value={days} onChange={(e) => setDays(Number(e.target.value))} className="w-auto!">
              {rangeOptions.map((o) => (
                <option key={o.days} value={o.days}>
                  Last {o.label}
                </option>
              ))}
            </Select>
          </>
        }
      />
      <ErrorBox msg={error} />
      {loading && !data ? (
        <Spinner />
      ) : (
        data && (
          <div className="space-y-6">
            <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
              <Kpi label={`Visits (${days}d)`} value={data.visits} />
              <Kpi label="All-time visits" value={data.allTime} />
              <Kpi label="Links" value={data.links} />
              <Kpi label={`Bot hits (${days}d)`} value={data.bots} sub="Excluded from charts" />
            </div>
            {data.links === 0 ? (
              <Card>
                <Empty icon={<Plus className="size-5" />} title="Create your first link">
                  Head over to <RouterLink className="text-cyan-600 underline" to="/links">Links</RouterLink> to shorten a URL.
                </Empty>
              </Card>
            ) : (
              <>
                <Card className="p-5">
                  <h3 className="mb-4 text-sm font-semibold">Top links</h3>
                  {data.topLinks?.length ? (
                    <ul className="divide-y divide-slate-100 dark:divide-slate-800">
                      {data.topLinks.map((l) => (
                        <li key={l.id} className="flex items-center justify-between gap-4 py-2.5">
                          <RouterLink to={`/links/${l.id}`} className="min-w-0 hover:underline">
                            <div className="truncate text-sm font-medium text-cyan-700 dark:text-cyan-400">{l.shortUrl.replace(/^https?:\/\//, "")}</div>
                            <div className="truncate text-xs text-slate-500">{l.title || l.targetUrl}</div>
                          </RouterLink>
                          <span className="flex items-center gap-1.5 text-sm tabular-nums text-slate-600 dark:text-slate-300">
                            <BarChart3 className="size-3.5" />
                            {fmtNum(l.count)}
                          </span>
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <p className="py-6 text-center text-sm text-slate-400">No visits in this period</p>
                  )}
                </Card>
                <StatsView stats={data} />
              </>
            )}
          </div>
        )
      )}
    </>
  );
}
