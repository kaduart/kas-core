import { describe, expect, it } from "vitest";
import type { AgendaBooking } from "@/lib/kas/contract";
import { hourRange, placeBookings } from "./layout";

const SP = "America/Sao_Paulo";
const bk = (id: string, start: string, end: string, blocked = end): AgendaBooking => ({
  id,
  resource_id: "prof-ana",
  client_ref: "cli-" + id,
  starts_at: start,
  ends_at: end,
  blocked_until: blocked,
  status: "scheduled",
  buffer_min: 0,
  version: 1,
  updated_at: start,
});

describe("grade da agenda", () => {
  it("vazia mostra 08–18", () => {
    expect(hourRange([], [], SP)).toEqual({ start: 480, end: 1080 });
  });
  it("começa na primeira hora usada e cobre o buffer (mínimo 4 h)", () => {
    // 17:20–18:00 local, bloqueado até 18:20
    const r = hourRange([bk("a", "2026-11-30T20:20:00Z", "2026-11-30T21:00:00Z", "2026-11-30T21:20:00Z")], [], SP);
    expect(r).toEqual({ start: 17 * 60, end: 21 * 60 });
  });
  it("sobreposições dividem a largura; os demais ocupam a coluna toda", () => {
    const p = placeBookings(
      [
        bk("a", "2026-11-30T12:00:00Z", "2026-11-30T12:40:00Z"),
        bk("b", "2026-11-30T12:20:00Z", "2026-11-30T13:00:00Z"),
        bk("c", "2026-11-30T14:00:00Z", "2026-11-30T14:40:00Z"),
      ],
      SP,
    );
    const by = Object.fromEntries(p.map((x) => [x.item.id, [x.top, x.lane, x.lanes]]));
    expect(by).toEqual({ a: [540, 0, 2], b: [560, 1, 2], c: [660, 0, 1] });
  });
});
