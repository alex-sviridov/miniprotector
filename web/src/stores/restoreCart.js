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
    toggleFile(host, path) {
      this.rules = toggleFileRule(this.rules, host, path)
    },
    toggleFolder(path) {
      this.rules = toggleFolderRule(this.rules, path)
    },
  },
})
