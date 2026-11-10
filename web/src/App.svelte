<script>
  import Composer from './lib/Composer.svelte'
  import Library from './lib/Library.svelte'
  import Queue from './lib/Queue.svelte'
  import { Studio } from './lib/studio.svelte.js'

  const studio = new Studio()

  $effect(() => {
    studio.start()
    // The stream and the polling it triggers must stop when the page goes away,
    // or a closed tab leaves a reconnect loop running.
    return () => studio.stop()
  })

  const connectionLabel = $derived(
    {
      connecting: '连接中…',
      open: '已连接',
      error: '连接中断，重试中…',
      closed: '已断开',
    }[studio.connection] ?? studio.connection,
  )
</script>

<div class="app">
  <header class="top">
    <div class="brand">
      <h1>jonus</h1>
      <span class="tagline">本地 AI 音乐创作</span>
    </div>

    <div class="status">
      {#if studio.engine}
        <span class="dot {studio.engine.state}"></span>
        <span class="engine">{studio.engine.name}</span>
        <span class="sep">·</span>
      {/if}
      <span class="conn {studio.connection}">{connectionLabel}</span>
    </div>
  </header>

  {#if studio.error}
    <div class="banner">
      {studio.error}
      <button onclick={() => studio.refresh()}>重试</button>
    </div>
  {/if}

  <main>
    <div class="left">
      <section class="panel">
        <header><h2>创作</h2></header>
        <Composer {studio} />
      </section>
    </div>

    <div class="right">
      <Queue {studio} />
      <Library {studio} />
    </div>
  </main>
</div>

<style>
  .app {
    max-width: 78rem;
    margin: 0 auto;
    padding: 1.5rem 1.25rem 3rem;
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  .top {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 1rem;
    padding-bottom: 0.75rem;
    border-bottom: 1px solid var(--line);
  }

  .brand {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
  }

  h1 {
    margin: 0;
    font-size: 1.25rem;
    letter-spacing: -0.01em;
  }

  .tagline {
    font-size: 0.78rem;
    color: var(--muted);
  }

  .status {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.75rem;
    color: var(--muted);
  }

  .dot {
    width: 0.45rem;
    height: 0.45rem;
    border-radius: 50%;
    background: var(--muted);
  }

  .dot.ready {
    background: var(--ok);
  }

  .dot.unavailable {
    background: var(--danger);
  }

  .conn.error,
  .conn.closed {
    color: var(--warn);
  }

  main {
    display: grid;
    grid-template-columns: minmax(20rem, 1fr) minmax(20rem, 1.1fr);
    gap: 1rem;
    align-items: start;
  }

  .left,
  .right {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    min-width: 0;
  }

  .banner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.55rem 0.8rem;
    border-radius: 0.45rem;
    border: 1px solid color-mix(in srgb, var(--danger) 45%, transparent);
    background: color-mix(in srgb, var(--danger) 12%, transparent);
    color: var(--danger);
    font-size: 0.82rem;
  }

  @media (max-width: 60rem) {
    main {
      grid-template-columns: 1fr;
    }
  }
</style>
