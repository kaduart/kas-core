import type { Metadata } from "next";
import { ModuleGate } from "@/components/modules/ModuleGate";
import { ReportsView } from "@/components/modules/ReportsView";

export const metadata: Metadata = { title: "Relatórios · KAS Core" };

export default function Page() {
  return (
    <ModuleGate module="reports"><ReportsView /></ModuleGate>
  );
}
