import type { Metadata } from "next";
import { ModuleGate } from "@/components/modules/ModuleGate";
import { ProcessView } from "@/components/modules/ProcessView";

export const metadata: Metadata = { title: "Process Studio · KAS Core" };

export default function Page() {
  return (
    <ModuleGate module="process"><ProcessView /></ModuleGate>
  );
}
