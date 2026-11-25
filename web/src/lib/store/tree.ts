// LinkGrabber file tree: builds folders from GrabberFile.rel_path and
// computes selections. Selection is stored as the set of *excluded* file
// indexes per entry, so files that arrive while the folder is still being
// listed are selected by default.
import type { ConfirmItem, GrabberEntry, GrabberFile } from '../api/types';

export interface TreeFolder {
  name: string;
  /** Slash-separated prefix with trailing slash ("S1/sub/"); "" for the root. */
  path: string;
  folders: TreeFolder[];
  files: GrabberFile[];
  /** Every file index below this folder. */
  all: number[];
  size: number;
  unknownSize: boolean;
}

function filePath(f: GrabberFile): string {
  return (f.rel_path || f.name).replace(/^\/+/, '');
}

export function buildTree(files: GrabberFile[] | null | undefined): TreeFolder {
  const root: TreeFolder = { name: '', path: '', folders: [], files: [], all: [], size: 0, unknownSize: false };
  for (const f of files ?? []) {
    const parts = filePath(f).split('/').filter(Boolean);
    parts.pop(); // file name
    let node = root;
    const chain: TreeFolder[] = [root];
    for (const p of parts) {
      let next = node.folders.find((x) => x.name === p);
      if (!next) {
        next = { name: p, path: node.path + p + '/', folders: [], files: [], all: [], size: 0, unknownSize: false };
        node.folders.push(next);
      }
      node = next;
      chain.push(node);
    }
    node.files.push(f);
    for (const n of chain) {
      n.all.push(f.index);
      if (f.size >= 0) n.size += f.size;
      else n.unknownSize = true;
    }
  }
  const sort = (n: TreeFolder) => {
    n.folders.sort((a, b) => a.name.localeCompare(b.name, 'es', { numeric: true }));
    n.files.sort((a, b) => filePath(a).localeCompare(filePath(b), 'es', { numeric: true }));
    n.folders.forEach(sort);
  };
  sort(root);
  return root;
}

export type Check = 'all' | 'none' | 'some';

export function folderCheck(folder: TreeFolder, excluded: ReadonlySet<number>): Check {
  let inc = 0;
  for (const i of folder.all) if (!excluded.has(i)) inc++;
  if (inc === 0) return 'none';
  return inc === folder.all.length ? 'all' : 'some';
}

/** Toggles a whole folder: if everything is selected, deselect; else select all. */
export function toggleFolder(folder: TreeFolder, excluded: Set<number>): void {
  const select = folderCheck(folder, excluded) !== 'all';
  for (const i of folder.all) {
    if (select) excluded.delete(i);
    else excluded.add(i);
  }
}

export interface FileFilter {
  /** "mkv, mp4" or "*.mkv" (case-insensitive); empty = any. */
  ext: string;
  /** Bytes; 0 = no limit. */
  minSize: number;
  maxSize: number;
  /** Regular expression over the relative path; empty = any. */
  regex: string;
}

export const EMPTY_FILTER: FileFilter = { ext: '', minSize: 0, maxSize: 0, regex: '' };

export function isFilterActive(f: FileFilter): boolean {
  return !!(f.ext.trim() || f.minSize || f.maxSize || f.regex.trim());
}

/** Returns a predicate, or an error message for an invalid regex. */
export function compileFilter(f: FileFilter): { test: (file: GrabberFile) => boolean; error: string } {
  const exts = f.ext
    .split(/[\s,;]+/)
    .map((e) => e.trim().replace(/^\*?\./, '').toLowerCase())
    .filter(Boolean);
  let re: RegExp | null = null;
  let error = '';
  if (f.regex.trim()) {
    try {
      re = new RegExp(f.regex, 'i');
    } catch {
      error = 'La expresión regular no es válida';
    }
  }
  const test = (file: GrabberFile) => {
    const p = filePath(file);
    if (exts.length) {
      const dot = file.name.lastIndexOf('.');
      const ext = dot >= 0 ? file.name.slice(dot + 1).toLowerCase() : '';
      if (!exts.includes(ext)) return false;
    }
    if (f.minSize && file.size >= 0 && file.size < f.minSize) return false;
    if (f.maxSize && file.size >= 0 && file.size > f.maxSize) return false;
    if (re && !re.test(p)) return false;
    return true;
  };
  return { test, error };
}

/** Files that will be downloaded: not excluded and passing the filter. */
export function selectedFiles(entry: GrabberEntry, excluded: ReadonlySet<number>, filter: FileFilter): GrabberFile[] {
  const { test, error } = compileFilter(filter);
  return (entry.files ?? []).filter((f) => !excluded.has(f.index) && (error ? true : test(f)));
}

/** Builds the ConfirmItem for one entry (null when nothing is selected). */
export function confirmItem(entry: GrabberEntry, excluded: ReadonlySet<number>, filter: FileFilter, packageName?: string): ConfirmItem | null {
  const files = entry.files ?? [];
  const sel = selectedFiles(entry, excluded, filter);
  if (files.length > 0 && sel.length === 0) return null;
  const item: ConfirmItem = { entry_id: entry.id };
  if (packageName && packageName !== entry.package_name) item.package_name = packageName;
  if (sel.length !== files.length) item.file_indexes = sel.map((f) => f.index);
  return item;
}

export function sumSize(files: GrabberFile[]): { size: number; unknown: boolean } {
  let size = 0;
  let unknown = false;
  for (const f of files) {
    if (f.size >= 0) size += f.size;
    else unknown = true;
  }
  return { size, unknown };
}
