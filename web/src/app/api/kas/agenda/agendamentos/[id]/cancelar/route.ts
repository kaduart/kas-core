import { NextResponse, type NextRequest } from "next/server";
import { badRequest, bffContext, bffError, forbiddenOrigin, sameOrigin } from "@/lib/kas/bff";
import { bookingId, cancelBody } from "@/lib/kas/agenda-input";
import { cancelAgendaBooking } from "@/lib/kas/core-client";

export const dynamic = "force-dynamic";

export async function POST(req: NextRequest, ctxRoute: { params: Promise<{ id: string }> }) {
  if (!sameOrigin(req)) return forbiddenOrigin();
  const ctx = await bffContext();
  if (ctx instanceof NextResponse) return ctx;
  const id = bookingId.safeParse((await ctxRoute.params).id);
  const b = cancelBody.safeParse(await req.json().catch(() => null));
  if (!id.success || !b.success) return badRequest();
  try {
    return NextResponse.json(await cancelAgendaBooking(ctx.cfg, ctx.session.tenantId, ctx.token, id.data, b.data.versao));
  } catch (e) {
    return bffError(e);
  }
}
