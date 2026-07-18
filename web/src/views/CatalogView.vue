<script setup>
import { onMounted, reactive } from 'vue'
import { useCatalogStore } from '../stores/catalog'

const catalog = useCatalogStore()
const form = reactive({ sourceHost: '', pattern: '' })

function submit() {
  catalog.search({ ...form })
}

onMounted(() => {
  catalog.search({ ...form })
})
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">Catalog</h1>
    <form @submit.prevent="submit" class="flex gap-2 mb-4">
      <input v-model="form.sourceHost" placeholder="source host" class="border rounded px-2 py-1" />
      <input v-model="form.pattern" placeholder="path pattern" class="border rounded px-2 py-1" />
      <button type="submit" class="bg-blue-600 text-white rounded px-3 py-1">Search</button>
    </form>
    <p v-if="catalog.loading">Loading...</p>
    <p v-else-if="catalog.error" class="text-red-600">{{ catalog.error }}</p>
    <table v-else class="w-full text-left border-collapse">
      <thead>
        <tr class="border-b">
          <th class="py-2 pr-4">Path</th>
          <th class="py-2 pr-4">Source Host</th>
          <th class="py-2 pr-4">Size</th>
          <th class="py-2 pr-4">Mode</th>
          <th class="py-2 pr-4">Modified</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="entry in catalog.entries" :key="entry.id" class="border-b">
          <td class="py-2 pr-4">{{ entry.path }}</td>
          <td class="py-2 pr-4">{{ entry.source_host }}</td>
          <td class="py-2 pr-4">{{ entry.size }}</td>
          <td class="py-2 pr-4">{{ entry.mode }}</td>
          <td class="py-2 pr-4">{{ new Date(entry.mod_time * 1000).toLocaleString() }}</td>
        </tr>
      </tbody>
    </table>
    <div class="flex gap-2 mt-4">
      <button :disabled="!catalog.canGoPrev" @click="catalog.prevPage()" class="border rounded px-3 py-1 disabled:opacity-50">
        Prev
      </button>
      <button :disabled="!catalog.hasMore" @click="catalog.nextPage()" class="border rounded px-3 py-1 disabled:opacity-50">
        Next
      </button>
    </div>
  </div>
</template>
