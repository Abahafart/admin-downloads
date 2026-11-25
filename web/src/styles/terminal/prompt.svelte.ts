// Link between Terminal's ctrl+k handler (CommandBar.svelte) and the "›"
// prompt in its shell: ctrl+k focuses the prompt instead of opening a modal.
export const promptState = $state({ mounted: false, focusRequests: 0 });

export function requestPromptFocus(): boolean {
  if (!promptState.mounted) return false;
  promptState.focusRequests++;
  return true;
}
