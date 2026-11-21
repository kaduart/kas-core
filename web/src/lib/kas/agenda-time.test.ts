import { describe, expect, it } from "vitest";
import { addDays, fmtDayLong, hhmm, localDate, localMinutes, weekDays, weekStart, weekdayOf, zonedInstant } from "./agenda-time";

const SP = "America/Sao_Paulo";

describe("datas no fuso da empresa", () => {
  it("09:00 em São Paulo é 12:00 UTC", () => {
    expect(zonedInstant("2026-11-30", 9 * 60, SP).toISOString()).toBe("2026-11-30T12:00:00.000Z");
  });
  it("respeita horário de verão de um fuso que ainda o tem", () => {
    expect(zonedInstant("2026-07-01", 9 * 60, "America/New_York").toISOString()).toBe("2026-07-01T13:00:00.000Z");
    expect(zonedInstant("2026-12-01", 9 * 60, "America/New_York").toISOString()).toBe("2026-12-01T14:00:00.000Z");
  });
  it("meia-noite local vira a data certa mesmo perto da virada em UTC", () => {
    const d = new Date("2026-12-01T02:30:00Z"); // 23:30 do dia 30 em São Paulo
    expect(localDate(d, SP)).toBe("2026-11-30");
    expect(localMinutes(d.toISOString(), SP)).toBe(23 * 60 + 30);
  });
  it("semana começa na segunda", () => {
    expect(weekdayOf("2026-11-29")).toBe(0);
    expect(weekStart("2026-11-29")).toBe("2026-11-23");
    expect(weekStart("2026-11-30")).toBe("2026-11-30");
    expect(weekDays("2026-12-02")).toEqual(["2026-11-30", "2026-12-01", "2026-12-02", "2026-12-03", "2026-12-04", "2026-12-05", "2026-12-06"]);
  });
  it("soma dias atravessando mês e ano", () => {
    expect(addDays("2026-12-31", 1)).toBe("2027-01-01");
    expect(addDays("2027-03-01", -1)).toBe("2027-02-28");
  });
  it("formata em português", () => {
    expect(hhmm(8 * 60 + 5)).toBe("08:05");
    expect(fmtDayLong("2026-11-30")).toBe("segunda-feira, 30 de novembro");
  });
});
