<script lang="ts">
  // Terminal: ctrl+k focuses the "›" prompt of the shell (with "/" typed so
  // the command list shows). When the shell is not on screen (login,
  // connecting) the shared modal command bar is used.
  import SharedCommandBar from '../../components/CommandBar.svelte';
  import { app } from '../../lib/store/app.svelte';
  import { requestPromptFocus, promptState } from './prompt.svelte';

  $effect(() => {
    if (app.ui.commandOpen && promptState.mounted && requestPromptFocus()) app.ui.commandOpen = false;
  });
</script>

{#if !promptState.mounted}
  <SharedCommandBar />
{/if}
