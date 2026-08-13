import { defineStore } from 'pinia'
import { toggleFile as toggleFileRule, toggleFolder as toggleFolderRule } from '../utils/restoreRules'

export const useRestoreCartStore = defineStore('restoreCart', {
  state: () => ({
    rules: [],
  }),
  getters: {
    hasSelections: (state) => state.rules.length > 0,
    entries: (state) => state.rules.filter((r) => r.include),
  },
  actions: {
    // storeHost/size are optional, display-only (never sent to the API --
    // see restoreSubmission.js's toWireRule) -- captured off the catalog
    // row at selection time since the cart's rule shape otherwise has no
    // way to know either.
    toggleFile(host, path, storeHost, size) {
      this.rules = toggleFileRule(this.rules, host, path, { storeHost, size })
    },
    toggleFolder(path) {
      this.rules = toggleFolderRule(this.rules, path)
    },
    removeEntry(entry) {
      if (entry.host === null) this.toggleFolder(entry.path)
      else this.toggleFile(entry.host, entry.path)
    },
    // setDestPath mutates the exact rule matching (entry.host, entry.path)
    // in place -- a no-op if none matches (e.g. the entry was just
    // removed out from under an in-flight edit).
    setDestPath(entry, destPath) {
      const rule = this.rules.find((r) => r.host === entry.host && r.path === entry.path)
      if (rule) rule.destPath = destPath
    },
  },
})
