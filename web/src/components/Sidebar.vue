<script setup>
import IconClients from './icons/IconClients.vue'
import IconCatalog from './icons/IconCatalog.vue'
import IconPolicies from './icons/IconPolicies.vue'
import IconStorage from './icons/IconStorage.vue'
import IconJobs from './icons/IconJobs.vue'

const NAV_ITEMS = [
  { name: 'clients', label: 'Clients', icon: IconClients },
  { name: 'catalog', label: 'Catalog', icon: IconCatalog },
  { name: 'policies', label: 'Policies', icon: IconPolicies },
  { name: 'storage', label: 'Storage', icon: IconStorage },
  { name: 'jobs', label: 'Jobs', icon: IconJobs },
]
</script>

<template>
  <nav class="w-48 h-screen bg-slate-900 text-slate-300 p-3 flex flex-col">
    <div class="flex items-center gap-2 px-2 pb-4 mb-3 border-b border-slate-800">
      <div class="w-6 h-6 rounded bg-blue-600 text-white text-xs font-bold flex items-center justify-center">
        M
      </div>
      <span class="text-slate-50 font-semibold text-sm">Miniprotector</span>
    </div>
    <div class="space-y-1">
      <router-link
        v-for="item in NAV_ITEMS"
        :key="item.name"
        :to="{ name: item.name }"
        class="flex items-center gap-2.5 pl-3 pr-2.5 py-1.5 rounded text-sm text-slate-300 hover:bg-slate-800 hover:text-white"
        active-class="bg-slate-800 text-white border-l-4 border-blue-500 pl-2"
      >
        <component :is="item.icon" class="w-4 h-4 shrink-0" />
        {{ item.label }}
      </router-link>
    </div>
  </nav>
</template>
