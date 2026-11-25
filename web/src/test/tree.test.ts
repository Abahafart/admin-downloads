import { describe, expect, it } from 'vitest';
import { buildTree, compileFilter, confirmItem, folderCheck, selectedFiles, sumSize, toggleFolder, EMPTY_FILTER } from '../lib/store/tree';
import type { GrabberEntry } from '../lib/api/types';

const files = [
  ['S1/a.mkv', 100], ['S1/b.srt', 1], ['S1/sub/c.mkv', 200], ['S2/d.mp4', 300], ['e.txt', -1],
].map(([rel, size], index) => ({ index, name: String(rel).split('/').pop()!, rel_path: String(rel), size: Number(size) }));
const entry: GrabberEntry = { id: 9, url: 'u', state: 'ready', package_name: 'F', is_folder: true, files, total_size: -1, added_at: '' };

describe('LinkGrabber tree', () => {
  it('builds folders from rel_path with sizes and counts', () => {
    const t = buildTree(files);
    expect(t.folders.map((f) => f.name)).toEqual(['S1', 'S2']);
    expect(t.files.map((f) => f.name)).toEqual(['e.txt']);
    const s1 = t.folders[0];
    expect(s1.path).toBe('S1/');
    expect(s1.all).toEqual([0, 1, 2]);
    expect(s1.size).toBe(301);
    expect(s1.folders[0].path).toBe('S1/sub/');
    expect(t.unknownSize).toBe(true);
  });

  it('checks and toggles folders (tri-state)', () => {
    const t = buildTree(files);
    const ex = new Set<number>();
    expect(folderCheck(t.folders[0], ex)).toBe('all');
    ex.add(2);
    expect(folderCheck(t.folders[0], ex)).toBe('some');
    toggleFolder(t.folders[0], ex); // some -> all selected
    expect(folderCheck(t.folders[0], ex)).toBe('all');
    toggleFolder(t.folders[0], ex); // all -> none
    expect(folderCheck(t.folders[0], ex)).toBe('none');
    expect([...ex].sort()).toEqual([0, 1, 2]);
  });

  it('filters by extension, size and regex', () => {
    expect(selectedFiles(entry, new Set(), { ...EMPTY_FILTER, ext: '*.mkv' }).map((f) => f.index)).toEqual([0, 2]);
    expect(selectedFiles(entry, new Set(), { ...EMPTY_FILTER, ext: 'mkv, mp4', minSize: 150 }).map((f) => f.index)).toEqual([2, 3]);
    expect(selectedFiles(entry, new Set(), { ...EMPTY_FILTER, maxSize: 150 }).map((f) => f.index)).toEqual([0, 1, 4]);
    expect(selectedFiles(entry, new Set(), { ...EMPTY_FILTER, regex: '^S1/' }).map((f) => f.index)).toEqual([0, 1, 2]);
    expect(compileFilter({ ...EMPTY_FILTER, regex: '(' }).error).not.toBe('');
  });

  it('builds the ConfirmItem', () => {
    expect(confirmItem(entry, new Set(), EMPTY_FILTER)).toEqual({ entry_id: 9 });
    expect(confirmItem(entry, new Set([1]), EMPTY_FILTER, 'Nuevo')).toEqual({ entry_id: 9, package_name: 'Nuevo', file_indexes: [0, 2, 3, 4] });
    expect(confirmItem(entry, new Set([0, 1, 2, 3, 4]), EMPTY_FILTER)).toBeNull();
    expect(sumSize(files)).toEqual({ size: 601, unknown: true });
  });
});
