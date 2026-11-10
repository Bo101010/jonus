<script>
  import { ago, bytes, duration } from './format.js'

  let { studio } = $props()

  // One player at a time: several tracks playing at once is never what someone
  // browsing a library wants, and it makes it unclear which one the controls
  // belong to.
  let current = $state(null)

  function play(track) {
    current = current?.id === track.id ? null : track
  }
</script>

<section class="panel">
  <header>
    <h2>曲库</h2>
    <span class="count">{studio.tracks.length}</span>
  </header>

  {#if studio.tracks.length === 0}
    <p class="empty">曲库是空的。生成第一首歌之后，它会出现在这里。</p>
  {:else}
    <ul>
      {#each studio.tracks as track (track.id)}
        <li>
          <button class="play" onclick={() => play(track)} aria-label="播放">
            {current?.id === track.id ? '■' : '▶'}
          </button>

          <div class="info">
            <span class="title">{track.title}</span>
            <span class="meta">
              {duration(track.duration_ms)} · {bytes(track.size_bytes)} · {track.format} ·
              {ago(track.created_at)}
              {#if track.is_stem}<span class="stem">分轨 {track.stem_name}</span>{/if}
            </span>
          </div>

          <a class="download" href="{track.audio_url}?download=1" download>下载</a>
        </li>

        {#if current?.id === track.id}
          <li class="player">
            <!-- The audio endpoint serves byte ranges, so seeking works without
                 downloading the whole file first. -->
            <audio src={track.audio_url} controls autoplay></audio>
          </li>
        {/if}
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
    gap: 0.35rem;
  }

  li {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.45rem 0.5rem;
    border-radius: 0.4rem;
  }

  li:not(.player):hover {
    background: var(--surface-2);
  }

  li.player {
    padding: 0 0.5rem 0.6rem;
  }

  audio {
    width: 100%;
    height: 2rem;
  }

  .play {
    flex: none;
    width: 1.9rem;
    height: 1.9rem;
    border-radius: 50%;
    border: 1px solid var(--line);
    background: transparent;
    color: var(--text);
    font-size: 0.7rem;
    cursor: pointer;
    display: grid;
    place-items: center;
  }

  .play:hover {
    border-color: var(--accent);
    color: var(--accent);
  }

  .info {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }

  .title {
    font-size: 0.85rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .meta {
    font-size: 0.7rem;
    color: var(--muted);
  }

  .stem {
    margin-left: 0.4rem;
    padding: 0 0.3rem;
    border-radius: 0.2rem;
    background: var(--line);
  }

  .download {
    flex: none;
    font-size: 0.7rem;
    color: var(--muted);
    text-decoration: none;
  }

  .download:hover {
    color: var(--accent);
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
