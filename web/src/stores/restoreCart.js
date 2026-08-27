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
    toggleFile(host, path, storeHost, size, notBefore, notAfter) {
      this.rules = toggleFileRule(this.rules, host, path, { storeHost, size, notBefore, notAfter })
    },
    toggleFolder(path, notBefore, notAfter) {
      this.rules = toggleFolderRule(this.rules, path, { notBefore, notAfter })
    },
    removeEntry(entry) {
      if (entry.host === null) this.toggleFolder(entry.path)
      else this.toggleFile(entry.host, entry.path)
    },
    setDestPath(entry, destPath) {
      const rule = this.rules.find((r) => r.host === entry.host && r.path === entry.path)
      if (rule) rule.destPath = destPath
    },
    // setVersionWindow pins (or, called with 0, 0, resets) the version an
    // already-selected entry resolves to -- see restoreRules.js's toggle
    // functions for how a *new* selection gets its initial window instead
    // (the current catalog filter's receivedAfter/receivedBefore).
    setVersionWindow(entry, notBefore, notAfter) {
      const rule = this.rules.find((r) => r.host === entry.host && r.path === entry.path)
      if (rule) {
        rule.notBefore = notBefore
        rule.notAfter = notAfter
      }
    },
  },
})
