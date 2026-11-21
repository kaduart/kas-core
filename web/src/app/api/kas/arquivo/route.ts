import { NextResponse, type NextRequest } from "next/server";
import { bffContext, bffError } from "@/lib/kas/bff";
import { getRevenueFile, type RevenueFile } from "@/lib/kas/core-client";
import { isPeriod, periodRange } from "@/lib/kas/period";

export const dynamic = "force-dynamic";

const KINDS: RevenueFile[] = ["attribution-csv", "report-csv", "report-html"];

/** Baixa o arquivo pronto do Go (CSV ou relatório HTML com a marca), da empresa da sessão. */
export async function GET(req: NextRequest) {
  const ctx = await bffContext();
  if (ctx instanceof NextResponse) return ctx;
  const kind = req.nextUrl.searchParams.get("tipo") as RevenueFile;
  const period = req.nextUrl.searchParams.get("periodo") ?? "30d";
  if (!KINDS.includes(kind) || !isPeriod(period)) return NextResponse.json({ error: { code: "invalid_request" } }, { status: 400 });
  const range = periodRange(period);
  try {
    const res = await getRevenueFile(ctx.cfg, ctx.session.tenantId, ctx.token, kind, { from: range.from, to: range.to });
    const headers = new Headers({ "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" });
    headers.set("Content-Type", res.headers.get("Content-Type") ?? "application/octet-stream");
    const ext = kind === "report-html" ? "html" : "csv";
    const name = `${kind === "attribution-csv" ? "receita-por-campanha" : "relatorio-receita"}-${ctx.session.tenantId}-${range.fromDate}-a-${range.toDate}.${ext}`;
    headers.set("Content-Disposition", `attachment; filename="${name}"`);
    return new NextResponse(res.body, { status: 200, headers });
  } catch (e) {
    return bffError(e);
  }
}
