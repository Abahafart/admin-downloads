// Svelte action: sets the DOM-only `indeterminate` property of a checkbox.
export function indeterminate(node: HTMLInputElement, value: boolean) {
  node.indeterminate = value;
  return {
    update(v: boolean) {
      node.indeterminate = v;
    },
  };
}
