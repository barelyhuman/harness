import type { ComponentChildren } from "preact";

const S = (p: { children: ComponentChildren; size?: number }) => (
  <svg width={p.size ?? 16} height={p.size ?? 16} viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round">
    {p.children}
  </svg>
);
export const IconAdd = () => <S><path d="M8 3v10M3 8h10" /></S>;
export const IconRemove = () => <S><path d="M3 8h10" /></S>;
export const IconDiscard = () => <S><path d="M3.5 6.5h6a3 3 0 0 1 0 6H6M3.5 6.5l2.5-2.5M3.5 6.5L6 9" /></S>;
export const IconOpenFile = () => <S><path d="M4 2h5l3 3v9H4zM9 2v3h3" /></S>;
export const IconRefresh = () => <S><path d="M13 8a5 5 0 1 1-1.6-3.7M13 2.5v2.8H10.2" /></S>;
export const IconCheck = () => <S><path d="M3 8.5l3.2 3.2L13 4.8" /></S>;
export const IconEllipsis = () => <S><circle cx="3.5" cy="8" r=".6" fill="currentColor" /><circle cx="8" cy="8" r=".6" fill="currentColor" /><circle cx="12.5" cy="8" r=".6" fill="currentColor" /></S>;
export const IconChevron = (p: { open: boolean }) => <S><path d={p.open ? "M4 6l4 4 4-4" : "M6 4l4 4-4 4"} /></S>;
export const IconList = () => <S><path d="M3 4h10M3 8h10M3 12h10" /></S>;
export const IconTree = () => <S><path d="M3 3h4M5 3v9M5 8h3M5 12h3M9 7h4M9 11h4" /></S>;
export const IconBranch = () => <S size={14}><circle cx="5" cy="3.5" r="1.4" /><circle cx="5" cy="12.5" r="1.4" /><circle cx="11" cy="6" r="1.4" /><path d="M5 5v6M11 7.4C11 10 5 8.5 5 11" /></S>;
export const IconUp = () => <S size={12}><path d="M8 13V3M4 7l4-4 4 4" /></S>;
export const IconDown = () => <S size={12}><path d="M8 3v10M4 9l4 4 4-4" /></S>;
export const IconInline = () => <S><path d="M2.5 3h11v10h-11zM2.5 8h11" /></S>;
export const IconSplit = () => <S><path d="M2.5 3h11v10h-11zM8 3v10" /></S>;
export const IconPrev = () => <S><path d="M8 13V3M4 7l4-4 4 4" /></S>;
export const IconNext = () => <S><path d="M8 3v10M4 9l4 4 4-4" /></S>;
export const IconClose = () => <S><path d="M4 4l8 8M12 4l-8 8" /></S>;
