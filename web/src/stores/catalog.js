import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

const MAX_PAGE_LIMIT = 500
const DEFAULT_RANGE_SECONDS = 7 * 24 * 60 * 60

function buildQuery(filters, parentDirectories, startingAfter, limit) {
  const params = new URLSearchParams()
  if (filters.receivedAfter) params.set('received_after', String(filters.receivedAfter))
  if (filters.receivedBefore) params.set('received_before', String(filters.receivedBefore))
  if (filters.sourceHosts?.length) params.set('source_hosts', filters.sourceHosts.join(','))
  if (filters.jobNames?.length) params.set('job_names', filters.jobNames.join(','))
  if (filters.pattern) params.set('pattern', filters.pattern)
  if (parentDirectories?.length) params.set('parent_directories', parentDirectories.join(','))
  if (startingAfter !== undefined) params.set('starting_after', String(startingAfter))
  params.set('limit', String(limit))
  return params.toString()
}

// buildFacetQuery mirrors buildQuery but excludes `exclude` (the facet's
// own dimension -- 'sourceHosts' for the clients facet, 'jobNames' for the
// jobs facet) so a facet list is never narrowed by its own current
// selection.
function buildFacetQuery(filters, exclude) {
  const params = new URLSearchParams()
  if (filters.receivedAfter) params.set('received_after', String(filters.receivedAfter))
  if (filters.receivedBefore) params.set('received_before', String(filters.receivedBefore))
  if (filters.pattern) params.set('pattern', filters.pattern)
  if (exclude !== 'sourceHosts' && filters.sourceHosts?.length) {
    params.set('source_hosts', filters.sourceHosts.join(','))
  }
  if (exclude !== 'jobNames' && filters.jobNames?.length) {
    params.set('job_names', filters.jobNames.join(','))
  }
  return params.toString()
}

// buildChildrenQuery narrows ListDirectoryChildren by date/host/job, same
// as buildFacetQuery, plus the parent_path being browsed. No pattern
// param: directory browsing and pattern search are mutually exclusive
// modes (see refresh()).
function buildChildrenQuery(filters, parentPath) {
  const params = new URLSearchParams()
  params.set('parent_path', parentPath ?? '')
  if (filters.receivedAfter) params.set('received_after', String(filters.receivedAfter))
  if (filters.receivedBefore) params.set('received_before', String(filters.receivedBefore))
  if (filters.sourceHosts?.length) params.set('source_hosts', filters.sourceHosts.join(','))
  if (filters.jobNames?.length) params.set('job_names', filters.jobNames.join(','))
  return params.toString()
}

export const useCatalogStore = defineStore('catalog', {
  state: () => {
    const now = Math.floor(Date.now() / 1000)
    return {
      filters: {
        pattern: '',
        receivedAfter: now - DEFAULT_RANGE_SECONDS,
        receivedBefore: now,
        sourceHosts: [],
        jobNames: [],
      },
      currentPath: null,
      entries: [],
      loading: false,
      error: null,
      clientFacets: [],
      clientFacetsLoading: false,
      clientFacetsError: null,
      jobFacets: [],
      jobFacetsLoading: false,
      jobFacetsError: null,
      directoryChildren: [],
      directoryChildrenLoading: false,
      directoryChildrenError: null,
      _searchToken: 0,
      _clientFacetsToken: 0,
      _jobFacetsToken: 0,
      _directoryChildrenToken: 0,
    }
  },
  actions: {
    async search() {
      const token = ++this._searchToken
      const parentDirectories = this.filters.pattern ? [] : this.currentPath ? [this.currentPath] : []
      try {
        await withRequest(this, async () => {
          const collected = []
          let startingAfter
          for (;;) {
            const qs = buildQuery(this.filters, parentDirectories, startingAfter, MAX_PAGE_LIMIT)
            const body = await apiFetch(`/catalog?${qs}`)
            if (token !== this._searchToken) return // superseded by a newer search
            collected.push(...body.data)
            if (!body.has_more || body.data.length === 0) break
            startingAfter = body.data[body.data.length - 1].id
          }
          if (token === this._searchToken) this.entries = collected
        })
      } catch {
        // withRequest already recorded this.error; discard any partial or
        // stale results rather than leaving a previous search's rows on screen.
        if (token === this._searchToken) this.entries = []
      }
    },
    async fetchClientFacets() {
      const token = ++this._clientFacetsToken
      await withRequest(
        this,
        async () => {
          const qs = buildFacetQuery(this.filters, 'sourceHosts')
          const body = await apiFetch(`/catalog/clients?${qs}`)
          if (token === this._clientFacetsToken) this.clientFacets = body.data
        },
        { rethrow: false, loadingKey: 'clientFacetsLoading', errorKey: 'clientFacetsError' }
      )
    },
    async fetchJobFacets() {
      const token = ++this._jobFacetsToken
      await withRequest(
        this,
        async () => {
          const qs = buildFacetQuery(this.filters, 'jobNames')
          const body = await apiFetch(`/catalog/jobs?${qs}`)
          if (token === this._jobFacetsToken) this.jobFacets = body.data
        },
        { rethrow: false, loadingKey: 'jobFacetsLoading', errorKey: 'jobFacetsError' }
      )
    },
    async fetchDirectoryChildren() {
      const token = ++this._directoryChildrenToken
      await withRequest(
        this,
        async () => {
          const qs = buildChildrenQuery(this.filters, this.currentPath)
          const body = await apiFetch(`/catalog/directories/children?${qs}`)
          if (token === this._directoryChildrenToken) this.directoryChildren = body.data
        },
        { rethrow: false, loadingKey: 'directoryChildrenLoading', errorKey: 'directoryChildrenError' }
      )
    },
    // refresh re-fetches whatever the current view needs: a pattern
    // search is a flat, cross-directory mode (no folder rows, entries
    // unscoped by currentPath); otherwise it's browse mode, which always
    // re-fetches the current folder's children, plus that folder's
    // direct files if currentPath isn't the synthetic root/Home screen.
    async refresh() {
      if (this.filters.pattern) {
        this.directoryChildren = []
        this.directoryChildrenError = null
        await this.search()
        return
      }
      await this.fetchDirectoryChildren()
      if (this.currentPath !== null) {
        await this.search()
      } else {
        this.entries = []
        this.error = null
      }
    },
    navigateTo(path) {
      this.currentPath = path
      return this.refresh()
    },
    navigateHome() {
      this.currentPath = null
      return this.refresh()
    },
  },
})
