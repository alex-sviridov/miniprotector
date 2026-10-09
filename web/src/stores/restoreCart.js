import { defineStore } from 'pinia'
import {
  toggleFile as toggleFileRule,
  toggleFolder as toggleFolderRule,
  ensureFileRule,
  ensureFolderRule,
} from '../utils/restoreRules'

export const useRestoreCartStore = defineStore('restoreCart', {
  state: () => ({
    rules: [],
  }),
  getters: {
    hasSelections: (state) => state.rules.length > 0,
    entries: (state) => state.rules.filter((r) => r.include),
  },
  actions: {
    // storeHost/size/damaged are optional, display-only (never sent to the API --
    // see restoreSubmission.js's toWireRule) -- captured off the catalog
    // row at selection time since the cart's rule shape otherwise has no
    // way to know either.
    toggleFile(host, path, storeHost, size, notBefore, notAfter, damaged) {
      this.rules = toggleFileRule(this.rules, host, path, { storeHost, size, notBefore, notAfter, damaged })
    },
    toggleFolder(path, notBefore, notAfter) {
      this.rules = toggleFolderRule(this.rules, path, { notBefore, notAfter })
    },
    // ensureFileSelected/ensureFolderSelected guarantee an exact rule
    // exists at (host, path)/path -- used instead of toggleFile/toggleFolder
    // when the path is already selected (possibly only via an inherited
    // ancestor folder rule, with no rule of its own to pin a version window
    // onto): toggling in that case would incorrectly flip the *resolved*
    // state to unselected (creating an exclusion rule) instead of
    // materializing the implicit selection into a real rule. See
    // CatalogView.vue's selectVersion, which is the only current caller.
    ensureFileSelected(host, path, storeHost, size, notBefore, notAfter, damaged) {
      this.rules = ensureFileRule(this.rules, host, path, { storeHost, size, notBefore, notAfter, damaged })
    },
    ensureFolderSelected(path, notBefore, notAfter) {
      this.rules = ensureFolderRule(this.rules, path, { notBefore, notAfter })
    },
    removeEntry(entry) {
      if (entry.host === null) this.toggleFolder(entry.path)
      else this.toggleFile(entry.host, entry.path)
    },
    setDestPath(entry, destPath) {
      const rule = this.rules.find((r) => r.host === entry.host && r.path === entry.path)
      if (rule) rule.destPath = destPath
    },
    // setDamaged refreshes the display-only damaged flag of an exact file
    // rule (the flag describes the version the rule resolves to, so it
    // changes when a version is pinned or reset). Not persisted anywhere;
    // a rule without the flag simply counts as not damaged.
    setDamaged(entry, damaged) {
      const rule = this.rules.find((r) => r.host === entry.host && r.path === entry.path)
      if (rule) rule.damaged = damaged
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
