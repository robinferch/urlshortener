import { useState } from "react";
import { Link as RouterLink, useParams } from "react-router-dom";
import { ArrowLeft, Download, ExternalLink, Pencil, QrCode } from "lucide-react";
import type { Link, Stats } from "../api";
import { Kpi, StatsView, rangeOptions } from "../charts";
import { Badge, Button, Card, CopyButton, ErrorBox, PageHeader, Select, Spinner, fmtDateTime, useApi } from "../ui";
import { LinkFormModal, QRModal } from "./LinkForm";
import { linkStatus } from "./Links";

export default function LinkDetail() {
  const { id } = useParams();
  const [days, setDays] = useState(30);
  const link = useApi<Link>(`/links/${id}`);
  const stats = useApi<Stats>(`/links/${id}/stats?days=${days}`);
  const [editing, setEditing] = useState(false);
  const [qr, setQr] = useState(false);

  if (link.error) return <ErrorBox msg={link.error} />;
  if (!link.data) return <Spinner />;
  const l = link.data;
  const status = linkStatus(l);

  return (
    <>
      <RouterLink to="/links" className="mb-4 inline-flex items-center gap-1 text-sm text-slate-500 hover:text-slate-900 dark:hover:text-slate-100">
        <ArrowLeft className="size-4" /> All links
      </RouterLink>
      <PageHeader
        title={l.title || `${l.domain}/${l.slug}`}
        actions={
          <>
            <Select aria-label="Time range" value={days} onChange={(e) => setDays(Number(e.target.value))} className="w-auto!">
              {rangeOptions.map((o) => (
                <option key={o.days} value={o.days}>
                  Last {o.label}
                </option>
              ))}
            </Select>
            <a href={`/api/links/${l.id}/visits.csv`} className="inline-flex h-10 items-center gap-1.5 rounded-lg border border-slate-300 bg-white px-4 text-sm font-medium text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200 dark:hover:bg-slate-800">
              <Download className="size-4" /> CSV
            </a>
            <Button variant="secondary" onClick={() => setQr(true)}>
              <QrCode className="size-4" /> QR
            </Button>
            <Button onClick={() => setEditing(true)}>
              <Pencil className="size-4" /> Edit
            </Button>
          </>
        }
      />
      <Card className="mb-6 p-5">
        <div className="grid gap-4 text-sm sm:grid-cols-2">
          <div className="min-w-0">
            <div className="text-slate-500">Short link</div>
            <div className="flex items-center gap-1">
              <a href={l.shortUrl} target="_blank" rel="noreferrer" className="truncate font-medium text-cyan-700 hover:underline dark:text-cyan-400">
                {l.shortUrl}
              </a>
              <CopyButton text={l.shortUrl} />
              {status && <Badge tone={status.tone}>{status.label}</Badge>}
            </div>
          </div>
          <div className="min-w-0">
            <div className="text-slate-500">Destination</div>
            <a href={l.targetUrl} target="_blank" rel="noreferrer" className="flex items-center gap-1 truncate hover:underline">
              <span className="truncate">{l.targetUrl}</span> <ExternalLink className="size-3 shrink-0" />
            </a>
          </div>
          <div>
            <div className="text-slate-500">Created</div>
            <div>
              {fmtDateTime(l.createdAt)}
              {l.creatorEmail && <span className="text-slate-500"> by {l.creatorEmail}</span>}
            </div>
          </div>
          <div>
            <div className="text-slate-500">Rules</div>
            <div className="flex flex-wrap gap-1.5">
              {l.hasPassword && <Badge>Password</Badge>}
              {l.validSince && <Badge>From {fmtDateTime(l.validSince)}</Badge>}
              {l.validUntil && <Badge>Until {fmtDateTime(l.validUntil)}</Badge>}
              {l.maxVisits && (
                <Badge>
                  {l.limitUsed}/{l.maxVisits} visits used (incl. bots)
                </Badge>
              )}
              {l.iosUrl && <Badge>iOS target</Badge>}
              {l.androidUrl && <Badge>Android target</Badge>}
              {l.forwardQuery && <Badge>Forwards query</Badge>}
              {l.tags.map((t) => (
                <Badge key={t} tone="cyan">
                  #{t}
                </Badge>
              ))}
            </div>
          </div>
        </div>
      </Card>
      <ErrorBox msg={stats.error} />
      {stats.data ? (
        <>
          <div className="mb-6 grid grid-cols-2 gap-4 lg:grid-cols-3">
            <Kpi label={`Visits (${days}d)`} value={stats.data.visits} />
            <Kpi label="All-time visits" value={stats.data.allTime} />
            <Kpi label={`Bot hits (${days}d)`} value={stats.data.bots} sub="Link unfurlers, crawlers, scripts" />
          </div>
          <StatsView stats={stats.data} />
        </>
      ) : (
        <Spinner />
      )}
      <LinkFormModal open={editing} link={l} onClose={() => setEditing(false)} onSaved={(nl) => (setEditing(false), link.setData(nl))} />
      <QRModal link={qr ? l : null} onClose={() => setQr(false)} />
    </>
  );
}
