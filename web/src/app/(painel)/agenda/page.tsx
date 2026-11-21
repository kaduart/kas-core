import type { Metadata } from "next";
import { ModuleGate } from "@/components/modules/ModuleGate";
import { AgendaView } from "@/components/agenda/AgendaView";

export const metadata: Metadata = { title: "Agenda · KAS Core" };

export default function Page() {
  return (
    <ModuleGate module="scheduling">
      <AgendaView />
    </ModuleGate>
  );
}
