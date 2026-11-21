import { AppShell } from "@/components/shell/AppShell";
import { requireSession } from "@/lib/session";
import { TenantProvider } from "@/lib/tenant/TenantProvider";

export default async function PainelLayout({ children }: { children: React.ReactNode }) {
  const session = await requireSession();
  return (
    <TenantProvider session={session}>
      <AppShell>{children}</AppShell>
    </TenantProvider>
  );
}
