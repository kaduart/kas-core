import { NextResponse, type NextRequest } from "next/server";
import { bffContext, bffError } from "@/lib/kas/bff";
import { getRevenue } from "@/lib/kas/core-client";
import { isPeriod, periodRange } from "@/lib/kas/period";

export const dynamic = "force-dynamic";

/** Receita por campanha da empresa da sessão, no período pedido (GET /internal/v1/attribution/revenue). */
export async function GET(req: NextRequest) {
  const ctx = await bffContext();
  if (ctx instanceof NextResponse) return ctx;
  const period = req.nextUrl.searchParams.get("periodo") ?? "30d";
  if (!isPeriod(period)) return NextResponse.json({ error: { code: "invalid_request" } }, { status: 400 });
  const range = periodRange(period);
  try {
    const data = await getRevenue(ctx.cfg, ctx.session.tenantId, ctx.token, { from: range.from, to: range.to });
    return NextResponse.json({ ...data, periodo: { de: range.fromDate, ate: range.toDate } }, { headers: { "Cache-Control": "no-store" } });
  } catch (e) {
    return bffError(e);
  }
}
