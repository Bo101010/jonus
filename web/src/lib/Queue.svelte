<script>
  import { ago, phase, state } from './format.js'

  let { studio } = $props()

  // Only the recent past is interesting: a long-running studio accumulates
  // thousands of finished jobs, and the library is where old work is found.
  const visible = $derived(studio.jobs.slice(0, 12))

  const TERMINAL = new Set(['succeeded', 'failed', 'canceled', 'interrupted'])

  function progressOf(job) {
    const live = studio.live[job.id]
    if (live && job.state === 'running') return live.fraction ?? job.progress ?? 0
    if (job.state === 'succeeded') return 1
    return job.progress ?? 0
  }

  function labelOf(job) {
    if (job.state === 'running') return phase(job.phase) || '生成中'
    return state(job.state)
  }
</script>

<section class="panel">
  <header>
    <h2>任务队列</h2>
    <span class="count">{studio.jobs.length}</span>
  </header>

  {#if visible.length === 0}
    <p class="empty">还没有任务。在左边描述一首歌，然后点生成。</p>
  {:else}
    <ul>
      {#each visible as job (job.id)}
        <li class:terminal={TERMINAL.has(job.state)}>
          <div class="head">
            <span class="title">{job.title}</span>
            <span class="when">{ago(job.created_at)}</span>
          </div>

          <div class="meta">
            <span class="badge {job.state}">{labelOf(job)}</span>
            {#if job.state === 'running'}
              <span class="percent">{Math.round(progressOf(job) * 100)}%</span>
            {/if}
            {#if job.attempt > 1}
              <span class="attempt">第 {job.attempt} 次</span>
            {/if}
          </div>

          <div class="bar">
            <div
              class="fill {job.state}"
              style="width: {Math.round(progressOf(job) * 100)}%"
            ></div>
          </div>

          {#if job.error}
            <p class="error">{job.error}</p>
          {/if}

          <div class="row">
            {#if job.state === 'pending' || job.state === 'running'}
              <button onclick={() => studio.cancel(job.id)} disabled={studio.busy}>取消</button>
            {/if}
            {#if TERMINAL.has(job.state)}
              <button onclick={() => studio.retry(job.id)} disabled={studio.busy}>重试</button>
              <button class="danger" onclick={() => studio.remove(job.id)} disabled={studio.busy}>
                删除
              </button>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
  }

  li {
    border: 1px solid var(--line);
    border-radius: 0.5rem;
    padding: 0.6rem 0.7rem;
    background: var(--surface-2);
  }

  li.terminal {
    opacity: 0.85;
  }

  .head {
    display: flex;
    justify-content: space-between;
    gap: 0.5rem;
    align-items: baseline;
  }

  .title {
    font-size: 0.9rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .when {
    font-size: 0.7rem;
    color: var(--muted);
    flex: none;
  }

  .meta {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-top: 0.35rem;
    font-size: 0.72rem;
    color: var(--muted);
  }

  .badge {
    padding: 0.05rem 0.4rem;
    border-radius: 0.25rem;
    background: var(--line);
  }

  .badge.running {
    background: color-mix(in srgb, var(--accent) 25%, transparent);
    color: var(--accent);
  }

  .badge.succeeded {
    background: color-mix(in srgb, var(--ok) 25%, transparent);
    color: var(--ok);
  }

  .badge.failed,
  .badge.interrupted {
    background: color-mix(in srgb, var(--danger) 25%, transparent);
    color: var(--danger);
  }

  .percent {
    font-variant-numeric: tabular-nums;
  }

  .bar {
    height: 3px;
    border-radius: 2px;
    background: var(--line);
    margin-top: 0.45rem;
    overflow: hidden;
  }

  .fill {
    height: 100%;
    background: var(--accent);
    transition: width 0.25s ease-out;
  }

  .fill.succeeded {
    background: var(--ok);
  }

  .fill.failed,
  .fill.interrupted {
    background: var(--danger);
  }

  .error {
    margin: 0.4rem 0 0;
    font-size: 0.72rem;
    color: var(--danger);
    word-break: break-word;
  }

  .row {
    display: flex;
    gap: 0.4rem;
    margin-top: 0.5rem;
  }

  .row button {
    font-size: 0.72rem;
    padding: 0.15rem 0.55rem;
  }

  .count {
    font-size: 0.75rem;
    color: var(--muted);
  }

  .empty {
    margin: 0;
    font-size: 0.82rem;
    color: var(--muted);
  }
</style>
