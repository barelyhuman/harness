import {
  IconArrowBackUp, IconArrowDown, IconArrowUp, IconCheck as TCheck, IconChevronDown, IconChevronRight, IconDots, IconFile, IconGitBranch,
  IconLayoutColumns, IconLayoutRows, IconList as TList, IconListTree, IconMinus, IconPlus, IconRefresh as TRefresh, IconX,
} from "@tabler/icons-preact";

const sz = 16;
const sw = 1.5;
export const IconAdd = () => <IconPlus size={sz} stroke={sw} />;
export const IconRemove = () => <IconMinus size={sz} stroke={sw} />;
export const IconDiscard = () => <IconArrowBackUp size={sz} stroke={sw} />;
export const IconOpenFile = () => <IconFile size={sz} stroke={sw} />;
export const IconRefresh = () => <TRefresh size={sz} stroke={sw} />;
export const IconCheck = () => <TCheck size={sz} stroke={sw} />;
export const IconEllipsis = () => <IconDots size={sz} stroke={sw} />;
export const IconChevron = (p: { open: boolean }) => (p.open ? <IconChevronDown size={sz} stroke={sw} /> : <IconChevronRight size={sz} stroke={sw} />);
export const IconList = () => <TList size={sz} stroke={sw} />;
export const IconTree = () => <IconListTree size={sz} stroke={sw} />;
export const IconBranch = () => <IconGitBranch size={14} stroke={sw} />;
export const IconUp = () => <IconArrowUp size={12} stroke={sw} />;
export const IconDown = () => <IconArrowDown size={12} stroke={sw} />;
export const IconInline = () => <IconLayoutRows size={sz} stroke={sw} />;
export const IconSplit = () => <IconLayoutColumns size={sz} stroke={sw} />;
export const IconPrev = () => <IconArrowUp size={sz} stroke={sw} />;
export const IconNext = () => <IconArrowDown size={sz} stroke={sw} />;
export const IconClose = () => <IconX size={sz} stroke={sw} />;
