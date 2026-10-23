import { IconChevronDown } from "@tabler/icons-preact";
import { useEffect, useState } from "preact/hooks";
import { loadModels, models } from "./actions";
import { ModelPicker } from "./ModelPicker";

/** A "<label> [model ▾]" chip that opens the model picker in report-only mode. value is a model key. */
export function RoleChip(p: {
  label: string;
  value: string;
  onPick: (key: string) => void;
  title?: string;
  placeholder?: string;
  below?: boolean;
  autoOpen?: boolean;
  class?: string;
}) {
  const [open, setOpen] = useState(!!p.autoOpen);
  useEffect(() => { if (p.autoOpen) { void loadModels(); setOpen(true); } }, [p.autoOpen]);
  const m = models.value.find((x) => x.key === p.value);
  const name = m?.label ?? (p.value ? p.value.split("/").slice(1).join("/") || p.value : p.placeholder ?? "none");
  return (
    <span class="relative inline-flex">
      <button
        class={p.class ?? "flex h-6 items-center gap-1 rounded-md px-2 text-[11.5px] text-muted hover:bg-hover hover:text-text"}
        title={p.title ?? `${p.label} model`}
        onClick={() => { void loadModels(); setOpen(!open); }}
      >
        <span class="text-faint">{p.label}</span>
        <span class={p.value ? "text-text" : "text-faint"}>{name}</span>
        <IconChevronDown size={11} class="opacity-60" />
      </button>
      {open && (
        <>
          <div class="fixed inset-0 z-20" onClick={() => setOpen(false)} />
          <ModelPicker sessionId="" curKey={p.value} below={p.below} onClose={() => setOpen(false)} onPick={(x) => p.onPick(x.key)} />
        </>
      )}
    </span>
  );
}
