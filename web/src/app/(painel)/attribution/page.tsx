import type { Metadata } from "next";
import { ModuleGate } from "@/components/modules/ModuleGate";
import { AttributionView } from "@/components/modules/AttributionView";

export const metadata: Metadata = { title: "Attribution · KAS Core" };

export default function Page() {
  return (
    <ModuleGate module="attribution"><AttributionView /></ModuleGate>
  );
}
