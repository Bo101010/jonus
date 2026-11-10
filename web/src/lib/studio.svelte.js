import { api } from './api.js'
import { subscribeEvents } from './events.js'

// Studio is the page's state: the engine, the queue, the library, and the live
// progress coming in over the event stream.
//
// It is one object rather than a set of loose reactive variables because the
// pieces are not independent — a finished job changes both the queue and the
// library, and a retry puts a new row in the queue — so keeping them together
// is what stops them drifting apart.
export class Studio {
  engine = $state(null)
  jobs = $state([])
  tracks = $state([])
  connection = $state('connecting')

  /** live holds the newest event per job, keyed by job id. */
  live = $state({})

  error = $state('')
  busy = $state(false)

  #unsubscribe = null

  /** Start following the server and load the initial state. */
  async start() {
    this.#unsubscribe = subscribeEvents(
      (event) => this.#apply(event),
      (connection) => {
        this.connection = connection
      },
    )

    await this.refresh()
  }

  stop() {
    this.#unsubscribe?.()
    this.#unsubscribe = null
  }

  /** Reload everything from the server. */
  async refresh() {
    try {
      const [engine, jobs, tracks] = await Promise.all([
        api.engineStatus(),
        api.listJobs(),
        api.listTracks(),
      ])
      this.engine = engine
      this.jobs = jobs?.jobs ?? []
      this.tracks = tracks?.tracks ?? []
      this.error = ''
    } catch (error) {
      this.error = error.message
    }
  }

  /** Queue a generation and return the created job. */
  async create(request) {
    this.busy = true
    try {
      const created = await api.createJob(request)
      this.jobs = [created, ...this.jobs.filter((job) => job.id !== created.id)]
      this.error = ''
      return created
    } catch (error) {
      this.error = error.message
      throw error
    } finally {
      this.busy = false
    }
  }

  async cancel(id) {
    await this.#act(() => api.cancelJob(id))
  }

  async retry(id) {
    await this.#act(() => api.retryJob(id))
  }

  async remove(id) {
    await this.#act(() => api.deleteJob(id))
  }

  async #act(action) {
    this.busy = true
    try {
      await action()
      await this.refresh()
    } catch (error) {
      this.error = error.message
    } finally {
      this.busy = false
    }
  }

  /**
   * Fold one stream event into the state.
   *
   * A terminal event is the signal to reload: the event carries the state but
   * not the tracks, and the library only knows about a track once its row
   * exists, so the authoritative answer has to come from the server.
   */
  #apply(event) {
    const id = event.job_id

    if (event.state === 'pending') {
      delete this.live[id]
    } else {
      this.live[id] = event
    }

    const index = this.jobs.findIndex((job) => job.id === id)
    if (index >= 0) {
      const job = this.jobs[index]
      this.jobs[index] = {
        ...job,
        state: event.state,
        phase: event.phase || job.phase,
        progress: event.fraction,
      }
    }

    if (event.state === 'succeeded' || event.state === 'failed') {
      this.refresh()
    }
  }
}
