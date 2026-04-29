"use client";

import { Plus } from "lucide-react";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { useModalStore } from "@multica/core/modals";

export function MobileCreateFab() {
  const isMobile = useIsMobile();
  if (!isMobile) return null;

  return (
    <button
      onClick={() => useModalStore.getState().open("create-issue")}
      className="fixed bottom-6 right-6 z-40 flex h-14 w-14 items-center justify-center rounded-full bg-primary text-primary-foreground shadow-lg active:scale-95 transition-transform"
      aria-label="New issue"
    >
      <Plus className="h-6 w-6" />
    </button>
  );
}
