import type { Metadata } from "next";
import { ModuleGate } from "@/components/modules/ModuleGate";
import { OmnichannelView } from "@/components/modules/OmnichannelView";

export const metadata: Metadata = { title: "Omnichannel · KAS Core" };

export default function Page() {
  return (
    <ModuleGate module="omnichannel"><OmnichannelView /></ModuleGate>
  );
}
