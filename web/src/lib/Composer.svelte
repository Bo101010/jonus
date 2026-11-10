<script>
  import { Studio } from './studio.svelte.js'

  let { studio } = $props()

  let prompt = $state('')
  let style = $state('')
  let lyrics = $state('')
  let instrumental = $state(false)
  let duration = $state(60)

  const capability = $derived(studio.engine?.capabilities)
  const maxDuration = $derived(Math.round((capability?.max_duration_ms ?? 300000) / 1000))

  // The hosted engines choose a song's length from its lyrics and take no
  // length parameter, so offering a slider there would be offering a control
  // that does nothing.
  const controlsDuration = $derived(capability?.controls_duration ?? true)

  // The server requires a prompt or lyrics; saying so before the request keeps
  // the user from learning it from a 400.
  const ready = $derived(
    studio.engine?.state === 'ready' && (prompt.trim() !== '' || lyrics.trim() !== ''),
  )

  const styleSuggestions = ['民谣', 'city pop', 'lofi', '钢琴', '电子', '国风', '摇滚']

  function addStyle(tag) {
    const tags = style
      .split(',')
      .map((part) => part.trim())
      .filter(Boolean)
    if (tags.includes(tag)) return
    style = [...tags, tag].join(', ')
  }

  async function submit(event) {
    event.preventDefault()
    if (!ready) return

    try {
      await studio.create({
        prompt: prompt.trim(),
        lyrics: instrumental ? '' : lyrics.trim(),
        instrumental,
        duration_seconds: controlsDuration ? Number(duration) || 0 : 0,
        style: style.trim(),
      })
      // Keep the prompt: iterating on a description is the common case, and
      // retyping it every time would be tedious.
      lyrics = ''
    } catch {
      // studio.create has already surfaced the message.
    }
  }
</script>

<form class="composer" onsubmit={submit}>
  <label class="field">
    <span>描述这首歌</span>
    <textarea
      bind:value={prompt}
      rows="3"
      placeholder="例如：轻快的民谣，木吉他扫弦，午后阳光的感觉"
    ></textarea>
  </label>

  <label class="field">
    <span>风格标签 <em>可选</em></span>
    <input bind:value={style} placeholder="用逗号分隔，例如 民谣, 温暖" />
  </label>

  <div class="suggestions">
    {#each styleSuggestions as tag (tag)}
      <button type="button" class="chip" onclick={() => addStyle(tag)}>{tag}</button>
    {/each}
  </div>

  <div class="row">
    {#if controlsDuration}
      <label class="field">
        <span>时长 <em>{duration} 秒</em></span>
        <input type="range" min="10" max={maxDuration} step="5" bind:value={duration} />
      </label>
    {:else}
      <p class="field note">时长由引擎按歌词决定，不可指定</p>
    {/if}

    <label class="toggle">
      <input type="checkbox" bind:checked={instrumental} />
      <span>纯音乐（无人声）</span>
    </label>
  </div>

  {#if !instrumental}
    <label class="field">
      <span>歌词 <em>可选，可写 [Verse] / [Chorus] 标记</em></span>
      <textarea bind:value={lyrics} rows="5" placeholder="留空则由引擎自行生成"></textarea>
    </label>
  {/if}

  <div class="actions">
    <button type="submit" disabled={!ready || studio.busy}>
      {studio.busy ? '提交中…' : '生成'}
    </button>
    {#if studio.engine && studio.engine.state !== 'ready'}
      <span class="hint">引擎 {studio.engine.state}，暂时无法生成</span>
    {/if}
  </div>
</form>

<style>
  .composer {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }

  .field > span {
    font-size: 0.8rem;
    color: var(--muted);
    letter-spacing: 0.02em;
  }

  .field em {
    font-style: normal;
    opacity: 0.65;
  }

  .note {
    margin: 0;
    font-size: 0.8rem;
    color: var(--muted);
    padding-bottom: 0.35rem;
  }

  /* The prompt and style fields are the bare <input>/<textarea>; the range and
     checkbox are laid out by .row and .toggle instead. */
  textarea,
  input:not([type]) {
    width: 100%;
  }

  .row {
    display: flex;
    gap: 1.5rem;
    align-items: flex-end;
    flex-wrap: wrap;
  }

  .row .field {
    flex: 1 1 16rem;
  }

  .suggestions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    margin-top: -0.5rem;
  }

  .chip {
    padding: 0.15rem 0.6rem;
    border-radius: 999px;
    border: 1px solid var(--line);
    background: transparent;
    color: var(--muted);
    font-size: 0.75rem;
    cursor: pointer;
  }

  .chip:hover {
    border-color: var(--accent);
    color: var(--accent);
  }

  .toggle {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.85rem;
    color: var(--muted);
    padding-bottom: 0.35rem;
  }

  .toggle input {
    width: auto;
  }

  .actions {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .hint {
    font-size: 0.8rem;
    color: var(--warn);
  }
</style>
